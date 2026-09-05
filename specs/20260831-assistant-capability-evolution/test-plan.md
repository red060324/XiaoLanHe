# Test Plan

- Status: `SUPERSEDED`
- Superseded by: `../20260904-advanced-ai-architecture/test-plan.md`
- Authoritative spec: `./spec.md`

## Scope And Environments

PRE_MERGE uses deterministic fake model Nodes, fake tools, fixed clocks, and an
isolated PostgreSQL/pgvector service. Real model/Web calls remain opt-in rollout
checks and cannot replace deterministic evidence. Start with exact tests, then
affected packages, race, frontend, `make ci BASE_REF=origin/master`, container
smoke, and public GitHub Actions.

## Cases

| ID | Class | Layer | Scenario | Expected result | Command/evidence | Status |
|---|---|---|---|---|---|---|
| V1 | PRE_MERGE | baseline | current route/queries/tool/evidence fixtures | immutable comparison input and metric schema | eval baseline command | TODO |
| V2 | PRE_MERGE | Router/Planner | direct, clarify, valid plan, invalid JSON/source/count/query, provider failure | one route/Skill; validated plan or safe local fallback; no CoT persisted | exact Node/UseCase tests | TODO |
| V3 | PRE_MERGE | Skill | three named Skills, generic fallback, unknown version/tool, startup validation | immutable definitions; only allowed tools registered per run | Skill/Agent tests | TODO |
| V4 | PRE_MERGE | Agent | plan execution, refined query, partial/all failure, no result, max iterations/tools/time, cancellation | sole Agent stays bounded and read-only | deterministic Agent tests + race | TODO |
| V5 | PRE_MERGE | fusion | duplicate URLs/chunks, incompatible scores, tie/freshness/source priority | deterministic RRF order and complete provenance | fusion table tests | TODO |
| V6 | PRE_MERGE | memory | below/above threshold, prior summary, summary failure, concurrent watermark, guest/auth profile | correct window/summary/profile; no current-message duplication or backward watermark | UseCase/PostgreSQL/race tests | TODO |
| V7 | PRE_MERGE | profile API | get/replace/clear, invalid lists/region/currency/price, cross-user attempt, CSRF | owner-only validated profile; Agent cannot mutate it | HTTP/PostgreSQL/frontend tests | TODO |
| V8 | PRE_MERGE | migration | fresh/repeat/concurrent/checksum and upgrade from 005 | additive 006 applies exactly once and preserves data | PostgreSQL integration | TODO |
| V9 | PRE_MERGE | extractor | valid graph, malformed/oversized/duplicate/self relation, failure, replay | validated idempotent transaction or zero partial graph rows | fake extractor + PostgreSQL tests | TODO |
| V10 | PRE_MERGE | retrieval | FTS/vector/entity/relation/one/two-hop/no provenance/candidate cap | bounded hybrid candidates; every result maps to a source chunk/document | PostgreSQL retrieval tests | TODO |
| V11 | PRE_MERGE | contract | REST/SSE direct/research/citations/profile and disconnect | compatibility retained; cancellation propagates | HTTP/SSE tests | TODO |
| V12 | PRE_MERGE | privacy | route/Skill/plan/summary/graph success and failures | safe metrics present; no prompt/message/profile/token/raw output | captured log tests | TODO |
| V13 | PRE_MERGE | eval | route/Skill, facet coverage, Recall@K, citations, faithfulness rubric, latency/calls | baseline and candidate results reported separately; no unsupported improvement claim | deterministic eval report | TODO |
| V14 | PRE_MERGE | repository | Go/public-only/Java absence, architecture, spec drift, frontend, race, build | all repository gates pass | `make ci BASE_REF=origin/master` | TODO |
| V15 | PRE_MERGE | container | migration, startup Skill validation, existing product smoke | one Go container remains healthy and compatible | GitHub Actions | TODO |
| V16 | ROLLOUT | real model | named eval set with Planner/Summary/Extractor/Research | aggregate quality, latency, calls, tokens/cost and failures recorded | approved target report | TODO |
| V17 | ROLLOUT | optional Web | recent game fact with Web enabled/disabled/unavailable | freshness improves when available; local degraded answer otherwise | approved SearXNG smoke | TODO |

## Eval Threshold Gate

Approval before implementation must set candidate thresholds. Until then the
minimum non-regression gate is:

- route and Skill accuracy do not fall below the current deterministic fixture;
- required query-facet coverage and citation coverage do not regress;
- no increase in uncited factual answers on the rubric set;
- P95 model calls and latency are reported, not hidden by an average;
- profile/memory factual consistency is 100% for explicit fixture facts.

Multi-Agent remains excluded even if an eval fails; failure first produces a
root-cause report showing whether retrieval, planning, evidence, or synthesis is
responsible.

## Not Applicable

- No transactional/idempotency payment test is added because the Assistant
  remains read-only and commerce contracts are unchanged.
- No distributed worker/queue test because extraction is synchronous and the
  application remains one process.
- No Python/LightRAG compatibility test because no Python runtime is added.

## Exit Criteria

V1-V15 have executed PASS evidence, every AC maps to a result, migration and
rollback are proven, and no critical/high privacy or read-only safety defect
remains. V16-V17 stay explicit rollout evidence and cannot be reported as run
without the target, credentials, and cost approval.
