---
name: xiaolanhe-feature-workflow
description: Use for XiaoLanHe feature requests, behavior or architecture changes, Agent work, and implementation-bound refactors. Requires reviewed scope, design, tasks, and test plan before production code changes.
---

# XiaoLanHe Feature Workflow

## Intake

1. From the repository root, read `AGENTS.md`, `ARCHITECTURE.md`, the lifecycle
   and spec guidance, the requirement source, active specs, and relevant code.
2. Classify nearby code as reusable, migration debt, or uncertain.
3. Clarify goal, non-goals, acceptance criteria, modules, contracts, storage,
   dependencies, failure behavior, rollout, and verification.

## Spec Gate

Create one authoritative `specs/YYYYMMDD-feature-name/` directory. Use the
templates under `docs/reference/`. Lite work needs `spec.md`, `tasks.md`, and
`test-plan.md`; cross-module, architecture, Agent, storage, public-contract, or
rollout work also needs `plan.md` and a final `report.md`.

For Agent work, explicitly classify Nodes versus Agents and define tools,
read/write scope, trusted context, cancellation, iteration/tool/time budgets,
memory, degraded behavior, observability, evals, and side-effect approval.

Present the spec, tasks, and test plan for human review. Stop before production
code until approval is explicit.

## Implementation And Verification

After approval, implement contract, business behavior, adapter, then wiring.
Keep framework/provider types at the boundary and add the smallest test proving
each changed behavior. If scope or design changes, update the spec before code.

Use the canonical Make targets in `docs/guidance/local-verification.md`. Run the
narrowest relevant check first, then every PRE_MERGE gate. Record commands,
executed tests, failures, skipped checks, and environment blockers.

## Handoff

Map acceptance criteria to the final diff and evidence. Report retained debt,
rollout/rollback state, and residual risk. A full spec is not READY while any
PRE_MERGE task is incomplete.
