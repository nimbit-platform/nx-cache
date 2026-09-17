# syntax=docker/dockerfile:1.7

# CSS and Go compile on the builder CPU (not QEMU). Go then cross-compiles
# to TARGETARCH so linux/amd64 + linux/arm64 share the module/build caches.

FROM --platform=$BUILDPLATFORM node:22-alpine AS css
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --ignore-scripts --prefer-offline --no-audit --no-fund
COPY assets ./assets
COPY components ./components
COPY internal/web ./internal/web
RUN npx @tailwindcss/cli -i assets/css/globals.css -o /out/app.css --minify

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/server ./cmd/server
COPY internal ./internal
COPY components ./components
COPY utils ./utils
COPY --from=css /out/app.css internal/web/static/app.css
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/nx-cache ./cmd/server

FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 65532 cache \
 && mkdir -p /data \
 && chown 65532:65532 /data

# Scratch keeps the runtime tiny: static binary + certs/zoneinfo/user only.
FROM scratch
LABEL org.opencontainers.image.source="https://github.com/nimbit-platform/nx-cache"
LABEL org.opencontainers.image.url="https://github.com/nimbit-platform/nx-cache/pkgs/container/nx-cache"
LABEL org.opencontainers.image.title="nx-cache"
LABEL org.opencontainers.image.description="Self-hosted Nx remote cache"
LABEL org.opencontainers.image.licenses="MIT"
COPY --from=runtime /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=runtime /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=runtime /etc/passwd /etc/group /etc/
COPY --from=runtime --chown=65532:65532 /data /data
COPY --from=build /out/nx-cache /usr/local/bin/nx-cache
USER 65532:65532
EXPOSE 8080
ENV PORT=8080 CATALOG_BACKEND=s3 SQLITE_PATH=/data/nx-cache.db
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["/usr/local/bin/nx-cache", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/nx-cache"]
