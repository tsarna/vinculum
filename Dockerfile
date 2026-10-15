# syntax=docker/dockerfile:1
#
# Alpine-based Vinculum image. This is the plugin-capable image: the binary
# is built cgo-enabled and dynamically linked against musl, so it can load
# Go plugins (.so files) via -buildmode=plugin. Build plugins with the
# matching vinculum-build image (same tag) and drop them in /plugins.
#
# (The scratch-based minimal image, Dockerfile.minimal, is statically
# linked and cannot load plugins.)
#
# The host binary is built by `go install github.com/tsarna/vinculum@<ref>`
# rather than from the working tree. This is REQUIRED for plugin support:
# a plugin imports vinculum as a versioned dependency, and -buildmode=plugin
# bakes the module path+version (e.g. .../vinculum@v0.37.0/...) into each
# package's build ID. The host must reference those same packages by the
# identical module@version, which only a real module install produces — a
# `go build .` of the working tree gives the packages "main-module"
# identity and the plugin then fails to load with "different version of
# package ...". VINCULUM_REF must therefore be the published tag the plugin
# author will `require` (vX.Y.Z). For branch images (:latest/:dev) it is a
# commit pseudo-version, and plugins must pin that exact pseudo-version.
#
#   docker build -t vinculum --build-arg VINCULUM_REF=v0.37.0 .

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.27-alpine AS builder

# gcc + musl-dev provide the C toolchain cgo needs (a plugin host must be
# cgo/dynamically linked to dlopen plugins). git is needed to fetch the
# module directly (GOPRIVATE bypasses the proxy/sumdb, which may lag a
# freshly pushed tag).
RUN apk add --no-cache git gcc musl-dev

ARG VERSION=dev
ARG COMMIT=""
ARG BUILD_TIME=""
# The go-installable ref of github.com/tsarna/vinculum to build: a release
# tag (vX.Y.Z) for releases, or a commit SHA / pseudo-version for branch
# builds. Required.
ARG VINCULUM_REF

ENV CGO_ENABLED=1 \
    GOOS=linux \
    GOPRIVATE=github.com/tsarna/vinculum

# CGO_ENABLED=1 produces a dynamically linked (musl) binary that can load
# plugins. -trimpath must match what plugin authors use so the plugin and
# host agree on package build IDs.
RUN test -n "$VINCULUM_REF" || { echo "ERROR: VINCULUM_REF build-arg is required" >&2; exit 1; }
RUN go install -trimpath \
    -ldflags="-s -w \
      -X github.com/tsarna/vinculum/version.Version=${VERSION} \
      -X github.com/tsarna/vinculum/version.Commit=${COMMIT} \
      -X github.com/tsarna/vinculum/version.BuildTime=${BUILD_TIME}" \
    "github.com/tsarna/vinculum@${VINCULUM_REF}"

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.24

RUN apk add --no-cache ca-certificates-bundle tzdata \
    && mkdir -p /conf /conf/git /data /data/write /plugins \
    && chown 65534:65534 /conf/git /data/write

COPY --from=builder /go/bin/vinculum /vinculum

USER 65534

# Docker replaces the entire CMD as soon as a run supplies arguments
# of its own, so a flag carried there would be lost by `docker run <image>
# serve /myconf` — taking file functions and the plugin directory with it.
# Each of these is overridable with -e, and an empty value turns one off:
# -e VINCULUM_PLUGIN_PATH=. Clear the two file paths together, since a write
# path has to sit under a file path.
ENV VINCULUM_FILE_PATH=/data \
    VINCULUM_WRITE_PATH=/data/write \
    VINCULUM_PLUGIN_PATH=/plugins \
    VINCULUM_HEALTH_LISTEN=:8081

# Readiness and liveness probes work out of the box, with no health config at
# all: /readyz, /livez, and /healthz are served here. The body says only "ok" or
# "not ready" — a listener that comes up whether or not the operator asked for
# it must not also volunteer the names of every component and the text of every
# connection error. Set VINCULUM_HEALTH_VERBOSE=true to allow ?verbose,
# VINCULUM_HEALTH_LISTEN=:9000 to move it, or an empty value to turn it off.
EXPOSE 8081

ENTRYPOINT ["/vinculum"]
CMD ["serve", "/conf"]
