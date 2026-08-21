# Dynamic DNS Zone File Updater

A small HTTP API that updates BIND zone files in place, so a router or any other
dynamic-DNS client can keep an `A` record pointed at a changing address. It is
compatible with the [Unifi Network Controller](#unifi-network-controller)'s
Dynamic DNS feature, and with anything else that can be pointed at a URL.

Demonstrates:

- [`server "http"`](../../doc/server-http.md) with route handlers and
  [basic authentication](../../doc/auth.md) whose credentials come from a
  mounted secret rather than from the config, so the config itself carries
  nothing site-specific and can be fetched at boot by a
  [`.vinit` `git` block](#deploying-from-a-vinit)
- A [functy (`.cty`)](../../doc/functy.md) function
  ([dns-zone-updater.cty](dns-zone-updater.cty)) that encapsulates the update
  logic and authorizes callers based on their authenticated username, sitting
  alongside the `.vcl` and callable from the handlers like a built-in
- An [`editor "line"`](../../doc/editor.md) block that performs idempotent,
  locked, line-by-line edits on a zone file (header timestamp, SOA serial bump,
  `A`-record replace), using "incidental" edits that don't, on their own, count
  as a real file change — so the header and serial updates are discarded when no
  record actually changed

## Endpoints

Both endpoints require [authentication](#the-credentials-file) and are `GET`
requests, because that is what dynamic-DNS clients send.

| Request | Effect |
|---|---|
| `GET /dns/update/{zone}?host=foo&ip=1.2.3.4` | Point `foo`'s `A` record at `1.2.3.4`, re-enabling it if it was disabled |
| `GET /dns/disable/{zone}?host=foo` | Disable `foo`'s `A` record by commenting it out with a `;DISABLED;` prefix |

Responses follow the dyndns2 convention that clients expect:

| Status | Body | Meaning |
|---|---|---|
| 200 | `good <ip>` | The record was changed |
| 200 | `nochg <ip>` | The record already said that; the file was left alone |
| 400 | `missing host or ip` | A required query parameter was not sent |
| 401 | — | No credential, or a wrong one |
| 403 | `Forbidden` | A valid credential, used against a zone or host it does not name |

## Configuration

### Environment variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `ZONES_DIR` | yes | — | Directory holding the zone files, one per zone, named `{zone}.zone` (e.g. `dyn.example.com.zone`) |
| `DNS_CREDENTIALS_FILE` | no | `dns-updaters.json`, resolved against `--file-path` | The [credentials file](#the-credentials-file). May be an absolute path. |
| `DNS_LISTEN` | no | `:8080` | Address to bind |

### Required flags

Both are required, and each fails loudly at startup rather than at request time
if omitted:

- **`-f` / `--file-path <dir>`** — without it `file()` is not defined at all, so
  the credentials file cannot be read.
- **`-w` / `--write-path <dir>`** — without it an `editor "line"` in its default
  file mode is a configuration error.

They constrain each other and `ZONES_DIR`:

- `--write-path` requires `--file-path`, and must be equal to or under it.
- `ZONES_DIR` must be equal to or under `--write-path`. The editor resolves
  relative paths against `--write-path` and rejects absolute paths outside it,
  which is what keeps a zone name taken from the URL from reaching anything
  else on disk.

## The credentials file

A JSON object mapping each credential's username to its password. **The username
is `"{zone}/{host}"`** — the exact record that credential may update:

```json
{
  "dyn.example.com/foo": "s3cret",
  "dyn.example.com/bar": "hunter2"
}
```

`update_dns` compares the authenticated username against `"{zone}/{host}"` built
from the request, so a credential cannot be used against any other record even
though both halves arrive from the caller.

The file is read once at startup. A missing or malformed file fails the boot,
and a username it does not list is rejected — the failure modes are all closed.
Adding or revoking a credential is a change to the secret and a restart, with no
change to any `.vcl` file, which is what makes the config safe to publish and
pin.

Give it restrictive permissions (`0400`, owned by the user Vinculum runs as); the
passwords are stored in the clear because Basic authentication compares them in
constant time, and there is no password-hash verification function to compare
against a digest instead.

## Running it locally

```sh
mkdir -p /tmp/dnsex/zones
printf '{"dyn.example.com/foo":"s3cret"}\n' > /tmp/dnsex/dns-updaters.json
chmod 0400 /tmp/dnsex/dns-updaters.json

cat > /tmp/dnsex/zones/dyn.example.com.zone <<'EOF'
;;; Updated by nobody on 1970-01-01T00:00:00Z

@   IN  SOA ns.example.com. hostmaster.example.com. (
        2024010101 ; Serial
        3600 600 604800 300 )
    IN  NS  ns.example.com.
foo IN  A   1.1.1.1
EOF

ZONES_DIR=/tmp/dnsex/zones \
  vinculum serve -f /tmp/dnsex -w /tmp/dnsex examples/dns-zone-updater/
```

Then:

```sh
curl -u 'dyn.example.com/foo:s3cret' \
  'http://localhost:8080/dns/update/dyn.example.com?host=foo&ip=1.2.3.4'
```

`vinculum check` takes the same flags, so the whole configuration — including
whether the credentials file parses — can be validated without binding a port:

```sh
ZONES_DIR=/tmp/dnsex/zones \
  vinculum check -f /tmp/dnsex -w /tmp/dnsex examples/dns-zone-updater/
```

## Deploying from a `.vinit`

Because nothing in this configuration is site-specific, it can live in a git
repository and be fetched at boot by a [`git` block](../../doc/git.md) in a
[`.vinit`](../../doc/vinit.md) file, which runs before any `.vcl` is parsed:

```hcl
# /conf/bootstrap.vinit

git "dns_updater" {
    repo = "https://github.com/example/dns-config.git"
    tag  = "v1.4.0"          # pin it — a boot should be reproducible

    fetch "config" {
        from = "dns-zone-updater"
        into = "/conf/git/dns"
    }
}
```

The [published image](../../doc/container.md) already runs
`serve -f /data -w /data/write /conf`, so the flags are in place and only the
mounts and environment are left:

```sh
docker run --rm -p 8080:8080 \
    -v "$PWD/conf:/conf:ro" \
    -v /etc/bind/zones:/data/write/zones \
    -v /etc/vinculum/dns-updaters.json:/data/dns-updaters.json:ro \
    -e ZONES_DIR=/data/write/zones \
    ghcr.io/tsarna/vinculum:latest
```

The credentials file is a natural fit for a Kubernetes `Secret` or a Docker
secret mounted read-only, entirely separate from the git-fetched configuration.

## Security notes

**Put TLS in front of it.** Basic credentials are base64-encoded, not encrypted.
Terminate TLS at a reverse proxy, or add a
[`tls {}` block](../../doc/server-http.md#tls) to the `server "http"` block.

**Don't make the `auth` block optional with `disabled`.** The pattern used in
[traffic-light/web.vcl](../traffic-light/web.vcl) — gating a mechanism on whether
its password was supplied — must not be copied here. A route whose only
mechanism is disabled becomes *unauthenticated*; see
[Turning a mechanism off](../../doc/auth.md#turning-a-mechanism-off). Failing to
boot when the secret is absent is the behavior you want.

**Zone names come from the URL.** Two independent things bound what they can
reach: the credential must name the zone (above), and the editor rejects any
path outside `--write-path`.

## Unifi Network Controller

Configure the controller's Dynamic DNS feature as:

| Setting | Value |
|---|---|
| Service | Custom |
| Hostname | `foo.dyn.example.com` |
| Username | `dyn.example.com/foo` |
| Password | the matching password from the credentials file |
| Server | `api.example.com/dns/update/dyn.example.com?host=foo&ip=%i` |

The controller substitutes the WAN address for `%i`.
