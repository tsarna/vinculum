# Container Images

Vinculum publishes three container images to GitHub Container Registry under
[`ghcr.io/tsarna`](https://github.com/tsarna?tab=packages&repo_name=vinculum):

| Image | Purpose |
|---|---|
| [`ghcr.io/tsarna/vinculum`](#vinculum-alpine) | Default runtime image. Alpine-based, cgo-enabled — **supports plugins**. |
| [`ghcr.io/tsarna/vinculum:*-minimal`](#vinculum-minimal) | Scratch-based runtime image. Smaller surface, no shell. Statically linked — **no plugin support**. |
| [`ghcr.io/tsarna/vinculum-build`](#vinculum-build) | Build environment for compiling Go plugins (`.so`) ABI-compatible with a specific Vinculum release. |

All three images are published for `linux/amd64` and `linux/arm64`.

## Tags

The three images share the same tag scheme, derived from the source branch
or git tag:

| Source | Tags |
|---|---|
| Git tag `vX.Y.Z` | `:X.Y.Z`, `:X.Y`, `:X` |
| Branch `main` | `:latest` |
| Branch `develop` | `:dev` |

For production deployments, pin to a specific patch version
(e.g. `ghcr.io/tsarna/vinculum:0.36.0`).

---

## `vinculum` (alpine) <a id="vinculum-alpine"></a>

The default runtime image. Built from [`Dockerfile`](../Dockerfile) on an
`alpine:3.23` base. Includes `ca-certificates-bundle` and `tzdata`. Runs as
UID 65534 (`nobody`).

This is the **plugin-capable** image: its binary is built cgo-enabled and
dynamically linked against musl, so it can load Go plugins dropped into
`/plugins`. (Plugins require a dynamically linked host; the minimal image
cannot load them.)

Mount points pre-created in the image:

| Path | Purpose |
|---|---|
| `/conf` | Configuration directory (`.vcl` and `.vinit` files) |
| `/conf/git` | Conventional destination for [git fetches](git.md) |
| `/data` | Read-only data directory (HTTP static files etc.) |
| `/data/write` | Writable data directory |

Default command:

```
serve /conf
```

The image's other settings are carried as environment variables rather than
command arguments, because `docker run` replaces the whole command as soon as
you pass arguments of your own:

| Variable | Value | Effect |
|---|---|---|
| `VINCULUM_FILE_PATH` | `/data` | Enables `file()`, `fileexists()`, `fileset()`, rooted there. |
| `VINCULUM_WRITE_PATH` | `/data/write` | Enables the file-write functions, rooted there. |
| `VINCULUM_PLUGIN_PATH` | `/plugins` | Where [plugins](plugins.md) are loaded from. |
| `VINCULUM_HEALTH_LISTEN` | `:8081` | Serves `/readyz`, `/livez`, and `/healthz` on port 8081. See [health.md](health.md). |

So they survive a command you supply yourself, and each is separately
overridable — an empty value turns one off:

```sh
docker run … ghcr.io/tsarna/vinculum serve /myconf              # keeps all three
docker run -e VINCULUM_FILE_PATH=/mnt/files … ghcr.io/tsarna/vinculum
docker run -e VINCULUM_PLUGIN_PATH= … ghcr.io/tsarna/vinculum
```

To turn off file access, clear both paths — a write path has to be under a
file path, so clearing only `VINCULUM_FILE_PATH` is rejected at startup:

```sh
docker run -e VINCULUM_FILE_PATH= -e VINCULUM_WRITE_PATH= … ghcr.io/tsarna/vinculum
```

### Health probes

Readiness and liveness probes work with no health configuration at all: the
image serves `/readyz`, `/livez`, and `/healthz` on port 8081, which is
`EXPOSE`d but published through no Service by default.

```sh
docker run -e VINCULUM_HEALTH_LISTEN=:9000 … ghcr.io/tsarna/vinculum   # move it
docker run -e VINCULUM_HEALTH_LISTEN= … ghcr.io/tsarna/vinculum        # turn it off
```

The body says only `ok` or `not ready`. A listener that comes up whether or not
you asked for it must not also volunteer the names of every component and the
text of every connection error, so `?verbose` is ignored unless you set
`VINCULUM_HEALTH_VERBOSE=true`. See [health.md](health.md#http-endpoints).

See [cli-env.md](cli-env.md) for the general rule — every flag has such a
variable, whether or not the image sets it.

Typical usage:

```sh
docker run --rm -p 8080:8080 \
    -v "$PWD/conf:/conf:ro" \
    -v "$PWD/data:/data" \
    ghcr.io/tsarna/vinculum:0.36.0
```

## `vinculum:*-minimal` <a id="vinculum-minimal"></a>

A `FROM scratch` variant built from
[`Dockerfile.minimal`](../Dockerfile.minimal). Contains only the static
binary, CA certificates, timezone data, `/etc/passwd`, `/etc/group`, and the
same pre-created mount-point directories as the alpine image.

Use this image when you want the smallest attack surface and don't need a
shell, package manager, or any other tooling inside the container.

**This image cannot load plugins.** The binary is statically linked
(`CGO_ENABLED=0`) and `scratch` has no dynamic loader or libc, so
`plugin.Open` (a `dlopen`) cannot work. The `/plugins` directory and the
`VINCULUM_PLUGIN_PATH` setting are present for parity but are inert unless a
config declares a `plugin` block — in which case startup fails. Use the default
(alpine) image if you need plugins.

It carries the same command and the same environment defaults as the alpine
image.

Tag suffix is `-minimal`, e.g. `ghcr.io/tsarna/vinculum:0.36.0-minimal`,
`:latest-minimal`, `:dev-minimal`.

## `vinculum-build` <a id="vinculum-build"></a>

Build environment for compiling Go plugins as `.so` files that load into a
matching Vinculum release. Built from [`Dockerfile.build`](../Dockerfile.build)
on the same `golang:1.26-alpine` builder used for the runtime images.

### Why this image exists

Go plugins (`-buildmode=plugin`) are extremely version-sensitive. The
plugin and the host binary must agree on:

- The Go toolchain version (down to the patch release)
- Every shared module's version
- Build flags (`-trimpath`, `-buildvcs`, build tags)
- GOOS / GOARCH and the C library (both musl, as in the alpine images)

cgo must be enabled on both sides: `-buildmode=plugin` requires external
(cgo) linking, and the host must be dynamically linked to `dlopen` the
plugin. This image and the default runtime image are both cgo-enabled to
match. (The static minimal image cannot load plugins.)

Any mismatch typically results in `plugin.Open` failing at load time. The
`vinculum-build` image pins all of these to the same values used by the
matching Vinculum release, so a plugin built inside the image will load
into the matching binary.

### Versioning

**The image tag must match the Vinculum release the plugin will be loaded
into.** `vinculum-build:0.36.0` produces plugins for `vinculum:0.36.0`.
Mixing versions will likely fail to load.

Your plugin's `go.mod` must also `require github.com/tsarna/vinculum` at
the **exact same version** as the runtime image. The default runtime image
is built by installing that versioned module (`go install …@vX.Y.Z`), and
`-buildmode=plugin` bakes the module path+version into every package's
build ID — so a plugin requiring a different version (or a `replace` to
local source) loads with "different version of package …". For a tagged
release, `require github.com/tsarna/vinculum vX.Y.Z`.

`:latest` and `:dev` runtime images are built from a **commit
pseudo-version**, not a clean tag. To build a plugin for them you must
`require` that exact pseudo-version (run `vinculum version` in the image,
or `go get github.com/tsarna/vinculum@<commit>`). For this reason plugins
are best targeted at tagged releases.

### Usage

The image has no default `CMD`. Bind-mount your plugin source at `/plugin`
and run the bundled **`vinculum-plugin-build`** wrapper:

```sh
docker run --rm \
    -v "$PWD":/plugin -w /plugin \
    ghcr.io/tsarna/vinculum-build:0.37.0 \
    vinculum-plugin-build -o myplugin.so .
```

The wrapper removes the three common ways a plugin drifts out of ABI
compatibility and would fail to load:

- it forces the toolchain (`GOTOOLCHAIN=local`), `cgo`, and the required
  build flags (`-buildmode=plugin -trimpath`) so you can't omit them;
- it verifies your `go.mod` requires the **exact** `github.com/tsarna/vinculum`
  version this image targets; and
- it diffs your plugin's compiled dependency closure against Vinculum's
  pinned versions and **fails the build** (with the offending modules
  named) if any shared, compiled dependency differs — turning a cryptic
  runtime `plugin.Open` failure into an actionable build error.

You can still call `go build -buildmode=plugin -trimpath …` directly, but
then those guarantees are on you.

The image pre-populates the Go module cache with Vinculum's pinned
dependency set. If your plugin's `go.mod` requires the matching
`github.com/tsarna/vinculum vX.Y.Z`, the transitive dependencies will be
resolved from the cache without a network fetch.

`CGO_ENABLED=1` and `GOOS=linux` are set in the image environment to match
the default (plugin-capable) runtime image. Plugins must be built
cgo-enabled, so do not set `CGO_ENABLED=0` — `-buildmode=plugin` would
fail with `requires external (cgo) linking, but cgo is not enabled`.

### Loading the plugin

Mount or `COPY` the resulting `.so` into `/plugins` in the runtime image:

```dockerfile
FROM ghcr.io/tsarna/vinculum:0.36.0
COPY myplugin.so /plugins/
```

See [plugins.md](plugins.md) for the `plugin` block syntax,
`--plugin-path` semantics, and ABI-compatibility considerations, and
[vinit.md](vinit.md) for the `.vinit` file format that declares
plugins.
