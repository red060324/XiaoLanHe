# Delivery And Verification Report

- Status: `VERIFYING — LOCAL CODE GATES PASS; EXTERNAL PRE_MERGE GATES BLOCKED`
- Date: 2026-09-04
- Branch: `codex/clean-architecture-refactor`
- Authoritative spec: `./spec.md`
- Worktree: existing dirty worktree preserved; no commit, push or deployment performed

## Outcome

The approved application behavior is implemented. XiaoLanHe now has a bounded
three-Agent read-only Assistant, four versioned Skills, layered memory, direct
official LightRAG knowledge ownership, an asynchronous knowledge-admin facade/UI, a
one-time importer, deterministic evaluation and protected low-cardinality runtime
metrics. Advanced knowledge neither creates a new PostgreSQL knowledge schema nor
dual-writes the legacy knowledge tables.

The selected LightRAG topology remains deliberately narrow: one official service
replica, two supported Gunicorn workers and one persistent `WORKING_DIR` using
`JsonKVStorage`, `NanoVectorDBStorage`, `NetworkXStorage` and
`JsonDocStatusStorage`. It is a small-corpus single-instance design, not a
multi-replica/high-availability or large-corpus production claim.
Startup now fails closed unless authenticated upstream contracts attest the pinned
core/API version, exact workspace and working directory, all four stores, Gunicorn
mode with two workers, explicit pipeline fields and a clear recovery fence. The
unauthenticated health path is limited to liveness and is checked not to disclose
configuration, paths or topology.

This revision is ready for code review but is not yet READY for production or merge
under the approved spec. The required official-container ingestion/retrieval/restart/
restore cases could not run because no Docker/Podman runtime is installed. Live
PostgreSQL 17 + pgvector migration/profile/summary and full-product integration did
run and pass. Real-provider and shared-environment activity remains rollout-only.

## Acceptance Criteria

| Criterion | Implemented evidence | Current result |
|---|---|---|
| AC1 hierarchical Multi-Agent | Game Copilot, independent Research/Planning workers and deterministic supervisor tests | PASS |
| AC2 typed minimum-context delegation | versioned envelopes, strict validation, server-owned evidence IDs and foreign/stale rejection tests | PASS |
| AC3 reusable Skills | four embedded JSON Skills with delegate/tool/mode/budget/output startup validation | PASS |
| AC4 Agentic query planning | bounded 1-8 unit planner, facet/source/filter/mode validation and fallback tests | PASS |
| AC5 real official LightRAG retrieval | strict official `/query/data` adapter and contract/adversarial tests implemented; no Go substitute | PARTIAL — official live retrieval blocked |
| AC6 native LightRAG storage | digest-pinned one-replica/two-worker/four-store manifest, strict topology readiness and isolated lifecycle checker implemented | BLOCKED — no local container runtime for ingestion/restart/restore evidence |
| AC7 LightRAG-owned knowledge | direct create/track/list/exact-delete facade, provider-neutral search, no projection/sync, one-time importer | PARTIAL — adapter/HTTP tests pass; official live lifecycle blocked |
| AC8 layered memory | latest-eight context, 12,000-rune refresh threshold, 2,000-rune cap and monotonic CAS implementation | PASS — unit/race and live PostgreSQL CAS |
| AC9 profile ownership | typed authenticated projection plus owner-only get/replace/clear API/UI | PASS — unit/HTTP/UI and live PostgreSQL preservation/ownership |
| AC10 safety and limits | shared 45s/12 model/12 tool/3 delegation budget, local caps, cancellation and explicit LightRAG failure | PASS |
| AC11 read-only trust boundary | no business mutation ports/tools; fixed server-side endpoints/identity; strict provider bounds and redaction | PASS |
| AC12 truthful observability | protected Prometheus registry, fixed label vocabularies, safe logs and provider-reported usage semantics | PASS — host volume/process metrics remain deployment-owned; no OTel span claim |
| AC13 measurable quality | versioned 8-case baseline/candidate deterministic evaluation | PASS |
| AC14 contracts and architecture | compatible REST/SSE, intentional async knowledge migration, boundary import hook and full local regressions | PASS |
| AC15 controlled delivery | additive migration, feature flag, fail-closed readiness, guarded lifecycle runner, docs and rollback implemented | PARTIAL — live DB passes; official container/GitHub Actions evidence incomplete |

## Verification Evidence

| Gate | Command/environment | Result |
|---|---|---|
| Full local repository gate | `XLH_TEST_DATABASE_URL=<isolated PostgreSQL 17 URL> GOCACHE=/private/tmp/xlh-final-go-cache make ci BASE_REF=origin/codex/clean-architecture-refactor` on 2026-09-04 | PASS (exit 0) |
| Go static and unit suites | full `go vet ./...` and `go test -count=1 ./...` inside the CI target | PASS |
| Race detector | full `go test -race -count=1 ./...` inside the CI target | PASS |
| Deterministic Assistant eval | `go run ./cmd/eval-assistant`; 8 cases, report `passed=true` | PASS |
| Eval quality/safety | advanced route/Skill, facets, Recall@8, citation and profile all `1.0`; unsupported/unauthorized/foreign/fallback all `0` | PASS |
| Eval fixture latency | advanced P50/P95 `107/121ms`, baseline `84/91ms` | REPORTED — deterministic fixture, not provider latency |
| Frontend | Vitest 6 files / 80 tests; TypeScript/Vite build; initial entry 246,161/512,000 bytes | PASS |
| Repository policy | formatting, hooks, doc links, placeholders, architecture, spec drift, shell syntax and LightRAG static manifest/security checks | PASS |
| Diff whitespace | `git diff --check` | PASS |
| PostgreSQL integration | temporary PostgreSQL 17.11 + pgvector 0.8.6; `TestProductPostgres` ran in ordinary and race suites | PASS |
| Official LightRAG runtime | guarded `make lightrag-lifecycle` runner is checked in; `docker` and `podman` commands are not installed, so it was not executed | ENVIRONMENT BLOCKED |
| GitHub Actions | workflow now provisions PostgreSQL, Redis/RocketMQ and pinned LightRAG health-contract checks | NOT RUN on this unpushed worktree |
| Real model/embedding/Web | needs credentials, network, cost approval and an isolated target | ROLLOUT BLOCKED |

The Assistant message renderer now loads through a React lazy boundary, reducing
the initial Vite entry from about 982 kB to 246 kB (about 76 kB gzip). Streamdown's
optional Mermaid/Shiki renderer chunks remain deferred and some exceed 500 kB;
that warning no longer represents initial-route transfer and is not a failed
functional gate.

## Implemented Boundaries

- Game Copilot performs at most four transitions and can delegate only to the
  selected Skill's Research and Planning workers. Workers cannot recurse.
- Research receives bounded query briefs rather than full history/profile. Planning
  receives only typed constraints, explicit profile projection, ownership IDs and
  run-local evidence IDs.
- Assistant operations remain permanently read-only. Coupon, order, payment,
  flash-sale, community and knowledge/profile administration are not Agent tools.
- LightRAG references are rendered as source labels unless a real safe URL exists;
  official `reference_id/file_path` data is never turned into a fabricated URL.
- LightRAG authentication requires a 32-512 character API key without CR/LF.
  `/auth/verify` must return `status=ok`; required health and pipeline fields may not
  disappear behind Go zero values. Redirects, oversized responses and recovery mode
  fail closed.
- The official route-specific request limits remain authoritative: normal routes use
  the upstream 1 MiB default while `/documents/text` retains its 50 MiB allowance.
  Deployment checks reject a global override that would silently collapse that tier.
- The application `/metrics` route exists only with `XLH_METRICS_TOKEN` and requires
  constant-time Bearer validation. Advanced or flash-sale mode requires a 32-512
  character token.
- Metric labels never contain run/session/user/source/content values. Provider token
  totals are emitted only from actual response usage metadata; omitted metadata is
  exposed as `usage_reported=false`.
- Application metrics report API-confirmable LightRAG storage contract, pipeline,
  recovery and managed document status. Volume bytes/free space, filesystem state,
  process RSS and container restarts must come from host/orchestrator monitoring.
- Runtime OpenTelemetry export/spans are not implemented and are not claimed.

## Required Environment Completion

Before marking this spec READY, run and attach evidence for all blocked PRE_MERGE
cases:

1. In an isolated host with Docker and approved test LLM/embedding credentials, run
   `XLH_LIGHTRAG_LIFECYCLE_ACK=isolated-destructive-test make lightrag-lifecycle`.
   The checked-in runner uses a unique empty Compose project and fixed loopback
   endpoint to exercise auth, create/track, duplicate rejection, all four retrieval
   modes, clean restart, full-volume archive/empty-volume restore and exact delete.
2. Retain the runner output and archive manifest, then separately exercise
   read-only/full volume and declared memory/corpus limits. The runner's cleanup is
   deliberately limited to resources it marked and created.
3. Confirm host/orchestrator policy prevents a second replica or shared writable
   workspace and keeps the authenticated API private; preserve missing/wrong-key and
   public-health non-disclosure evidence.
4. Run the checked-in GitHub Actions workflow on a clean checkout of the exact
   revision.

Real-model quality/cost evaluation, internal cohort observation and production
capacity alerts remain T19-T21 rollout gates rather than PRE_MERGE substitutes.

## Rollout And Rollback

No rollout or external mutation has occurred. Rollout keeps advanced mode disabled
while migration 007 is applied, verifies and snapshots the one LightRAG workspace,
dry-runs then explicitly imports an approved corpus, and only then enables a bounded
internal cohort.

Rollback stops new knowledge mutations, disables `XLH_ADVANCED_AI_ENABLED`, restores
the existing baseline Assistant/local-knowledge path and retains the entire LightRAG
volume for diagnosis. There is no reverse synchronization because advanced mode
never writes a PostgreSQL projection or continuously updates legacy knowledge.

## Decision

- Ready for code review: **yes**; the complete local deterministic/code gate passes.
- Ready for merge under this spec: **no**; required official LightRAG PRE_MERGE
  container cases and the clean-checkout GitHub Actions run remain incomplete.
- Ready for production rollout: **no**; isolated deployment, restore, real-provider
  evaluation, capacity monitoring and observation evidence are still required.
