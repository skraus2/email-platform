# syntax=docker/dockerfile:1
# Caddy built from source (main.go, go.mod): upstream release images can
# lag behind Go and golang.org/x/net security fixes for weeks. The pinned
# build image is the one the application uses. The official image stays the
# runtime base (user, directories, default config); only its binary is
# replaced. The new binary carries no file capability, which the kernel
# would refuse to execute under no-new-privileges; binding 80/443 needs
# none because the shared network namespace allows unprivileged ports.
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
# Same tags as the upstream release builds: no badger, MySQL or PostgreSQL
# storage backends.
RUN CGO_ENABLED=0 go build -trimpath -mod=readonly -tags nobadger,nomysql,nopgx -ldflags="-s -w" -o /out/caddy .

FROM caddy:2.11.7-alpine@sha256:e70b164da81ef698b3ad19af799ea6086ce6d9e48ed4079eb9fe26f6fbf14ee5
COPY --from=build /out/caddy /usr/bin/caddy
# The server needs neither curl nor its libraries (the health check uses
# busybox nc); removing them drops their vulnerabilities from the image.
RUN apk del --no-cache curl && ! command -v curl
# This image release predates the fixed zlib package.
RUN apk upgrade --no-cache zlib
RUN caddy version && command -v getcap >/dev/null && caps="$(getcap /usr/bin/caddy)" && \
    if [ -n "$caps" ]; then echo "caddy binary has file capabilities: $caps" >&2; exit 1; fi
