# Test Plan

- Status: `IMPLEMENTED — LOCAL CI PASS; PRE_MERGE ENVIRONMENT BLOCKED`
- Authoritative spec: `./spec.md`

## Scope And Environments

PRE_MERGE uses Go unit/race tests, a pinned MySQL 8.4 container, the existing Redis
7.4 and RocketMQ 5.3.2 integration environments, frontend Vitest/build gates, mock
LightRAG HTTP servers and an isolated pinned official LightRAG 1.5.7 + PyMilvus 3.0.0 +
Milvus 2.6.11 + etcd 3.5.25 + MinIO lifecycle stack.

MySQL schema/concurrency results must come from real MySQL 8.4. LightRAG/Milvus
ingestion, rebuild, persistence and restore must exercise the official containers and
real storage paths. Mocks prove application contracts only. A skipped required test is
not a pass. Paid model/embedding calls require explicit cost authorization; until then
the corresponding live cases remain blocked rather than being replaced with mocks.

## Cases

| ID | Class | Layer | Scenario | Expected result | Command/evidence | Status |
|---|---|---|---|---|---|---|
| V1 | PRE_MERGE | baseline | freeze public REST/SSE, auth, money/time/error/idempotency, Agent/eval and flash-sale fixtures | storage migration causes no unexplained contract drift | existing + versioned golden tests | PASS — LOCAL |
| V2 | PRE_MERGE | config | valid/invalid MySQL DSNs, timeout, parseTime, UTC, multiStatements, clientFoundRows, TLS, SQL mode and server version | only bounded MySQL 8.4 configuration starts; production verifies TLS peer/hostname | config/unit + live startup tests | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V3 | PRE_MERGE | migration | fresh/repeat/concurrent execution | schema applies exactly once under same-connection lock | isolated MySQL 8.4 integration | ENVIRONMENT BLOCKED — NOT RUN |
| V4 | PRE_MERGE | migration failure | checksum mismatch, dirty version, process death after implicit DDL, `GET_LOCK` 1/0/NULL and `RELEASE_LOCK` 1/0/NULL/error | startup fails closed; inspect/repair is authorized and postcondition-bound; lock result is never guessed | fault-injection integration | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V5 | PRE_MERGE | schema | FKs, checks, JSON, UTC DATETIME(6), BINARY(32), auto increments and all indexes | exact relational invariants and round trips hold | information_schema + repository tests | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V6 | PRE_MERGE | collation | case/accent variants for usernames, slugs and codes | equality matches application ASCII normalization only | MySQL integration | ENVIRONMENT BLOCKED — NOT RUN |
| V7 | PRE_MERGE | partial uniqueness | open prices, active entitlements and nullable source/coupon/order/profile references | duplicate active/non-null values fail; valid history/NULL rows coexist | concurrent MySQL integration | ENVIRONMENT BLOCKED — NOT RUN |
| V8 | PRE_MERGE | account/chat | register/login/session ownership, conversation create/list/messages | unchanged behavior with MySQL IDs/times/errors | repository + HTTP tests | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V9 | PRE_MERGE | catalog/community | price selection, bounded IN, stable cursor, reaction aggregates and post/comment locks | correct portable queries, no cross-user/stale mutation | repository + HTTP + concurrency | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V10 | PRE_MERGE | Assistant memory | JSON get/replace/clear, unrelated field preservation, summary watermark CAS | missing/null semantics and monotonic summary preserved | repository/usecase/race | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V11 | PRE_MERGE | promotion | last coupon stock, per-user limit, idempotent replay and lock-after-expiry | no oversell; waited transaction sees committed/expired state | concurrent MySQL integration | ENVIRONMENT BLOCKED — NOT RUN |
| V12 | PRE_MERGE | order/payment | stale price/coupon, duplicate create/pay, same-order entitlement replay and different-order active-entitlement collision | replay requires paid order + matching source order/provider reference/payment idempotency; different order conflicts and rolls back | concurrent/fault MySQL integration | ENVIRONMENT BLOCKED — NOT RUN |
| V13 | PRE_MERGE | transaction retry | injected 1213, 1205, cancellation, ambiguous commit and unknown errors | old tx is rolled back/discarded; only side-effect-free idempotent closures retry; ambiguity reconciles in fresh tx | deterministic + live contention tests | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V14 | PRE_MERGE | flash-sale activation | concurrent scope-row upsert, overlapping scopes/windows, backfill and lock order | at most one conflicting active window; scope row is durable and locked before activity | MySQL concurrency | ENVIRONMENT BLOCKED — NOT RUN |
| V15 | PRE_MERGE | Redis config/Lua | enabled/disabled feature; authenticated rediss; redis with missing/false/true or malformed insecure override; window/version/stock/one-user-one-order/digest replay and release races | production config accepts only authenticated TLS; plaintext requires explicit local/CI opt-in; disabled feature ignores dependency config; atomic admission and at-most-once release remain intact | config unit + Redis integration + race | PARTIAL — REDIS LIVE ENVIRONMENT BLOCKED |
| V16 | PRE_MERGE | RocketMQ config/MySQL | enabled/disabled feature; complete, missing, partial and malformed ACL credentials with false/true/malformed insecure override; transactional publish, duplicate/out-of-order delivery, consumer crash and replay | production config requires a valid access/secret pair; only explicit local/CI opt-in permits both absent and never a partial pair; at-least-once transport produces one durable result | config unit + RocketMQ + MySQL integration | PARTIAL — LIVE ENVIRONMENT BLOCKED |
| V17 | PRE_MERGE | release workers | multi-worker SKIP LOCKED claim, lease expiry, retry and completion | each lease has one owner; no job loss/duplication | concurrent MySQL/Redis integration | ENVIRONMENT BLOCKED — NOT RUN |
| V18 | PRE_MERGE | reconciliation | Redis reserve/failure versus MySQL reservation/order/release state | drift is detected and repaired idempotently | failure-matrix integration | ENVIRONMENT BLOCKED — NOT RUN |
| V19 | PRE_MERGE | knowledge boundary | baseline/advanced/public search/admin with SQL capture and direct-Milvus fake | all knowledge calls go to LightRAG; zero SQL knowledge/direct Milvus/fallback calls | composition/architecture tests | PASS — LOCAL |
| V20 | PRE_MERGE | LightRAG config/client | runtime and importer HTTPS defaults; explicit HTTPS URL; HTTP with missing/false/true insecure override; malformed boolean; direct construction of repository and migration-verifier clients; authenticated health with expected stores plus Nano/wrong workspace/version/recovery/down responses | config loaders and client boundaries prevent API keys/query bodies from reaching HTTP unless local/controlled opt-in is explicit; health fields actually exposed by `/health` are validated without unsupported inference | config/command unit tests + both client `httptest` adversarial suites | PASS — LOCAL |
| V20A | PRE_MERGE | public knowledge capacity | initial allow, five-request burst, deterministic lazy refill, global sharing across spoofed forwarded addresses, full four-request concurrency, malformed input, cancellation and slot release | endpoint stays unauthenticated; excess work returns `429 capacity_exceeded` with integer `Retry-After >= 1`; rejected/canceled-before-admission/malformed requests never call LightRAG and malformed input does not consume capacity; limiter has no caller map or refill goroutine | `GOCACHE=/private/tmp/xlh-go-cache go test -race ./internal/knowledge/entry -run 'Test(SearchCapacity|KnowledgeSearchCapacity)' -count=1 -v`; package vet | PASS — LOCAL |
| V21 | PRE_MERGE | Compose/security | exact image pins, stores, private ports, secrets, one LightRAG replica, three Milvus dependencies, four data volumes, `milvus-init` bare server URI, bootstrap/runtime `/lightrag` URI, `MILVUS_DB_NAME=lightrag` on all three phases and environment-specific LightRAG/Redis/RocketMQ security settings | static gates require exactly one bare init URI and two `/lightrag` LightRAG URIs; wrong URI, missing database name and unsupported topology are rejected; local/CI insecure transports or ACL-free brokers are explicit and production examples remain fail closed | static scripts + Compose checker unit tests | PARTIAL — LOCAL CI PASS; DOCKER BLOCKED |
| V22 | PRE_MERGE | Milvus health | PyMilvus 3.0.0, etcd, MinIO, Milvus 9091/version/database/exact pinned final namespaces, initial runtime database context, exact grants and LightRAG authenticated health sequencing | first bootstrap/runtime client context is `lightrag`; no target collection or database-scoped runtime grant exists in `default`; the unchanged named-database and cluster-scope grant set passes while create-database/create-user remains denied; operator schema and Go health/fence gates pass | isolated container + schema/RBAC fixtures | PARTIAL — CURRENT URI STATIC/UNIT PASS; LIVE ENVIRONMENT BLOCKED |
| V23 | PRE_MERGE | live ingestion/query | ingest relationship corpus and query local/global/hybrid/mix | official LightRAG writes and returns managed chunk/entity/relation evidence from Milvus | lifecycle runner | ENVIRONMENT BLOCKED — NOT RUN |
| V24 | PRE_MERGE | persistence/restore | clean restart and empty-volume cold restore of all four components using the checksum-bound source-generation/config manifest | every archive and regular member size/hash validates before extraction; missing component, truncation, unsafe member and mixed generation fail; restored marker first becomes `stale/restore_pending_verification`; only forced `restore_verify` for a new generation can verify before service start | lifecycle backup/restore + manifest unit/negative fixtures | PARTIAL — LIFECYCLE NOT RUN |
| V25 | PRE_MERGE | vector rebuild | Nano workspace -> externally proven stopped/restart-disabled writers -> three official library rebuild functions -> Milvus | writer artifact binds digest/attempt/generation/operation/contract/expiry; strict stats reject skipped/duplicates by default; only a separate reviewed bounded duplicate policy may waive duplicates; unchanged source digests, exact three target ID sets and retrieval pass | guarded migration runner + evidence/policy fixtures | PARTIAL — LIVE/COST AUTHORIZATION BLOCKED |
| V26 | PRE_MERGE | rebuild failure | writer evidence missing/expired/tampered/wrong binding, illegal transition, abandoned rebuilding, wrong contract, source mutation, signal/interruption, malformed stats, duplicate-policy violation, finalizer failure and stale/missing fence | preflight refuses before mutation or atomically records a schema-complete failed/stale report; abandoned attempt gets its own immutable failed report; unchanged sources allow explicit rerun/rollback | fault-injection lifecycle + Python unit tests | PARTIAL — LIVE LIFECYCLE NOT RUN |
| V27 | PRE_MERGE | data cutover | real PostgreSQL source -> MySQL 8.4 dry run, independently signed source freeze and target writer fence, copy/resume (including a fresh same-generation target fence), cyclic references, malformed source, auto increment, repeat and source/target revalidation | checkpoints reject changed source identity/snapshot/schema/tool, embedded/live target migration provenance, target schema, generation, wrong key/key ID or second-phase state; missing/expired/tampered/wrong-identity fence, active application/background writers, enabled restart or enabled write traffic fail before every target mutation; final live revalidation fails before successful report/Completed publication; no duplicate/lost rows | real PostgreSQL -> MySQL 8.4 rehearsal with immutable artifacts | BLOCKED — NOT RUN |
| V28 | PRE_MERGE | authenticated read-only verification | separate trusted source and target key/key-ID verification of both manifest-embedded evidence records, target canonical payload/digests, expected deployment generation and referenced immutable reconciliation report; table counts, canonical row digests, orphan/uniqueness/status, coupon/stock/order/payment/entitlement/flash-sale totals; snapshot checkpoint directory and both databases before/after | wrong/missing/cross-used key, key ID or generation, tampered evidence/payload/digest/report and every data mismatch fail closed; verify accepts neither external attestation path, creates no lock/artifact and changes zero filesystem/database bytes | real PostgreSQL -> MySQL 8.4 verification rehearsal plus before/after filesystem and database evidence | BLOCKED — NOT RUN |
| V29 | PRE_MERGE | dependencies/static | normal binaries/import graph/config/docs contain no PostgreSQL/pgvector/Nano runtime assumption | pgx absent or isolated only to migration tool; architecture follows spec | static checks | PASS — LOCAL |
| V30 | PRE_MERGE | observability/privacy | DB retries/pool/migration/rebuild metrics and logs under errors | bounded labels; no SQL text, DSN, keys, content or user IDs leak | telemetry tests | PASS — LOCAL |
| V31 | PRE_MERGE | full regression | Go tests/race/vet/fmt, eval, frontend, hooks, architecture, MySQL/Redis/RocketMQ, two-stage Milvus URI checks and container build | all required gates exit zero without skip; focused tests alone are not complete-gate evidence | canonical Make/remote-CI evidence | PARTIAL — LOCAL CI PASS; REMOTE CI PENDING; LIVE/CONTAINER BLOCKED |
| V32 | PRE_MERGE | connection lifecycle | force pool growth/replacement beyond idle capacity and inspect each physical connection | every connection has TLS/UTC/strict mode; none bypass connector initialization | MySQL integration with connection IDs | ENVIRONMENT BLOCKED — NOT RUN |
| V33 | PRE_MERGE | affected-row semantics | insert/update/no-op/upsert paths with `clientFoundRows=false` | every `RowsAffected` branch matches actual changed-row semantics | repository + MySQL integration | PARTIAL — LIVE MYSQL BLOCKED |
| V34 | PRE_MERGE | utf8mb4 capacity | maximum-rune game descriptions, conversation messages/summaries, post/comment text and 4-byte code points | accepted values round trip; over-limit values fail without truncation/warning | schema/repository boundary tests | PARTIAL — LIVE MYSQL BLOCKED |
| V35 | PRE_MERGE | lock plan | EXPLAIN/EXPLAIN ANALYZE and live contention for price/coupon/order/activity/release queries | intended indexes are selected and locks remain within documented resource scope | MySQL plan + performance fixture | ENVIRONMENT BLOCKED — NOT RUN |
| V36 | PRE_MERGE | legacy knowledge manifest | freeze/snapshot, malformed rows, canonical envelope hashes, metadata disposition and old chunk accounting | immutable complete manifest or explicit approved exclusions; no silent field loss | synthetic PostgreSQL source | PASS — LOCAL |
| V37 | PRE_MERGE | legacy knowledge import | success/replay/409 mismatch/failure mid-batch/resume and more than 4,000 documents | contiguous success watermark never skips failure; unbounded verifier proves exact processed source-key set | PostgreSQL + official LightRAG lifecycle | PARTIAL — LIVE LIFECYCLE BLOCKED |
| V38 | PRE_MERGE | legacy knowledge retirement | source/target counts and content lengths, terminal states, pipeline state, new chunk totals and four query modes/citations | all retirement gates pass; legacy tables remain read-only and runtime has no SQL fallback | reconciliation + lifecycle report | ENVIRONMENT BLOCKED — NOT RUN |
| V39 | PRE_MERGE | rebuild fence | command-to-operation mapping, legal absent/stale/rebuilding/failed/verified transitions, abandoned attempt, atomic rename/fsync, canonical strict report schema, digest tamper, generation/contract mismatch, concurrent runner, canonical host roots, ancestor/leaf symlink, nesting/alias/device/hardlink attacks, full-tree validation before privileged mutation, stable lock inodes and one `flock` domain, exact UID/GID/mode metadata contracts, sealed final-publish and frozen recovery, pinned read-directory descriptors, shared-GID wiring and controller/deferred-LightRAG argv isolation | Python controller and Go readiness accept only the same complete verified report; `restore-verify` cannot be relabeled; locks and atomic state survive crashes; unsafe host trees cause zero privileged mutation; readiness cannot follow rebound parent directories or rely on world permissions; official LightRAG configuration cannot parse controller-only arguments | 190 Python metadata/static tests (64 preparer, 29 host validator, 86 controller, 11 guarded-start) + Go deployment/readiness contract tests; real Linux container UID 1000/65532 cross-UID lifecycle remains required and is not represented by the Python count | PASS — LOCAL STATIC; CROSS-UID LIFECYCLE BLOCKED |
| V40 | PRE_MERGE | fresh Milvus bootstrap | ordered root `milvus-init` -> runtime `lightrag-bootstrap` -> steady-state `lightrag` against empty and nonempty sources/targets | init alone uses the bare URI/root; both LightRAG phases start in `lightrag` without root or `default` privileges or a selection race; only independently empty valid schemas can verify without embedding and nonempty sources require full rebuild | isolated lifecycle | ENVIRONMENT BLOCKED — NOT RUN |
| V44 | PRE_MERGE | runtime readiness/startup | one slow/erroring MySQL, Redis, RocketMQ or LightRAG check while peers complete; RocketMQ preflight failure/timeout; fast or blocked synchronous SDK `Start` | checks run concurrently with independent deadlines, the whole response is bounded, no private error leaks, and no check inherits another dependency's spent budget; the SDK adapter admits at most one residual readiness request, failed preflight never starts a client, fast Start cancels its watchdog, and blocked Start causes process exit within the hard deadline without an orphan client | deterministic HTTP/adapter/startup/subprocess tests + race | PASS — LOCAL |
| V45 | PRE_MERGE | deployment smoke | Go public knowledge search, admin read/write authorization, baseline chat-to-LightRAG and enabled flash sale over real Redis/RocketMQ | wiring failures fail the smoke; missing real middleware is NOT RUN rather than PASS | container smoke + integration logs | ENVIRONMENT BLOCKED — NOT RUN |
| V41 | ROLLOUT | production restore | provider MySQL backup and complete knowledge stack backup restore | measured RPO/RTO and verified application reads | approved production-like target | TODO |
| V42 | ROLLOUT | relational cutover | write freeze, drain, copy, verify, switch, smoke and observation | no invariant mismatch; rollback decision remains available | operator checklist/metrics | TODO |
| V43 | ROLLOUT | vector cutover | authorized provider-backed rebuild and post-enable observation | quality/latency/counts/cost captured; no stale Nano vectors | operator/rebuild/eval report | TODO |

## Acceptance Traceability

| Acceptance criterion | Primary cases |
|---|---|
| AC1 | V2, V8-V10, V19, V29, V32 |
| AC2 | V2, V5-V7, V33-V35 |
| AC3 | V3, V4 |
| AC4 | V1, V8-V12 |
| AC5 | V7, V11-V14, V17, V33, V35 |
| AC6 | V15-V18, V44, V45 |
| AC7 | V19, V20, V29, V36-V38, V44, V45 |
| AC8 | V20-V23, V39, V40 |
| AC9 | V23, V24, V39, V40 |
| AC10 | V25, V26, V39, V40 |
| AC11 | V27, V28, V36-V38 |
| AC12 | V2, V15, V16, V20-V22, V24, V30, V32, V39, V44, V45 |
| AC13 | V1, V15-V20A, V30, V31, V38, V44, V45 |
| AC14 | V1-V40, V44-V45 |

## Required Concurrency Assertions

- Total successful coupon claims never exceeds stock or per-user limit.
- Total successful Redis admissions never exceeds activity stock and one user receives at
  most one request outcome per activity.
- Duplicate RocketMQ deliveries produce one durable reservation/order outcome.
- The same order cannot be paid twice and one user/edition has at most one active
  entitlement; an active-entitlement collision from a different order is never treated
  as replay.
- An operation that waits for a lock observes newly committed expiry/cancellation/price
  state under explicit Read Committed.
- Concurrent flash-sale activation for the same scope cannot create overlapping active
  windows; concurrent scope creation converges on one durable scope-lock row.
- Release-job workers never lease the same row concurrently and expired leases recover.
- Transaction retries roll back and discard the failed transaction, do not duplicate
  side effects and do not continue after cancellation.
- Public knowledge searches never exceed the configured in-process execution cap; a
  canceled admitted request releases its slot, and rate/concurrency rejections perform
  zero downstream provider calls even when callers rotate forwarded-address headers.

## LightRAG/Milvus Lifecycle Assertions

The isolated runner must prove:

1. official pinned component versions, including PyMilvus 3.0.0, and authenticated
   storage configuration;
2. init with the bare server URI, bootstrap/runtime with `/lightrag` plus
   `MILVUS_DB_NAME=lightrag`, first runtime context in `lightrag`, no LightRAG target
   collections or database-scoped runtime grants in `default`, and the unchanged exact
   runtime grant set;
3. exact database `lightrag`, exact pinned final namespaces and exact official
   dynamic-field/field/type/length/nullability/primary schema for entity, relationship
   and chunk collections with dimension 1024, AUTOINDEX and COSINE search;
4. create -> terminal status -> all supported query modes -> exact delete;
5. clean LightRAG/Milvus stack restart;
6. complete per-file/per-archive checksum backup and new-generation restore into empty
   volumes, including missing/truncated/mixed-generation rejection;
7. NanoVectorDB source backup, external attempt-bound stopped-writer evidence, all three official rebuild
   library functions, structured stats, source digests and exact target ID sets;
8. all five fence states, generation/contract/report integrity and fresh-empty bootstrap;
9. rejection of partial backup, restored verified marker, wrong workspace/embedding
   contract/dimension, expired/tampered evidence, unreviewed duplicates and interrupted rebuild;
10. legacy manifest/import/reconciliation when source rows exist.

## Agent Cases

The Multi-Agent behavior itself is unchanged, but deterministic eval metadata must
record `MilvusVectorDBStorage`, embedding model/dimension and component versions. Router,
Planner, Research/Planning/Game Copilot budgets, cancellation, citations, source
filtering, partial/all-source failure and permanent read-only tools rerun unchanged.
LightRAG/Milvus unavailability must be explicit and must never unlock a SQL fallback or
transactional Agent tool.

## Not Applicable

- Browser/UI redesign is not applicable; existing frontend tests remain regression
  gates because public contracts are unchanged.
- Direct Milvus application tests are intentionally absent: only official LightRAG may
  own/query its vector schema. Milvus is verified through infrastructure health,
  collection inspection in the operator runner and LightRAG black-box behavior.
- Multi-replica LightRAG and Milvus distributed success tests are excluded because this
  release deliberately targets single-replica LightRAG plus standalone Milvus. Static
  checks must reject claims/configuration that imply otherwise.
- Per-client fairness and distributed public-search quotas are excluded: this release
  uses one constant-memory process-local budget and has no trusted-proxy policy.
- Automatic destructive retirement of PostgreSQL/Nano data is not tested because it is
  outside scope; rollback backups are retained through the observation window.

## Exit Criteria

V1-V40 and V44-V45 pass without required skips; every acceptance criterion maps to executed evidence;
normal runtime and knowledge paths contain no PostgreSQL/pgvector/Nano fallback; MySQL
and flash-sale concurrency invariants hold; official LightRAG/Milvus ingestion, rebuild,
restart and full restore are proven; and no critical/high correctness, data-loss,
security, privacy or provenance defect remains. V41-V43 remain rollout-only until the
user separately authorizes infrastructure, credentials, cost and live mutations.
V27 and V28 require a real PostgreSQL source and MySQL 8.4 target; until that rehearsal is
captured they remain `BLOCKED — not run`, and overall PRE_MERGE readiness is `BLOCKED`
rather than inferred from unit, mock, or documentation evidence. The current two-stage
Milvus URI fix passed the complete local `make ci` gate; clean Linux remote CI, real
container and production checks remain pending or `ENVIRONMENT BLOCKED` as identified
above.
