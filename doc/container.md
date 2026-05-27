# Container Images

Vinculum publishes three container images to GitHub Container Registry under
[`ghcr.io/tsarna`](https://github.com/tsarna?tab=packages&repo_name=vinculum):

| Image | Purpose |
|---|---|
| [`ghcr.io/tsarna/vinculum`](#vinculum-alpine) | Default runtime image. Alpine-based. |
| [`ghcr.io/tsarna/vinculum:*-minimal`](#vinculum-minimal) | Scratch-based runtime image. Smaller surface, no shell. |
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

Mount points pre-created in the image:

| Path | Purpose |
|---|---|
| `/conf` | Configuration directory (`.vcl` files) |
| `/data` | Read-only data directory (HTTP static files etc.) |
| `/data/write` | Writable data directory |

Default command:

```
serve -f /data -w /data/write /conf
```

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
- CGO state (both built with CGO enabled, or both disabled)

Any mismatch typically results in `plugin.Open` failing at load time. The
`vinculum-build` image pins all of these to the same values used by the
matching Vinculum release, so a plugin built inside the image will load
into the matching binary.

### Versioning

**The image tag must match the Vinculum release the plugin will be loaded
into.** `vinculum-build:0.36.0` produces plugins for `vinculum:0.36.0`.
Mixing versions will likely fail to load.

`:latest` and `:dev` exist but are only useful when you also run a
matching `:latest` or `:dev` runtime image — moving targets on both sides.

### Usage

The image has no default `CMD`. Bind-mount your plugin source at `/plugin`
and invoke `go build` yourself:

```sh
docker run --rm \
    -v "$PWD":/plugin -w /plugin \
    ghcr.io/tsarna/vinculum-build:0.36.0 \
    go build -buildmode=plugin -trimpath -o myplugin.so .
```

The image pre-populates the Go module cache with Vinculum's pinned
dependency set. If your plugin's `go.mod` requires the matching
`github.com/tsarna/vinculum vX.Y.Z`, the transitive dependencies will be
resolved from the cache without a network fetch.

`CGO_ENABLED=0` and `GOOS=linux` are set in the image environment to match
the runtime images. Do not override these unless you know what you're
doing.

### Loading the plugin

Mount or `COPY` the resulting `.so` into `/plugins` in the runtime image:

```dockerfile
FROM ghcr.io/tsarna/vinculum:0.36.0
COPY myplugin.so /plugins/
```

Plugin loading is not yet released; this image is published in
preparation.
