# syntax=docker/dockerfile:1
#
# Alpine-based Vinculum image.
#
#   docker build -t vinculum .

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/vinculum .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates-bundle tzdata \
    && mkdir -p /conf /data

COPY --from=builder /out/vinculum /vinculum

ENTRYPOINT ["/vinculum"]
CMD ["server", "-f", "/data", "/conf"]
