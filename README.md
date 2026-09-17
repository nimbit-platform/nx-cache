# Nx Cache

Self-hosted [Nx remote cache](https://nx.dev/docs/kb/self-hosted-caching) server. Artifacts live in S3 (or MinIO). The HTTP API follows the Nx OpenAPI spec. A small dashboard shows what is cached, how old it is, and hit/miss counts.

Inspired by [IKatsuba/nx-cache-server](https://github.com/IKatsuba/nx-cache-server), implemented in Go with Chi.

**SQLite is not required.** By default the catalog and hit/miss counters are JSON objects in the same bucket (`{prefix}.meta/`). Set `CATALOG_BACKEND=sqlite` only if you want a local index.

License: [MIT](LICENSE).

## Features

- `PUT` / `GET` / `HEAD` `/v1/cache/{hash}` with bearer auth
- S3 or S3-compatible storage (AWS, MinIO, Cloudflare R2, …)
- Does not overwrite existing hashes (`409`)
- Requires `Content-Length` (`411`); truncated uploads are discarded
- Optional read-only token (`403` on write)
- Artifacts older than **5 days** are deleted by a background job **and** after a successful save
- Dashboard (templ + HTMX + Tailwind + [shadcn-templ](https://github.com/axadrn/shadcn-templ)) with session login

## Quick start

Published image (after the `image` workflow has run on `main`):

```bash
docker pull ghcr.io/nimbit-platform/nx-cache:latest
docker run --rm -p 8080:8080 \
  -e NX_CACHE_ACCESS_TOKEN=dev-token \
  -e UI_USERNAME=admin \
  -e UI_PASSWORD=choose-a-password \
  -e SESSION_SECRET=choose-a-secret \
  -e AWS_REGION=us-east-1 \
  -e AWS_ACCESS_KEY_ID=... \
  -e AWS_SECRET_ACCESS_KEY=... \
  -e S3_BUCKET_NAME=nx-cache \
  -e S3_ENDPOINT_URL=https://s3.amazonaws.com \
  -e CATALOG_BACKEND=s3 \
  ghcr.io/nimbit-platform/nx-cache:latest
```

Local MinIO stack:

```bash
UI_PASSWORD=choose-a-password docker compose up --build
```

- Cache API: `http://localhost:8080`
- UI: `http://localhost:8080/login`
- MinIO console: `http://localhost:9001`

Point an Nx workspace at the server:

```bash
export NX_SELF_HOSTED_REMOTE_CACHE_SERVER=http://localhost:8080
export NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN=dev-token
npx nx run-many -t build
```

## Passwords and tokens

There is no in-app account database. Credentials are environment variables read at process start. Change a value, then restart the container/process.

| Who | Variables | How to set / change |
| --- | --- | --- |
| Dashboard login | `UI_USERNAME` (default `admin`), `UI_PASSWORD` (**required**) | Set in compose, Kubernetes secret, or `-e`. Restart to apply. |
| Cookie signing | `SESSION_SECRET` | Set a long random string. If unset, derived from the access token + UI password. Changing it signs everyone out. |
| Nx CLI (read/write) | `NX_CACHE_ACCESS_TOKEN` | Same value as `NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN` on the Nx client. |
| Nx CLI (read only) | `NX_CACHE_READ_TOKEN` | Optional. `GET` works, `PUT` returns `403`. |

Example:

```bash
export UI_USERNAME=admin
export UI_PASSWORD='your-new-password'
export SESSION_SECRET='a-long-random-string'
export NX_CACHE_ACCESS_TOKEN='nx-bearer-token'
```

Do not commit real passwords. Use `.env` (gitignored) or your orchestrator's secrets.

## Configuration

All settings are environment variables. Also listed in `.env.example`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | Listen port |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `NX_CACHE_ACCESS_TOKEN` | required | Bearer token for Nx (read + write) |
| `NX_CACHE_READ_TOKEN` | empty | Optional read-only bearer token |
| `UI_USERNAME` | `admin` | Dashboard login |
| `UI_PASSWORD` | required | Dashboard password |
| `SESSION_SECRET` | derived | Cookie signing secret |
| `SESSION_SECURE` | `false` | Set `true` behind HTTPS |
| `AWS_REGION` | `us-east-1` | S3 region |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | default chain | Leave empty to use instance role / IRSA |
| `S3_BUCKET_NAME` | `nx-cache` | Bucket for artifacts **and** catalog JSON |
| `S3_ENDPOINT_URL` | empty | Set for MinIO / R2 |
| `S3_PREFIX` | `nx-cache/` | Object key prefix |
| `S3_FORCE_PATH_STYLE` | true when endpoint set | Path-style S3 |
| `S3_CREATE_BUCKET` | `false` | Create the bucket on boot |
| `STORAGE_BACKEND` | `s3` | `s3` or `memory` (dev only, not durable) |
| `CATALOG_BACKEND` | `s3` | `s3` (same bucket, no extra DB) or `sqlite` |
| `SQLITE_PATH` | unset | Only used when `CATALOG_BACKEND=sqlite` (then defaults to `data/nx-cache.db`) |
| `CACHE_TTL` | `120h` | Delete artifacts older than this |
| `CLEANUP_INTERVAL` | `1h` | Background cleanup cadence |
| `CLEANUP_ON_SAVE` | `true` | Also purge expired objects after `PUT` |
| `MAX_UPLOAD_BYTES` | `2GiB` | Reject larger uploads |

With `CATALOG_BACKEND=s3`, hits/misses live at `{S3_PREFIX}.meta/stats.json` and per-hash metadata at `{S3_PREFIX}.meta/entries/{hash}.json`.

## API

Matches the [Nx self-hosted cache spec](https://nx.dev/docs/kb/self-hosted-caching):

- `PUT /v1/cache/{hash}` — upload a task output (`Content-Length` required)
- `GET /v1/cache/{hash}` — download a task output (`application/octet-stream`)
- `HEAD /v1/cache/{hash}` — existence check
- `GET /health` — liveness

Authorization: `Authorization: Bearer <token>`.

## GitHub Actions / GHCR

- `test` workflow: `go test ./...` on push and pull request
- `image` workflow: builds `linux/amd64` and `linux/arm64`, pushes to `ghcr.io/<owner>/<repo>` on `main` and `v*` tags (PRs build without pushing)

Pull:

```bash
docker pull ghcr.io/nimbit-platform/nx-cache:latest
```

If the package is private, `docker login ghcr.io` with a GitHub token that can read packages, or set the package visibility to public in GitHub → Packages.

## Development

Requires Go 1.25+ and [templ](https://templ.guide).

```bash
go install github.com/a-h/templ/cmd/templ@v0.3.943
npm install
make test
make build
```
