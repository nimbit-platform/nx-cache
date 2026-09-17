# Nx Cache

Self-hosted [Nx remote cache](https://nx.dev/docs/kb/self-hosted-caching) server. Artifacts live in S3 (or MinIO), the HTTP API follows the Nx OpenAPI spec, and a small dashboard shows what is cached, how old it is, and hit/miss counts.

This is a Go rewrite of the idea behind [IKatsuba/nx-cache-server](https://github.com/IKatsuba/nx-cache-server): Chi instead of Deno, S3 for the blobs, SQLite for the catalog and stats.

## Features

- `PUT` / `GET` / `HEAD` `/v1/cache/{hash}` with bearer auth
- S3 or S3-compatible storage (AWS, MinIO, Cloudflare R2, …)
- Does not overwrite existing hashes (`409`)
- Requires `Content-Length` (`411`); truncated uploads are discarded
- Optional read-only token (`403` on write)
- Artifacts older than **5 days** are deleted by a background job **and** opportunistically after a successful save
- Dashboard (templ + HTMX + Tailwind + [shadcn-templ](https://github.com/axadrn/shadcn-templ)) with session login, cache list, age, hits vs misses

## Quick start (Docker)

```bash
docker compose up --build
```

Then:

- Cache API: `http://localhost:8080`
- UI: `http://localhost:8080/login` (default `admin` / `admin`)
- MinIO console: `http://localhost:9001` (`minioadmin` / `minioadmin`)

Point an Nx workspace at the server:

```bash
export NX_SELF_HOSTED_REMOTE_CACHE_SERVER=http://localhost:8080
export NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN=dev-token
npx nx run-many -t build
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | Listen port |
| `NX_CACHE_ACCESS_TOKEN` | required | Bearer token for Nx (read + write) |
| `NX_CACHE_READ_TOKEN` | empty | Optional read-only bearer token |
| `UI_USERNAME` | `admin` | Dashboard login |
| `UI_PASSWORD` | required | Dashboard password |
| `SESSION_SECRET` | derived | Cookie signing secret |
| `AWS_REGION` | `us-east-1` | S3 region |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | default chain | Leave empty to use instance role / IRSA |
| `S3_BUCKET_NAME` | `nx-cache` | Bucket |
| `S3_ENDPOINT_URL` | empty | Set for MinIO / R2 |
| `S3_PREFIX` | `nx-cache/` | Object key prefix |
| `S3_FORCE_PATH_STYLE` | true when endpoint set | Path-style S3 |
| `S3_CREATE_BUCKET` | `false` | Create the bucket on boot |
| `STORAGE_BACKEND` | `s3` | `s3` or `memory` (local/dev) |
| `SQLITE_PATH` | `data/nx-cache.db` | Catalog + hit/miss counters |
| `CACHE_TTL` | `120h` | Delete artifacts older than this |
| `CLEANUP_INTERVAL` | `1h` | Background cleanup cadence |
| `CLEANUP_ON_SAVE` | `true` | Also purge expired objects after `PUT` |
| `MAX_UPLOAD_BYTES` | `2GiB` | Reject larger uploads |

See `.env.example`.

## API

Matches the [Nx self-hosted cache spec](https://nx.dev/docs/kb/self-hosted-caching):

- `PUT /v1/cache/{hash}` — upload a task output (`Content-Length` required)
- `GET /v1/cache/{hash}` — download a task output (`application/octet-stream`)
- `HEAD /v1/cache/{hash}` — existence check
- `GET /health` — liveness

Authorization: `Authorization: Bearer <token>`.

## Development

Requires Go 1.25+ and [templ](https://templ.guide).

```bash
go install github.com/a-h/templ/cmd/templ@v0.3.943
npm install
make test
make build
```

UI components were added with `shadcn-templ add button card table input badge label alert separator`. Rebuild CSS after template changes with `make css`.

## Docker image

```bash
docker build -t nx-cache:local .
docker run --rm -p 8080:8080 \
  -e NX_CACHE_ACCESS_TOKEN=dev-token \
  -e UI_PASSWORD=admin \
  -e AWS_ACCESS_KEY_ID=... \
  -e AWS_SECRET_ACCESS_KEY=... \
  -e S3_BUCKET_NAME=nx-cache \
  -e S3_ENDPOINT_URL=https://s3.amazonaws.com \
  nx-cache:local
```
