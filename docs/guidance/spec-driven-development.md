# Spec-Driven Development

The spec set is the reviewed contract between product intent and implementation.
It is maintained with the code, not written once and ignored.

## Spec Modes

| Mode | Use when | Required artifacts |
|---|---|---|
| Lite | Local behavior, known pattern, no public/storage/config/Agent/rollout risk | `spec.md`, `tasks.md`, `test-plan.md` |
| Full | Cross-module work, architecture or Agent change, public API, storage, config, security or rollout risk | `spec.md`, `plan.md`, `tasks.md`, `test-plan.md` |

Add only when needed:

- `research.md`: verified facts and alternatives
- `data-model.md`: entities, tables, indexes, retention and migration
- `contracts/`: HTTP/SSE/provider examples or schemas
- `report.md`: final evidence; required for full specs

Start from the matching files in `../reference/`: `spec-template.md`,
`task-template.md`, `test-plan-template.md`, and `report-template.md`. Delete
inapplicable sections instead of leaving placeholders.

## Location And Ownership

```text
specs/YYYYMMDD-feature-name/
```

One feature/branch has one authoritative active spec. Mark overlapping older
work `superseded-by`, or state its non-overlapping historical scope. Conflicting
active specs block implementation.

## `spec.md`

Required sections:

- status, owner, source and branch
- goal/background
- decisions
- in scope and non-goals
- current-state evidence and debt classification
- testable acceptance criteria (`AC1`, `AC2`, ...)
- assumptions, open questions and clarify-round decisions

Do not encode implementation tasks as acceptance criteria. Criteria describe
observable product, contract, safety or architecture outcomes.

## `plan.md`

Required for full specs. Include:

- selected approach and rejected alternatives
- runtime and compile-time dependency flow
- affected modules/packages and composition root
- Entry, Presenter, UseCase, Entity and Repository ownership
- request/result/protocol and serialization boundaries
- required, optional and degraded dependencies
- errors, absence, partial results and cancellation
- concurrency, retry, limits and backpressure
- storage schema, cardinality, constraints, retention, cleanup and migration
- authentication, authorization, secrets and trust boundaries
- config, rollout, compatibility and rollback
- logs, metrics, traces and alertable failures
- migration debt touched or deliberately retained

For AI/Agent work, also include:

- Node versus Agent classification
- model-controlled decisions and deterministic controls
- tool names, schemas, read/write classification and trusted context
- maximum iterations, tool calls, time and token/cost budget
- memory ownership and retention
- prompt/version strategy
- deterministic evals and real-model rollout smoke
- human approval for side effects

## `tasks.md`

Every row has a class and evidence:

| Class | Meaning |
|---|---|
| `PRE_MERGE` | Must complete before review/merge readiness |
| `ROLLOUT` | Runs during deployment or live smoke |
| `FOLLOW_UP` | Explicitly excluded future work with trigger/owner |

Use `TODO`, `IN_PROGRESS`, `DONE`, or `BLOCKED`. An incomplete `PRE_MERGE`
task blocks handoff regardless of how much code is finished.

## `test-plan.md`

Write it before coding. Cover applicable layers:

- unit and deterministic behavior
- Agent loop/eval with fake model and tools
- storage/provider integration
- HTTP/SSE contract and cancellation
- frontend build and user flow
- race/concurrency and idempotency
- architecture/static checks
- observability and sensitive-data assertions
- rollout smoke and rollback

State what is not applicable and why. Never silently omit a relevant check.

## Architecture Compliance And Traceability

The plan must map every planned production behavior to an acceptance criterion,
approved deviation or named migration debt:

| Planned change | Owner/module | Requirement |
|---|---|---|
| example | assistant | AC3 |

Before handoff, repeat the mapping against the final merge-base diff. Remove or
separately approve unexplained behavior changes.

## Review Gates

1. Scope gate: goal, non-goals and assumptions approved.
2. Design gate: architecture, contracts, storage and risks approved.
3. Task gate: implementation order and test plan approved.
4. Implementation gate: production changes may begin.
5. Delivery gate: PRE_MERGE evidence and final diff are complete.
6. Rollout gate: environment, side effects and rollback are approved.

If implementation deviates from an approved decision, update the spec and ask
for review before continuing.
