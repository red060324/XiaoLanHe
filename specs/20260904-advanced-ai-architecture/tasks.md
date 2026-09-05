# Tasks

- Status: `IMPLEMENTED — ENVIRONMENT GATES BLOCKED`
- Authoritative spec: `./spec.md`

| ID | Class | Status | Task | Acceptance criteria | Evidence |
|---|---|---|---|---|---|
| T0 | PRE_MERGE | DONE | Human approves official LightRAG, native four-store single-instance boundary, direct async knowledge contract, three-Agent classification, Skills, budgets, thresholds and rollout | all | user approval on 2026-09-04 |
| T1 | PRE_MERGE | DONE | Freeze current Assistant and legacy-knowledge baseline fixtures and implement versioned eval schema/reporting before behavior changes | AC13-AC15 | versioned JSONL, strict dataset tests and deterministic report |
| T2 | PRE_MERGE | DONE | Introduce module-first `assistant` and `knowledge` Go entities/ports with compatibility-preserving chat composition | AC14 | architecture hook plus REST/SSE/full regression tests |
| T3 | PRE_MERGE | DONE | Add migration 007 and PostgreSQL repositories for summary watermark and Assistant profile only; assert no new LightRAG/knowledge tables | AC8, AC9, AC15 | isolated PostgreSQL 17 + pgvector fresh/repeat/concurrent/upgrade/profile/summary tests PASS |
| T4 | PRE_MERGE | DONE | Add strict advanced/LightRAG configuration, prompt versions and embedded Skill registry with delegate/tool/mode/budget/output validation | AC3, AC10-AC12, AC15 | config/startup/Skill tests PASS |
| T5 | PRE_MERGE | DONE | Implement typed Router and Query Planner Nodes, strict schemas, safe fallback and request-wide concurrent budget ledger | AC2-AC4, AC10 | entity/Node/budget and race tests PASS |
| T6 | PRE_MERGE | DONE | Implement strict LightRAG adapter for authenticated `status=ok`, health/topology/pipeline readiness and `/query/data`, fixed routes, caps, deadlines, response validation and managed-source filtering | AC5, AC10-AC12 | comprehensive `httptest` contract/adversarial suite, including missing fields, wrong mode/workers and recovery fence, PASS |
| T7 | PRE_MERGE | DONE | Migrate Research Agent to minimum briefs, Skill allowlists and typed artifacts; integrate LightRAG/catalog/forum/optional Web with explicit partial/unavailable behavior | AC1-AC5, AC10, AC11 | deterministic Agent/tool/failure/cancellation/race tests PASS |
| T8 | PRE_MERGE | DONE | Implement Planning Agent with read-only catalog, entitlement, constraint and run-local evidence tools plus post-parse revalidation | AC1-AC3, AC10, AC11 | planning, stale fact, ownership and forbidden-tool tests PASS |
| T9 | PRE_MERGE | DONE | Implement Game Copilot supervisor with typed delegation, run/sequence validation, acyclic dispatch and aggregate budgets | AC1-AC4, AC10 | research/planning/foreign-evidence/budget/cancellation tests PASS |
| T10 | PRE_MERGE | DONE | Replace advanced knowledge search/writes with direct LightRAG query/create/track/list/exact-delete facade using provider-neutral evidence, async string IDs and no PostgreSQL knowledge reads/writes | AC7, AC10, AC11, AC14 | mock provider, HTTP, auth, caps and allowlist tests PASS |
| T11 | PRE_MERGE | PARTIAL | Add pinned one-replica official LightRAG service to local/CI Compose with two Gunicorn workers, native four-store config, private strong API key/network, route-tiered request limits, memory/volume limits and persistent `WORKING_DIR` | AC5, AC6, AC15 | digest/static security checks, strict readiness and guarded live/lifecycle runners PASS; official-container lifecycle cannot run locally without Docker |
| T12 | PRE_MERGE | PARTIAL | Add bounded dry-run/import Go command for selected legacy knowledge with deterministic sources, 409 reconciliation and no ID writeback/synchronizer | AC7, AC14, AC15 | dry-run/resume/replay/failure/deadline tests PASS; live official import blocked |
| T13 | PRE_MERGE | DONE | Implement summary refresh, monotonic Context Builder and owner-only Assistant profile UseCase/API | AC8, AC9, AC11 | unit/HTTP/race plus live PostgreSQL profile/summary CAS tests PASS |
| T14 | PRE_MERGE | DONE | Implement profile and async knowledge-admin UI while preserving chat/SSE citations, cancellation, account isolation and accessibility | AC7-AC9, AC14 | Vitest and transport tests PASS; heavy message rendering is lazy-loaded and the production build enforces a 500 KiB initial-entry budget |
| T15 | PRE_MERGE | DONE | Add protected low-cardinality Prometheus metrics and safe Agent/LightRAG/memory logs; document host metrics and optional OTel boundary | AC11, AC12 | registry/model/HTTP/log redaction, cardinality and race tests PASS |
| T16 | PRE_MERGE | DONE | Wire feature flag and Agents; verify disabled baseline, enabled fail-closed LightRAG readiness and bounded shutdown | AC10, AC14, AC15 | composition/config/readiness/lifecycle tests PASS |
| T17 | PRE_MERGE | DONE | Run deterministic baseline/candidate eval and meet approved thresholds without unsupported claims | AC13 | 8 cases PASS; aggregate/per-case output produced by `make eval` |
| T18 | PRE_MERGE | BLOCKED | Complete docs/rollback and all focused/race/PostgreSQL/frontend/architecture/container/full CI gates including native-store restart/restore | AC14, AC15 | full local CI with PostgreSQL 17 + pgvector exits 0; isolated auth/ingest/query/restart/archive/restore/delete runner implemented but execution remains blocked by missing Docker/providers |
| T19 | ROLLOUT | BLOCKED | Provision approved private one-replica LightRAG service and persistent volume; apply migration disabled, restore smoke, import demo corpus and test rollback | AC5-AC7, AC14, AC15 | requires an approved deployment target, model credentials and persistent volume |
| T20 | ROLLOUT | BLOCKED | Enable an internal cohort and observe quality, latency, process memory, corpus/volume size, pipeline status, failures and privacy-safe telemetry | AC6, AC7, AC10-AC13, AC15 | requires T19 plus an observation window; host metrics are orchestrator-owned |
| T21 | ROLLOUT | BLOCKED | Run approved real-model and optional Web eval with pinned LightRAG/model/embedding/Skill/dataset versions and cost/token evidence | AC4, AC5, AC10, AC12, AC13 | requires provider credentials and cost authorization |
| T22 | FOLLOW_UP | TODO | Migrate LightRAG to an external backend only when measured corpus, memory, availability or multi-replica requirements exceed the native-store envelope | excluded | separate reviewed storage migration |
| T23 | FOLLOW_UP | TODO | Add side-effect approval protocol only through a separate reviewed transactional-Agent spec | excluded | explicit product requirement |

Implementation order is T1 -> T2/T3 -> T4/T5 -> T6/T7/T8 -> T9 -> T10/T11/T12 ->
T13/T14 -> T15/T16 -> T17/T18. T0 blocks production changes. T19-T21 require
separate environment, credential, network and cost approval.

Statuses are `TODO`, `IN_PROGRESS`, `DONE`, `PARTIAL`, or `BLOCKED`. Every incomplete
PRE_MERGE row blocks READY.
