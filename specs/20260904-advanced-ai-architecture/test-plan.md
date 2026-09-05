# Test Plan

- Status: `LOCAL GATES PASS — EXTERNAL GATES BLOCKED`
- Authoritative spec: `./spec.md`

## Scope And Environments

PRE_MERGE uses deterministic fake models/tools, `httptest` LightRAG servers,
race-enabled Go tests, frontend Vitest, isolated XiaoLanHe PostgreSQL/pgvector for
business data, and one live pinned official LightRAG container using its native four
stores on a temporary persistent volume. Live cases must ingest, persist and retrieve
real graph/vector data; mocks cannot replace them. `SKIP` is not PASS.

Paid model/embedding/Web calls remain rollout-only unless credential and expense
authorization is explicit. A deterministic/local-compatible provider may support the
live container only if the official indexing/storage/query path remains unchanged.

## Cases

| ID | Class | Layer | Scenario | Expected result | Evidence | Status |
|---|---|---|---|---|---|---|
| V1 | PRE_MERGE | baseline | current route/query/tool/memory/citation and legacy-knowledge fixtures | immutable versioned baseline | strict JSONL dataset and eval golden | PASS |
| V2 | PRE_MERGE | contracts | Router, QueryPlan and worker task/result parsing | unknown field/version/run/sequence/Skill/tool/evidence/size fails closed | entity/parser tests | PASS |
| V3 | PRE_MERGE | Skill | four valid definitions plus bad version/write tool/mode/cycle/limit | immutable valid registry; invalid startup rejected | Skill/config tests | PASS |
| V4 | PRE_MERGE | Router/Planner | routes/Skills/query counts/modes/filters/malformed/failure | one Skill and valid plan or bounded fallback; no CoT | fake Node tests | PASS |
| V5 | PRE_MERGE | budget | nested concurrent model/tool/delegation/time/output consumption | aggregate cap never exceeded and no race | budget tests + full race gate | PASS |
| V6 | PRE_MERGE | query adapter | `/auth/verify`, authenticated health/pipeline and `/query/data` body/key/modes plus missing fields, wrong topology, recovery, 401/403/422/429/5xx/timeout/redirect/oversize/malformed | fixed private endpoint, explicit required fields and strict mapped result/error; key absent from output/log | `httptest` adversarial suite | PASS |
| V7 | PRE_MERGE | evidence | entities/relations/chunks/references, unsafe/foreign source, URL, missing reference and caps | only managed-source bounded evidence reaches run store/citations | adapter/UseCase tests | PASS |
| V8 | PRE_MERGE | Research Agent | refinement, allowed/forbidden tools, LightRAG partial/down/no-result and limits | typed artifact, explicit unavailable, zero hidden local fallback/unauthorized calls | fake tools + race | PASS |
| V9 | PRE_MERGE | Planning Agent | recommendations/team plan, preferences, ownership exclusion and stale facts | revalidated evidence-linked artifact or explicit degradation; no mutation | fake tools/module tests | PASS |
| V10 | PRE_MERGE | Game Copilot | Research-only, Planning-only, combined/follow-up, malformed/stale result, recursion and caps | acyclic minimum-context dispatch and correct stop | deterministic supervisor tests | PASS |
| V11 | PRE_MERGE | context privacy | canaries in unrelated history/profile/provider payload | workers/LightRAG/logs receive no forbidden fields | captured-input and telemetry tests | PASS |
| V12 | PRE_MERGE | migration | fresh/repeat/concurrent/checksum/001-006 upgrade and duplicate-profile preflight | summary/profile-only 007 applies once; no knowledge/LightRAG table | isolated PostgreSQL 17 + pgvector integration | PASS |
| V13 | PRE_MERGE | knowledge adapter | public search plus create/track/list/delete mappings, canonical source hash, caps, auth, ambiguous write, 409 reconciliation, busy and disallowed endpoints | provider-neutral evidence, exact allowlist, idempotent async string IDs and no fabricated ID/blind retry | `httptest` suite | PASS |
| V14 | PRE_MERGE | knowledge ownership | advanced Assistant/public search/create/delete with SQL capture and legacy-store fake | zero PostgreSQL knowledge read/write and zero dual-write in advanced mode | package boundary, composition and adapter tests | PASS |
| V15 | PRE_MERGE | legacy import | dry-run, selected rows, deterministic source, restart/409 reconciliation and failure report | bounded one-time import; no ID writeback or continuous sync | fake adapter tests PASS; live official import unavailable | PARTIAL |
| V16 | PRE_MERGE | official version | image tag/digest/core/API mismatch, invalid auth body and valid pinned service | `latest` and mismatches rejected; expected release/API and `status=ok` accepted | digest/static checks and mock authenticated health PASS; official runtime unavailable | PARTIAL |
| V17 | PRE_MERGE | native storage | exact JsonKV/NanoVectorDB/NetworkX/JsonDocStatus, workspace/directory, Gunicorn/two-worker/one-replica topology and no external DB env | configured four stores on one volume; external backend absent; invalid runtime attestation fails readiness | manifest/static and adversarial readiness checks PASS; live inspection unavailable | PARTIAL |
| V18 | PRE_MERGE | live ingestion | create, track terminal document ID, duplicate source, list, exact async delete | official pipeline builds/lists/deletes records | isolated lifecycle runner implemented; needs Docker and working test LLM/embedding providers to execute | BLOCKED |
| V19 | PRE_MERGE | live retrieval | local/global/hybrid/mix over relationship corpus | structured graph/vector evidence and managed provenance | isolated lifecycle runner implements all four modes; needs Docker and working test providers to execute | BLOCKED |
| V20 | PRE_MERGE | persistence/security | clean restart, whole-volume archive/empty-volume restore, missing/wrong key and public-health disclosure | data survives restart/restore; unauthorized access blocked; public health exposes liveness only | static/live checks and isolated lifecycle runner implemented; local Docker unavailable | BLOCKED |
| V21 | PRE_MERGE | native-store limits | second replica, read-write shared volume, missing/read-only/full volume, memory/corpus limit and route-tiered body limits | invalid topology/config fails; ingestion allowance is preserved; host capacity is observed and bounded | replica/external-store/global-body-override statically rejected; host fault/resource tests unavailable | PARTIAL |
| V22 | PRE_MERGE | summary | threshold, prior summary, cap, failure, concurrent late result | recent eight + monotonic summary, no backward watermark | UseCase/race and live PostgreSQL CAS | PASS |
| V23 | PRE_MERGE | profile | absent/get/replace/clear, preservation, invalid/cross-user/guest/origin | owner-only profile and UI; Agent has no write port | unit/HTTP/PostgreSQL/Vitest | PASS |
| V24 | PRE_MERGE | HTTP/SSE/UI | direct/research/planning, async knowledge UI, citations, no-result, disconnect/account switch/a11y and initial-entry budget | compatible chat plus intentional knowledge-contract migration; heavy renderer is lazy and entry stays at or below 500 KiB | Hertz/socket/Vitest/build budget | PASS |
| V25 | PRE_MERGE | safety/privacy | injection, attempted mutation, forged identity/budget/endpoint and captured telemetry | content remains data; writes unavailable; secrets/content absent | adversarial/static/log/metric tests | PASS |
| V26 | PRE_MERGE | observability/eval | bounded Agent/model/LightRAG/memory metrics, redaction and baseline comparison | protected low-cardinality metrics and all deterministic thresholds pass; host volume/process metrics remain external | registry/model/HTTP/telemetry tests and eval report | PASS |
| V27 | PRE_MERGE | lifecycle/CI | disabled/enabled-invalid/down/readiness/shutdown/rollback and repository regressions | baseline compatible; enabled fails closed on auth/version/store/topology/recovery mismatch; no required skip | full local CI and PostgreSQL integration PASS; official lifecycle and clean-checkout Actions remain unavailable | PARTIAL |
| V28 | ROLLOUT | isolated deployment | private one-replica LightRAG volume, migration, import, restore and rollback | survives clean restart/restore within declared small-corpus envelope | no approved target/runtime | BLOCKED |
| V29 | ROLLOUT | real provider eval | pinned LightRAG/model/embedding/Skills/dataset flow | quality/P50/P95/calls/tokens/cost/failures recorded | no credential/cost authorization | BLOCKED |
| V30 | ROLLOUT | observation | Web enabled/disabled/down plus memory/corpus/volume/pipeline/failure/privacy | alerts and rollback decision with no HA/scale claim | requires deployed cohort and orchestrator telemetry | BLOCKED |

## Deterministic Eval Threshold Gate

- route and Skill accuracy `>= 0.90`;
- required-facet coverage `>= 0.85`; fixture Recall@8 `>= 0.80`;
- citation coverage and explicit memory/profile consistency `= 1.00`;
- unauthorized tool/delegation/LightRAG endpoint calls `= 0`;
- foreign-source references, hidden local fallback and unsupported fixture facts `= 0`;
- each quality metric regresses baseline by at most `0.02`;
- model/tool/delegation counts and P50/P95 deterministic latency are reported.

Real-provider quality remains rollout evidence and uses the same case IDs without
retry-until-pass sampling.

## Official Lifecycle Runner

`make lightrag-lifecycle` is intentionally fail-safe by default. It runs only with
`XLH_LIGHTRAG_LIFECYCLE_ACK=isolated-destructive-test` and explicit test
LLM/embedding provider names and keys. The script fixes the endpoint to loopback,
uses the repository Compose file and a unique project name, refuses pre-existing
same-name resources, requires an empty workspace, and removes only resources it
created. It validates authentication, real ingestion, duplicate `409`, all four
query modes, clean restart, complete volume archive, restore into a new empty volume,
post-restore retrieval and exact deletion. Presence and shell/static validation of
the runner are PASS; V18-V20 stay BLOCKED until its real official-container run is
captured.

## Not Applicable

- Payment/order/coupon/flash-sale/community mutation tests are unchanged because no
  Agent can call them; their regressions remain in V27.
- PostgreSQL/Redis/Neo4j/Milvus/Qdrant/MongoDB/OpenSearch tests do not apply to
  LightRAG storage. XiaoLanHe's PostgreSQL tests still apply to business memory/profile.
- Upstream Python unit tests are not copied; the pinned image is verified through
  API, storage, persistence and black-box retrieval behavior.
- Multi-replica/high-availability success tests do not apply. V21 must instead reject
  unsupported topology and make the limitation observable.

## Exit Criteria

V1-V27 must pass without required skips; every AC maps to code/evidence; official LightRAG
and all four native stores are exercised; no advanced-mode SQL knowledge operation or
projection table exists; restart/restore/security/rollback are proven; and no
critical/high read-only, privacy, ownership, injection or provenance defect remains.
V28-V30 require separate environment, credential, network and cost authorization.
