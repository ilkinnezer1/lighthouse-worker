FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/worker ./cmd/worker


FROM alpine:3.22

ARG TARGETARCH=amd64
ARG PACK_VERSION=0.40.8
ARG RAILPACK_VERSION=0.35.0
ARG NIXPACKS_VERSION=1.41.0

RUN apk add --no-cache ca-certificates git docker-cli docker-cli-compose

RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) pack_slug=linux;       railpack_slug=x86_64-unknown-linux-musl; nixpacks_slug=x86_64-unknown-linux-musl ;; \
      arm64) pack_slug=linux-arm64; railpack_slug=arm64-unknown-linux-musl;  nixpacks_slug=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    apk add --no-cache --virtual .fetch curl tar; \
    curl -fsSL "https://github.com/buildpacks/pack/releases/download/v${PACK_VERSION}/pack-v${PACK_VERSION}-${pack_slug}.tgz" \
      | tar -xzf - -C /usr/local/bin pack; \
    curl -fsSL "https://github.com/railwayapp/railpack/releases/download/v${RAILPACK_VERSION}/railpack-v${RAILPACK_VERSION}-${railpack_slug}.tar.gz" \
      | tar -xzf - -C /usr/local/bin railpack; \
    curl -fsSL "https://github.com/railwayapp/nixpacks/releases/download/v${NIXPACKS_VERSION}/nixpacks-v${NIXPACKS_VERSION}-${nixpacks_slug}.tar.gz" \
      | tar -xzf - -C /usr/local/bin nixpacks; \
    apk del .fetch; \
    chmod 0755 /usr/local/bin/pack /usr/local/bin/railpack /usr/local/bin/nixpacks; \
    docker --version; docker compose version; git --version; \
    pack --version; railpack --version; nixpacks --version

COPY --from=builder /out/worker /usr/local/bin/worker

# The daemon resolves bind-mount paths from user compose files, so WORK_DIR must name the same path on the host.
ENV WORK_DIR=/app/data/deployments
RUN mkdir -p "$WORK_DIR"
VOLUME ["/app/data"]

WORKDIR /app
EXPOSE 8081

# Root because the mounted docker.sock is root:docker with a host-specific GID no baked-in UID can match.
ENTRYPOINT ["worker"]
