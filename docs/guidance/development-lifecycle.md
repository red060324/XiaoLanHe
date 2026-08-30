# Development Lifecycle

Use this lifecycle for every feature, behavior change, architecture change,
storage change, Agent change, or non-trivial refactor.

## Default Flow

```text
request
  -> inspect and clarify
  -> draft spec and test plan
  -> human approval
  -> branch and implementation
  -> local verification
  -> CI and interface verification
  -> review handoff
  -> merge approval
  -> rollout and observation
```

Existing work-in-progress code is research input. It does not bypass the spec
gate or become an architecture precedent.

## Lifecycle States

| State | Meaning | Exit evidence |
|---|---|---|
| `DRAFT` | Scope/design is being clarified | reviewable spec set |
| `APPROVED` | Human approved scope, design and test plan | explicit approval |
| `IMPLEMENTING` | Approved tasks are being changed | task updates and tests |
| `VERIFYING` | Behavior is complete; gates are running | verification evidence |
| `READY` | Every PRE_MERGE gate passed | readiness checklist |
| `ROLLOUT` | Change is deployed or smoke-tested | rollout evidence |
| `COMPLETE` | Required rollout work is done | final report |

`BLOCKED` is evidence, not a substitute for unfinished work. Record the exact
dependency, attempted checks and the decision required.

## 1. Intake And Clarification

Read the request, `AGENTS.md`, `ARCHITECTURE.md`, relevant specs, code and
tests. Classify nearby code as reusable, migration debt or uncertain.

Clarify until these are known or explicitly assumed:

- goal, scope and non-goals
- testable acceptance criteria
- affected modules, APIs, storage, config and external providers
- failure/degradation behavior
- verification and rollout depth
- operations requiring human approval

Ask focused blocking questions; record non-blocking assumptions in the spec.

## 2. Spec And Design

Follow `spec-driven-development.md`. Choose the lightest valid mode and create
the test plan before production code.

Human gate: present the authoritative spec, architecture compliance section,
tasks and test plan. Do not implement until the user approves them.

## 3. Branch And Implementation

After approval:

1. Create or switch to a `codex/<feature>` branch and push it when remote work
   or CI is needed.
2. Re-read the approved architecture sections.
3. Implement contract -> business behavior -> external adapter -> wiring.
4. Keep changes inside the owning business module.
5. Add or update the smallest tests that prove changed behavior.
6. Update spec/tasks/test-plan before continuing if scope or design changes.

Do not add production fixtures, test-only branches, fake provider responses or
temporary credentials to make an online check pass.

## 4. Verification

Follow `local-verification.md`.

- Start with the exact changed package/test.
- Broaden according to risk.
- Agent logic uses deterministic fake models/tools before real-model smoke.
- API/storage changes require boundary/integration evidence.
- CI is the clean Linux/full-build signal.

Record the command, whether tests actually executed, result and skipped checks.

## 5. Review Handoff

Full specs and cross-module changes produce `report.md`. A handoff includes:

- what changed and why
- acceptance-criterion traceability
- architecture compliance and retained debt
- exact verification evidence
- skipped/blocked checks and residual risk
- migration, deployment, rollback and follow-up work

Complete `docs/reference/readiness-checklist.md`. Any failed or incomplete
PRE_MERGE item blocks merge readiness.

## 6. Merge And Rollout

Human approval is required before merge, production deployment, destructive
schema change, paid external verification, or write operations against shared
environments.

For rollout:

- use backward-compatible migrations
- verify health and key user paths
- inspect logs, metrics and Agent traces
- confirm rollback revision/procedure
- record follow-up results in the spec report

## Fast Path

Fast path is valid only for a narrow local change with no product behavior,
public contract, storage/config, Agent behavior, deployment or architecture
impact, or when the user explicitly requests it.

It still requires clear scope, a minimal task list, relevant verification and a
handoff note explaining why a full spec was unnecessary.

