# Documentation Map

Keep current rules here and feature history under `specs/`.

| Document | Purpose |
|---|---|
| `../ARCHITECTURE.md` | Current architecture and hard boundaries |
| `guidance/development-lifecycle.md` | Requirement-to-rollout workflow |
| `guidance/spec-driven-development.md` | Spec modes, contents and approval gates |
| `guidance/local-verification.md` | Local/CI commands and evidence rules |
| `reference/readiness-checklist.md` | Review, merge and rollout checklist |
| `reference/ai-coding-harness.md` | Public AI Coding execution chain |
| `reference/spec-template.md` | Feature spec starter |
| `reference/task-template.md` | Classified task starter |
| `reference/test-plan-template.md` | Verification plan starter |
| `reference/report-template.md` | Delivery evidence starter |

Maintenance rules:

- Put repeatable procedures in `guidance/`.
- Put stable checklists and contracts in `reference/`.
- Put feature-specific decisions and history in `specs/`.
- Update the smallest durable document when implementation proves a rule stale.
- Historical specs do not override `AGENTS.md` or `ARCHITECTURE.md`.
