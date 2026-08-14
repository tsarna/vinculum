# Git Fetch (`git` Bootstrap Block)

A `git` block is a [`.vinit`](vinit.md) bootstrap block that clones a remote
git repository during pass 1 of startup and materializes one or more subtrees of
it onto the local filesystem **before** any `.vcl` file is parsed. It lets you
keep configuration (and static assets, MCP resource files, etc.) in a
version-controlled repository and have Vinculum pull a pinned revision at boot,
rather than baking everything into the container image or a configmap.

Because the fetch completes before pass 2, the fetched `.vcl` files are
discovered by the normal VCL pipeline exactly as if they had shipped in the
image.

```hcl
# /conf/bootstrap.vinit

git "shared_config" {
    repo   = "https://github.com/example/vinculum-shared.git"
    branch = "main"

    auth {
        token = env.GIT_TOKEN
    }

    fetch "config" {
        from = "config"
        into = "/conf/git/shared"
    }

    fetch "static" {
        from = "www"
        into = "/var/www/static"
    }
}
```

The git client is implemented in **pure Go** (using
[`go-git`](https://github.com/go-git/go-git)), so it works in **every** published
image — including the scratch-based minimal image, which has no `git` binary and
no shell — and needs no external tooling.

## Syntax

```hcl
git "<label>" {
    disabled = <bool-expression>     # optional, standard .vinit semantics

    repo   = "<url>"                 # required
    branch = "<name>"                # optional, mutually exclusive with tag/commit
    tag    = "<name>"                # optional
    commit = "<sha>"                 # optional
    depth  = <int>                   # optional, default 1 (shallow); 0 = full history
    submodules = <bool>              # optional, default false

    auth { ... }                     # optional — see Authentication

    fetch "<name>" {                 # one or more required
        from = "<repo-subpath>"      # optional, default "." (repo root)
        into = "<local-path>"        # required
        overwrite = <bool>           # optional, default false
    }
}
```

The block label is a human-readable name used in logs and diagnostics; it has no
filesystem meaning. Labels must be unique across all `.vinit` files.

### Top-level attributes

<!-- vinculum:begin block-attrs git level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `repo` | string | yes |  | Repository URL to clone. |
| `branch` | string |  |  | Branch to clone. |
| `commit` | string |  |  | Commit SHA to check out. |
| `depth` | number |  | `1` | Shallow-clone depth. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `submodules` | bool |  |  | Recurse into submodules after checkout. |
| `tag` | string |  |  | Tag to clone. |

- Specify at most one of branch, tag, or commit; with none of them the remote's default branch is used.

**`repo`**

The transport is inferred from the form of the URL, and decides which `auth` attributes are legal: `https://` and `http://` take `token`, or `username` and `password`; `ssh://` and the scp-style `git@host:path` take a private key. Anything else — `file://`, a bare path — is a local clone that takes no credentials at all.

**`branch`**

Fetched directly at the configured `depth`, which is the efficient common case. With no `branch`, `tag`, or `commit`, the remote's default branch is used.

**`commit`**

The repository is cloned and the SHA checked out afterwards, so an arbitrary historical commit may not be in a shallow clone — pair it with `depth = 0`. A checkout that fails for that reason says so.

**`depth`**

`0` clones the full history, which is what pinning an arbitrary `commit` usually needs: a commit older than the shallow window is not in the clone and checkout fails.

**`disabled`**

Nothing the block would do at startup happens. It is evaluated against the `.vinit` context, where an environment variable that is not set is not an attribute of `env` at all — so gate on an optional one through `try`, as `disabled = try(env.SKIP_BOOTSTRAP, "") != ""`, rather than reading it directly and failing when it is absent.

**`tag`**

Fetched directly at the configured `depth`. Pinning a tag is what makes a boot reproducible.

#### Blocks

- `auth` (optional) — Credentials for the clone.
- `fetch "<name>"` (0..n) — One subtree of the repository, and where to put it. At least one is required.

<!-- vinculum:end block-attrs git -->

### `fetch` sub-blocks

A git block has **one or more** `fetch` sub-blocks — the schema says `0..n`
because the decode struct is a slice, but a clone with nowhere to put anything is
an error. Each copies one subtree of the cloned repository to one local
destination. All fetches share a single clone (the repository is cloned once per
git block), so declaring several destinations is cheap.

The block label names the fetch in diagnostics and logs.

<!-- vinculum:begin block-attrs git fetch level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `into` | string | yes |  | Local destination directory. |
| `from` | string |  | `.` | Path within the repository to copy. |
| `overwrite` | bool |  |  | Replace the contents of a non-empty destination. |

**`into`**

Absolute, or relative to the process working directory. Created if absent, and used as-is if it exists and is empty; a non-empty destination is an error unless `overwrite` is set. Each fetch owns its destination.

**`from`**

Must be repo-relative: a leading `/` or a `..` that escapes the repository root is an error. Naming a directory copies the whole subtree; naming a single file copies it into `into`. A path that does not exist is an error.

**`overwrite`**

The destination is cleared before the copy, so it holds the fetched tree and nothing else.

<!-- vinculum:end block-attrs git fetch -->

## Revision Selection

The clone targets exactly one revision: `commit`, `tag`, or `branch` (mutually
exclusive); absent all three, the remote default branch.

- **`branch` / `tag`** — fetched directly with the configured `depth` (default
  shallow, depth 1). The efficient common case.
- **`commit`** — the repository is cloned and the given SHA is checked out. An
  arbitrary historical commit may not be present in a shallow clone; if checkout
  fails because the commit is missing, the error suggests raising `depth` (or
  `depth = 0` for a full clone). Pinning a commit for reproducibility is usually
  paired with `depth = 0`.

**Pinning (`tag` or `commit`) is recommended for production.** A bare `branch`
(or the default branch) means the bytes Vinculum boots with can change between
restarts. Floating refs are convenient in development, but pin for reproducible
deployments.

## Authentication

The transport is inferred from the `repo` URL scheme, which determines the valid
`auth` attributes:

| URL form | Transport | Valid auth attributes |
|---|---|---|
| `https://…` / `http://…` | HTTP(S) | `token`, or `username` + `password` |
| `ssh://…` or `git@host:path` | SSH | `private_key` / `private_key_file`, `passphrase`, `known_hosts` / `insecure_ignore_host_key` |

The `auth` block is optional; omitting it means anonymous access (valid only for
a public HTTP(S) repo). Each attribute names the transport it belongs to, since
setting one that does not match `repo` is an error rather than an ignored value.

<!-- vinculum:begin block-attrs git auth level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `insecure_ignore_host_key` | bool |  | SSH only. Accept any server host key without verifying it. |
| `known_hosts` | string |  | SSH only. Path to a known_hosts file to verify the server host key against. |
| `passphrase` | string |  | SSH only. Passphrase for an encrypted private key. |
| `password` | string |  | HTTP(S) only. Password for basic auth. |
| `private_key` | string |  | SSH only. PEM-encoded private key material, inline. |
| `private_key_file` | string |  | SSH only. Path to a PEM private key on disk. |
| `token` | string |  | HTTP(S) only. Personal-access-token shorthand. |
| `username` | string |  | HTTP(S) only. Username for basic auth. |

- Specify at most one of token or username; token carries its own placeholder username.
- Specify at most one of token or password; token is sent as the password itself.
- Specify at most one of private_key or private_key_file.
- Specify at most one of known_hosts or insecure_ignore_host_key.

**`insecure_ignore_host_key`**

Turns off the verification `known_hosts` performs, and logs a warning when it does. For a trusted private network; anywhere else, provide `known_hosts`.

**`known_hosts`**

Host-key verification is on by default. With neither `known_hosts` nor `insecure_ignore_host_key`, `$HOME/.ssh/known_hosts` is used if it exists; if it does not, the fetch fails rather than trusting an unverified host. In a container, mount a known_hosts file and point this at it.

**`private_key`**

The SSH login user comes from the repo URL, defaulting to `git`.

**`token`**

Sent as HTTP basic auth with the token as the password and a placeholder username, which is what GitHub, GitLab, and Gitea PATs expect — so it needs no `username` of its own, and setting one is an error.

<!-- vinculum:end block-attrs git auth -->

Credentials almost always come from the environment (`token = env.GIT_TOKEN`,
`private_key = env.GIT_SSH_KEY`), so they are not committed to the `.vinit` file.
This composes naturally with Kubernetes secrets surfaced as environment
variables. Vinculum never logs credential values.

## Destinations

Each fetch **owns** its `into` directory:

1. **`into` does not exist** → it is created and the subtree is copied in.
2. **`into` exists and is empty** → the subtree is copied in (e.g. an empty
   pre-created mount point such as the image's `/conf/git/`).
3. **`into` exists and is non-empty** → this is **fatal** unless
   `overwrite = true` is set on the fetch, which acknowledges the destination is
   disposable and lets the fetch clear and take ownership of it.

> **Note.** Restart-time idempotency via a fetch-ownership marker file is a
> planned enhancement. Today, a non-empty unmanaged destination is always
> refused unless `overwrite = true`.

## Snapshot rule

Per the [`.vinit`](vinit.md) snapshot rule, pass 1 enumerates `.vinit` files
once, before any block runs:

- `.vinit` files **inside** fetched content are **ignored** — a git block cannot
  bootstrap further git blocks or plugins. This prevents recursive fetch chains.
- `.vcl` files inside fetched content **are** picked up, because pass 2
  enumerates `.vcl` files fresh after pass 1 finishes. This is the entire point
  of the feature.

Target a directory within the `--config` set if a fetch is meant to deliver
`.vcl` — typically a subdirectory of an already-configured `/conf`. Destinations
like `/var/www/static` deliver assets consumed at runtime (e.g. by a
`server "http"` files block) rather than `.vcl`.

## Errors

All git errors are fatal unless the block is `disabled` — bootstrap is
structurally pre-deployment, and a missing or unauthorized fetch must not let
Vinculum start half-configured. When a git block fails, startup aborts
immediately.

## Container Deployment

Both published runtime images can perform git fetches — the feature is pure Go
and needs neither a `git` binary nor a shell. The images pre-create `/conf/` and,
by convention, `/conf/git/` as the destination for fetched config:

```hcl
# mounted at /conf/configmap/bootstrap.vinit
git "app_config" {
    repo = "https://github.com/example/app-config.git"
    tag  = env.CONFIG_VERSION             # e.g. "v1.4.2" — pin for reproducibility

    auth { token = env.GIT_TOKEN }

    fetch "vcl" {
        from = "vinculum"
        into = "/conf/git/app"            # *.vcl here are loaded in pass 2
    }
}
```

Because a configmap-mounted `/conf` is read-only, fetch destinations must be a
writable path — a subdirectory not covered by the read-only mount, or a separate
writable volume. Mount configmaps at a subdirectory (e.g. `/conf/configmap/`) and
let git fetches target sibling subdirectories (e.g. `/conf/git/<name>/`); because
Vinculum scans `--config` recursively, both are picked up.

## Examples

### Public repo, whole tree, pinned tag

```hcl
git "examples" {
    repo = "https://github.com/example/vinculum-examples.git"
    tag  = "v2.0.0"

    fetch "all" {
        into = "/conf/git/examples"
    }
}
```

### Private repo over HTTPS with a PAT, two destinations

```hcl
git "shared" {
    repo   = "https://github.com/example/vinculum-shared.git"
    branch = "main"

    auth { token = env.GIT_TOKEN }

    fetch "config" {
        from = "config"
        into = "/conf/git/shared"
    }
    fetch "assets" {
        from = "www"
        into = "/var/www/static"
    }
}
```

### Private repo over SSH with a mounted key

```hcl
git "internal" {
    repo   = "git@github.com:example/internal-config.git"
    commit = env.CONFIG_COMMIT            # pinned SHA
    depth  = 0                            # full history so the commit is reachable

    auth {
        private_key_file = "/secrets/git/id_ed25519"
        known_hosts      = "/secrets/git/known_hosts"
    }

    fetch "vcl" {
        from = "vinculum"
        into = "/conf/git/internal"
    }
}
```

### Disabled outside development

```hcl
git "dev_overlay" {
    disabled = env.ENVIRONMENT != "development"

    repo   = "https://github.com/example/dev-overlay.git"
    branch = "main"

    fetch "overlay" {
        into = "/conf/git/dev"
    }
}
```
