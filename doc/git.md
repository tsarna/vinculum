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

| Attribute | Type | Required | Notes |
|---|---|---|---|
| `disabled` | bool expr | no | Skip the entire block — no clone, no fetch. Evaluated against the minimal `.vinit` context (`env.*` + stdlib). |
| `repo` | string | **yes** | Repository URL. Transport is inferred from the scheme (see [Authentication](#authentication)). |
| `branch` | string | no | Branch to clone. Mutually exclusive with `tag` and `commit`. |
| `tag` | string | no | Tag to clone. Mutually exclusive with `branch` and `commit`. |
| `commit` | string | no | Full commit SHA to check out. Mutually exclusive with `branch` and `tag`. |
| `depth` | number | no | Shallow-clone depth. Default `1` (tip only); `0` means a full clone. |
| `submodules` | bool | no | Recurse into submodules after checkout. Default `false`. |

If none of `branch` / `tag` / `commit` is given, the remote's default branch is
used. Specifying more than one is a fatal error.

### `fetch` sub-blocks

A git block has **one or more** `fetch` sub-blocks. Each copies one subtree of
the cloned repository to one local destination. All fetches share a single clone
(the repository is cloned once per git block), so declaring several destinations
is cheap.

| Attribute | Type | Required | Notes |
|---|---|---|---|
| label | string | yes | A name for the fetch, used in diagnostics/logging. |
| `from` | string | no | Path **within the repository** to copy. Default `"."` (whole repo). Must be repo-relative (no leading `/`, no `..`). A `from` that does not exist is fatal. May name a directory (whole subtree copied) or a single file (copied into `into`). |
| `into` | string | **yes** | Local destination directory. Absolute or relative to the process working directory. Created if absent. See [Destinations](#destinations). |
| `overwrite` | bool | no | Permit replacing a non-empty destination. Default `false`. |

The repository's `.git` directory is never copied into a destination.

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
a public HTTP(S) repo).

| Attribute | Transport | Notes |
|---|---|---|
| `token` | HTTP(S) | Personal-access-token shorthand. Sent as HTTP Basic auth with the token as the password and a placeholder username — what GitHub/GitLab/Gitea PATs expect. Mutually exclusive with `username`/`password`. |
| `username` / `password` | HTTP(S) | Basic-auth credentials. |
| `private_key` | SSH | PEM-encoded private key material (inline). Mutually exclusive with `private_key_file`. |
| `private_key_file` | SSH | Path to a PEM private key on disk. |
| `passphrase` | SSH | Passphrase for an encrypted private key. |
| `known_hosts` | SSH | Path to a `known_hosts` file used to verify the server host key. |
| `insecure_ignore_host_key` | SSH | Disable host-key verification entirely. Default `false`. Logs a warning when true. Mutually exclusive with `known_hosts`. |

Credentials almost always come from the environment (`token = env.GIT_TOKEN`,
`private_key = env.GIT_SSH_KEY`), so they are not committed to the `.vinit` file.
This composes naturally with Kubernetes secrets surfaced as environment
variables. Vinculum never logs credential values.

**Host-key verification (SSH).** Verification defaults to **on**: if neither
`known_hosts` nor `insecure_ignore_host_key` is set, the default user
`known_hosts` (`$HOME/.ssh/known_hosts`) is used if present, otherwise the fetch
fails asking you to provide `known_hosts` or to set
`insecure_ignore_host_key = true`. In containers, mount a `known_hosts` file and
point `known_hosts` at it, or — for a trusted private network — set
`insecure_ignore_host_key = true`.

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
