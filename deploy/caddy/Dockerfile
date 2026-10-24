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

FROM caddy:2.11.7-alpine@sha256:eb4d35b7452d1ab6171fbdbe8d9275b2301dad6dad92115d91e19fb9b24d510f
COPY --from=build /out/caddy /usr/bin/caddy
RUN caddy version && command -v getcap >/dev/null && caps="$(getcap /usr/bin/caddy)" && \
    if [ -n "$caps" ]; then echo "caddy binary has file capabilities: $caps" >&2; exit 1; fi
