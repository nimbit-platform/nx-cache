# syntax=docker/dockerfile:1

FROM node:22-alpine AS css
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm install --ignore-scripts
COPY assets ./assets
COPY components ./components
COPY internal/web ./internal/web
RUN npx @tailwindcss/cli -i assets/css/globals.css -o /out/app.css --minify

FROM golang:1.27-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /out/app.css internal/web/static/app.css
RUN go install github.com/a-h/templ/cmd/templ@v0.3.943
RUN templ generate
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/nx-cache ./cmd/server

FROM alpine:3.21
LABEL org.opencontainers.image.source="https://github.com/nimbit-platform/nx-cache"
LABEL org.opencontainers.image.url="https://github.com/nimbit-platform/nx-cache/pkgs/container/nx-cache"
LABEL org.opencontainers.image.title="nx-cache"
LABEL org.opencontainers.image.description="Self-hosted Nx remote cache"
LABEL org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates tzdata wget
WORKDIR /app
COPY --from=build /out/nx-cache /usr/local/bin/nx-cache
RUN adduser -D -H -u 65532 cache \
 && mkdir -p /data \
 && chown cache:cache /data
USER cache
EXPOSE 8080
ENV PORT=8080 CATALOG_BACKEND=s3
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD wget -qO- http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/usr/local/bin/nx-cache"]
