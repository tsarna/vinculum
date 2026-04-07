# syntax=docker/dockerfile:1
#
# Alpine-based Vinculum image.
#
#   docker build -t vinculum .

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/vinculum .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.23

RUN apk add --no-cache ca-certificates-bundle tzdata \
    && mkdir -p /conf /data /data/write

COPY --from=builder /out/vinculum /vinculum

USER 65534

ENTRYPOINT ["/vinculum"]
CMD ["serve", "-f", "/data", "-w", "/data/write", "/conf"]
