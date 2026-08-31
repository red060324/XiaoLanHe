# Public Deployment

XiaoLanHe deploys as one Go container plus one PostgreSQL database with the
`vector` extension. The React build is embedded in the same image, so browser
pages and `/api/` use one origin. SearXNG is optional.

## Render Blueprint

The smallest hosted setup uses the repository's `render.yaml`:

1. Fork or push the repository to GitHub.
2. In Render, create a Blueprint from the repository.
3. Store `XLH_AI_API_KEY` in Render's secret configuration.
4. Set `XLH_PUBLIC_ORIGIN` to the final service URL if the service name changes.
5. Open `https://<service-name>.onrender.com` and verify `/healthz` and
   `/readyz`.

Render supplies the `onrender.com` subdomain. Buying a custom domain is
optional. A custom domain only changes DNS/TLS configuration and
`XLH_PUBLIC_ORIGIN`; it does not require a code change.

## Any Docker Host

Use a PostgreSQL provider or self-hosted PostgreSQL that allows
`CREATE EXTENSION vector`, then build and run:

```bash
docker build -t xiaolanhe:local .
docker run --name xiaolanhe --env-file .env -p 8088:8088 xiaolanhe:local
```

Put a public TLS reverse proxy or hosting load balancer in front of port 8088.
For a custom domain, point its DNS record at that proxy and set
`XLH_PUBLIC_ORIGIN=https://your-domain.example` plus `XLH_COOKIE_SECURE=true`.
Do not expose PostgreSQL publicly.

## Configuration

| Variable | Required | Purpose |
|---|---:|---|
| `XLH_DATABASE_URL` | yes | PostgreSQL connection URL |
| `XLH_AI_API_KEY` | yes | OpenAI-compatible model credential |
| `XLH_AI_BASE_URL` | no | model API base URL |
| `XLH_AI_CHAT_MODEL` | no | chat/tool-calling model |
| `XLH_AI_EMBEDDING_MODEL` | no | embedding model |
| `XLH_PUBLIC_ORIGIN` | deployed browser app | same-origin mutation policy |
| `XLH_COOKIE_SECURE` | deployed HTTPS | must remain `true` outside local HTTP |
| `XLH_SEARCH_ENABLED` | no | register public Web Search when `true` |
| `SEARXNG_BASE_URL` | when Web Search is enabled | SearXNG endpoint |
| `XLH_RESEARCH_TIMEOUT` | no | total Research Agent deadline, default `30s` |
| `XLH_RESEARCH_TOOL_TIMEOUT` | no | per-tool deadline, default `10s` |
| `XLH_RESEARCH_MAX_ITERATIONS` | no | model iteration budget, default `6` |
| `XLH_RESEARCH_MAX_TOOL_CALLS` | no | tool call budget, default `8` |

Use the hosting provider's secret store. `.env.example` contains no working
credential and is only a local template.

## Migrations And Seed

Startup applies immutable `migrations/*.sql` under a PostgreSQL advisory lock
and records checksums. Never edit a migration already applied to a shared
database. Seed demo data explicitly, not at every startup:

```bash
XLH_SEED_ADMIN_PASSWORD='replace-with-a-strong-password' go run ./cmd/seed
```

The seed command updates the configured demo admin password, so do not run it
against an account whose ownership is unknown.

## Backup, Rollback, Smoke

Before migration or release, take a provider snapshot or a logical dump:

```bash
pg_dump --format=custom --file=xiaolanhe.dump "$XLH_DATABASE_URL"
```

Migrations 001-005 are additive. Roll back by redeploying the previous image;
leave compatible added tables in place and use a reviewed forward migration for
schema correction. Restore a dump only for actual data loss, after testing the
restore in an isolated database.

After deploy:

```bash
curl --fail https://<service>/healthz
curl --fail https://<service>/readyz
curl --fail https://<service>/api/games
curl --fail https://<service>/api/community/posts
curl --fail https://<service>/api/deals
```

Authenticated/admin product smoke and real model/Web smoke remain explicit
rollout actions because they use credentials and may create data or incur cost.
