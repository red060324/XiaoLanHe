# Public Deployment

XiaoLanHe always needs one Go container plus one business PostgreSQL database with
the `vector` extension. The React build is embedded in the same image, so browser
pages and `/api/` use one origin. SearXNG is optional. Redis and RocketMQ are
required only when the opt-in flash-sale feature is enabled. Official LightRAG and
its persistent volume are required only when advanced AI is enabled.

These dependencies do not all need to be purchased as virtual machines. A real
deployment can combine a container/PaaS for Go, managed PostgreSQL, managed Redis,
a managed or self-hosted RocketMQ-compatible service, and a persistent-volume host
for LightRAG. What matters is private connectivity, durable state, backups,
monitoring and tested recovery—not server ownership.

## Render Blueprint

The smallest hosted demo uses the repository's `render.yaml`:

1. Fork or push the repository to GitHub.
2. In Render, create a Blueprint from the repository.
3. Store `XLH_AI_API_KEY` in Render's secret configuration.
4. Set `XLH_PUBLIC_ORIGIN` to the final service URL if the service name changes.
5. Open `https://<service-name>.onrender.com` and verify `/healthz` and
   `/readyz`.

Render supplies the `onrender.com` subdomain. Buying a custom domain is
optional. A custom domain only changes DNS/TLS configuration and
`XLH_PUBLIC_ORIGIN`; it does not require a code change.

The checked-in Blueprint deliberately keeps `XLH_FLASH_SALE_ENABLED=false` and
`XLH_ADVANCED_AI_ENABLED=false`, and selects Render's free plans, so it cannot
silently create a paid service. This is a demo configuration, not a production
configuration: the free Web Service sleeps after inactivity, and the free
PostgreSQL database is limited to 1 GB, expires after 30 days, and has no
managed backups or connection pooling. For a persistent deployment, select a
paid Render database or another durable PostgreSQL provider that supports
`CREATE EXTENSION vector`, and configure tested backups before accepting user
data. The Blueprint does not provision Redis, RocketMQ or LightRAG and therefore is
not the complete production topology. See Render's public
[free-plan limits](https://render.com/docs/free) and
[supported PostgreSQL extensions](https://render.com/docs/postgresql-extensions).

The Blueprint waits for repository checks to pass before automatically
deploying and uses `/readyz` as the routing health check, so a process without a
usable database is not treated as ready.

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
Do not expose PostgreSQL publicly. The same rule applies to Redis, RocketMQ
NameServer, and RocketMQ Broker.

## Local Middleware Stack

The checked-in Compose file starts PostgreSQL/pgvector, password-protected Redis
7.4, and a persistent RocketMQ 5.3.2 NameServer/Broker pair for local integration:

```bash
cp .env.example .env
# Set matching local values for XLH_POSTGRES_PASSWORD, XLH_DATABASE_URL,
# XLH_REDIS_PASSWORD, and XLH_REDIS_URL. Do not reuse these in production.
make middleware-config
make middleware-up
```

The Go process runs on the host and connects to the loopback addresses from
`.env`. The broker deliberately advertises `127.0.0.1` for that topology. Do not
reuse `deploy/rocketmq/broker.conf` for an app running in another container or
host; set `brokerIP1` to a stable private address resolvable by every client.
Named volumes retain database, Redis AOF, RocketMQ logs, and broker state across
ordinary restarts. `make middleware-down` stops containers without deleting the
volumes. Removing volumes is a destructive reset and is not part of that target.

## Official LightRAG Native-Store Stack

`deploy/docker-compose.lightrag.yml` pins official LightRAG `1.5.7`, API `0344` and
an immutable image digest. It uses exactly `JsonKVStorage`,
`NanoVectorDBStorage`, `NetworkXStorage` and `JsonDocStatusStorage`. There is no
LightRAG PostgreSQL, Redis, Neo4j, Milvus, Qdrant, MongoDB or search cluster.

All workspace files live under `/app/data/rag_storage` on the
`xlh-lightrag-data` volume. The local service binds only `127.0.0.1:9621`; a
containerized Go app should instead use private service DNS and must not publish
LightRAG publicly. Outbound access remains necessary for its LLM and embedding APIs.

```bash
export XLH_LIGHTRAG_API_KEY='a-distinct-private-key-at-least-32-chars'
export XLH_LIGHTRAG_LLM_API_KEY='...'
export XLH_LIGHTRAG_EMBEDDING_API_KEY='...'
make lightrag-static
make lightrag-config
make lightrag-up
```

The supported topology is one LightRAG service replica with `WORKERS=2` and one
read-write volume. Do not mount that workspace from a second service replica. Native
storage keeps its working set in process and is suitable only for a measured
small-corpus deployment; use a separately reviewed storage migration when corpus,
memory, HA or horizontal-scaling requirements exceed that envelope.
The manifest deliberately leaves `MAX_REQUEST_BODY_BYTES` unset: official 1.5.7 then
keeps its 1 MiB ordinary-route limit and separate 50 MiB text-ingestion tier. The Go
facade independently caps a knowledge request at 1 MiB and its normalized content at
1 MiB.

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
| `XLH_RESEARCH_TIMEOUT` | no | total Research Agent deadline, default `25s` |
| `XLH_RESEARCH_TOOL_TIMEOUT` | no | per-tool deadline, default `10s` |
| `XLH_RESEARCH_MAX_ITERATIONS` | no | model iteration budget, default `6` |
| `XLH_RESEARCH_MAX_TOOL_CALLS` | no | tool call budget, default `8` |
| `XLH_ADVANCED_AI_ENABLED` | no | require the three-Agent + official LightRAG path; default `false` |
| `XLH_METRICS_TOKEN` | when advanced AI or flash sale enabled | distinct 32-512 character operator bearer token for `GET /metrics`; no CR/LF |
| `XLH_LIGHTRAG_BASE_URL` | when advanced AI enabled | private official LightRAG API URL |
| `XLH_LIGHTRAG_API_KEY` | when advanced AI enabled | distinct 32-512 character server-to-server LightRAG API key; no CR/LF |
| `XLH_LIGHTRAG_WORKSPACE` | no | pinned workspace, default `xiaolanhe_v1` |
| `XLH_LIGHTRAG_WORKING_DIR` | no | expected directory, default `/app/data/rag_storage` |
| `XLH_LIGHTRAG_LLM_*` | LightRAG container | LLM binding endpoint, key and model |
| `XLH_LIGHTRAG_EMBEDDING_*` | LightRAG container | embedding endpoint, key, model and dimension |
| `XLH_ASSISTANT_TOTAL_TIMEOUT` | no | shared Agent deadline, default `45s` |
| `XLH_ASSISTANT_MAX_MODEL_CALLS` / `XLH_ASSISTANT_MAX_TOOL_CALLS` | no | shared budgets, defaults `12` / `12` |
| `XLH_ASSISTANT_MAX_DELEGATIONS` | no | shared delegation cap, default `3` |
| `XLH_PLANNING_TIMEOUT` | no | Planning Agent local deadline, default `15s` |
| `XLH_SUMMARY_*` | no | summary timeout, threshold, cap and prompt version |
| `XLH_FLASH_SALE_ENABLED` | no | opt in to Redis + RocketMQ flash sale; default `false` |
| `XLH_REDIS_URL` | when flash sale enabled | `redis://` or `rediss://` URL with an explicit non-empty password; use TLS/private networking in production |
| `XLH_REDIS_KEY_PREFIX` | no | deployment-specific Redis namespace |
| `XLH_ROCKETMQ_NAMESERVERS` | when flash sale enabled | comma-separated private NameServer addresses |
| `XLH_ROCKETMQ_ACCESS_KEY` / `XLH_ROCKETMQ_SECRET_KEY` | provider-dependent | RocketMQ ACL credentials; configure together |
| `XLH_ROCKETMQ_TOPIC` | no | versioned reservation topic |
| `XLH_ROCKETMQ_PRODUCER_GROUP` / `XLH_ROCKETMQ_CONSUMER_GROUP` | no | stable deployment-scoped groups |

Use the hosting provider's secret store. `.env.example` contains no working
credential and is only a local template.

## Metrics And Host Monitoring

When `XLH_METRICS_TOKEN` is configured, scrape the private application endpoint with
a dedicated operator credential:

```bash
curl --fail \
  -H "Authorization: Bearer $XLH_METRICS_TOKEN" \
  https://<private-service>/metrics
```

The route is not registered when the token is empty. Advanced mode and flash-sale
mode each reject startup unless the token is present and valid. Do not reuse the user session secret,
LightRAG key or model key. Keep `/metrics` on a private network or protect it with an
operator-only gateway in addition to the bearer token.

The application exposes bounded Prometheus metrics for Agent operations/budgets,
model request results, provider-reported token usage, LightRAG requests/queries/
storage contract/pipeline/recovery/managed document status and memory-summary work.
It also exposes bounded flash-sale admission, transaction/check, consume, final-guard,
recovery, expiry and release outcomes plus processed-item and pending-age histograms.
`usage_reported=false` means the model provider omitted usage metadata; no token
estimate is substituted. Run/session/user/source keys and content are not metric
labels. This release does not emit OpenTelemetry spans. Configure the host or
orchestrator separately for LightRAG volume bytes/free space, process RSS, container
memory, restarts and filesystem errors; those values cannot be proven by the
LightRAG HTTP API and are intentionally not fabricated by XiaoLanHe.

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

Before migration or release, take a provider snapshot or a logical dump. Free
Render PostgreSQL has no managed backups, so export a logical dump yourself or
upgrade before storing data you cannot recreate:

```bash
pg_dump --format=custom --file=xiaolanhe.dump "$XLH_DATABASE_URL"
```

Migrations 001-007 are additive. Migration 007 contains only conversation summary
and profile changes; it contains no knowledge or LightRAG tables.

### LightRAG import, backup and restore

Run the legacy importer without `--execute` first. The report contains stable legacy
IDs/source keys and the last scanned ID; only an explicit execute run writes:

```bash
go run ./cmd/import-knowledge --limit 20
go run ./cmd/import-knowledge --execute --limit 20 --after-id 0
```

Each execute run handles at most 100 records, waits for terminal track status and
reports partial failures without blind write retry. Re-run from the reported
`lastId`; 409 reconciliation uses the deterministic source key. No LightRAG ID is
written back into PostgreSQL.

A consistent native-store backup must include the complete `WORKING_DIR`, never
selected files:

1. Disable knowledge mutations at the public facade and wait until
   `/documents/pipeline_status` is idle with no recovery required.
2. Stop the one LightRAG service cleanly.
3. Snapshot/archive the complete `xlh-lightrag-data` volume plus the pinned image,
   workspace, model and embedding configuration.
4. Restart the service, or restore into an empty volume using the same pinned
   configuration.
5. Verify authenticated `/health`, managed document counts and fixed local/global/
   hybrid/mix retrieval cases before accepting writes.

Never restore into a non-empty workspace and never change embedding dimension/prefix
semantics in place; use a new workspace and re-index. Volume encryption, access and
retention are deployment responsibilities.

The repository provides an isolated destructive lifecycle gate that performs the
same procedure, including missing/wrong-key checks, one real ingestion, all four
retrieval modes, clean restart, whole-volume archive/empty-volume restore and exact
delete. It creates a unique Compose project and refuses to reuse an existing one:

```bash
export XLH_LIGHTRAG_API_KEY='...'
export XLH_LIGHTRAG_LLM_API_KEY='...'
export XLH_LIGHTRAG_EMBEDDING_API_KEY='...'
export XLH_LIGHTRAG_LIFECYCLE_ACK=isolated-destructive-test
make lightrag-lifecycle
```

This invokes real provider extraction/query calls and may incur cost. Run it only on
a disposable Docker host with port 9621 free; the script removes only its uniquely
labelled Compose resources and volume. Save the command output as PRE_MERGE evidence.

### Rollback

For an Assistant/LightRAG rollback, stop knowledge mutations, set
`XLH_ADVANCED_AI_ENABLED=false`, redeploy, and retain the LightRAG volume for
diagnosis. Since there was no dual write or continuous synchronizer, no reverse sync
exists. The disabled mode can use the legacy local knowledge baseline, but it must not
be labelled as LightRAG.

For a flash-sale rollback, set `XLH_FLASH_SALE_ENABLED=false`, stop new intake,
inspect/drain accepted pending work, and redeploy the previous image;
leave compatible added tables in place and use a reviewed forward migration for
schema correction. Do not delete Redis markers or the RocketMQ topic during an
application rollback: accepted requests may still need reconciliation. Restore a
database, Redis AOF/snapshot, or broker store only for actual data loss, after
testing the restore in an isolated environment.

Production flash-sale operation additionally requires private persistent Redis
with an explicit URL password, TLS where supported, replication and tested backup/restore;
and a persistent RocketMQ cluster with ACL, monitored consumer lag/retries/DLQ,
and tested broker recovery. A single local Compose broker is integration tooling,
not a high-availability production topology. When the feature is enabled, `/readyz`
requires Redis PING and an authenticated RocketMQ topic-route lookup with at least
one publishable queue. This catches a missing or inaccessible topic rather than
merely an open NameServer TCP port; it does not replace broker lag, DLQ, durability
or end-to-end publish monitoring. When advanced AI is enabled, startup
and `/readyz` also require authenticated LightRAG health with the exact version,
workspace, directory and native stores; there is no silent PostgreSQL knowledge
fallback.

After deploy:

```bash
curl --fail https://<service>/healthz
curl --fail https://<service>/readyz
curl --fail https://<service>/api/games
curl --fail https://<service>/api/community/posts
curl --fail https://<service>/api/deals
```

For an isolated environment where creating demo data is acceptable, run the
full authenticated product smoke after seeding:

```bash
XLH_SMOKE_BASE_URL=https://<service> \
XLH_SMOKE_ADMIN_PASSWORD='<seeded-admin-password>' \
bash scripts/smoke-product.sh
```

The script creates a temporary user, community content, one coupon claim, one
sandbox-paid order, and an entitlement. Omit `XLH_SMOKE_ADMIN_PASSWORD` to skip
the admin catalog-write case. Do not run it against an environment where this
demo data is unwanted.

Hosted-environment authenticated/admin smoke and real model/Web smoke remain
explicit rollout actions because they use credentials, create data, or may
incur cost. The repository CI runs the same product script only against its
disposable isolated database.

Run `make eval` before rollout. It is a deterministic fixture comparison, not a claim
about real-provider quality. Live LightRAG ingestion/retrieval, clean restart,
backup/restore and real-model evaluation must be recorded separately in the rollout
report.
