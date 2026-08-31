# AGENTS.md - XiaoLanHe Repository Map

XiaoLanHe is a Go modular monolith for a game community, game discovery,
promotions, orders, and a read-only game assistant.

## Read Order

1. Read `ARCHITECTURE.md` for dependency and module boundaries.
2. For feature or behavior work, read
   `docs/guidance/development-lifecycle.md`,
   `docs/guidance/spec-driven-development.md`, and
   `skills/plugins/xiaolanhe/skills/xiaolanhe-feature-workflow/SKILL.md`.
3. Read the active spec and the closest module code/tests.
4. Treat existing code as current-state evidence, not automatic precedent.

## Documentation Map

| Need | Read |
|---|---|
| Architecture and module boundaries | `ARCHITECTURE.md` |
| End-to-end development lifecycle | `docs/guidance/development-lifecycle.md` |
| Spec format and review gates | `docs/guidance/spec-driven-development.md` |
| Local and CI verification | `docs/guidance/local-verification.md` |
| Review/merge/rollout decision | `docs/reference/readiness-checklist.md` |
| Public AI Coding harness | `docs/reference/ai-coding-harness.md` |

Before feature, architecture, Agent, or non-trivial refactor work, sync the
repo-owned Skill from the repository root:

```bash
python3 skills/plugins/xiaolanhe/lib/agents/sync_repo_skills.py --prune
```

## Hard Rules

- Production code follows Clean Architecture's dependency rule.
- Organize the repository by business module; do not create repository-wide
  horizontal `usecase`, `entity`, or `repository` dumping grounds.
- Entry and Presenter own HTTP/SSE protocol adaptation only.
- UseCases orchestrate; Entities own business rules; Repositories own external
  access mechanics.
- Eino, Hertz, pgx, model DTOs, and provider errors do not enter public UseCase
  or Entity contracts.
- Create interfaces only at a real cross-boundary dependency or when multiple
  implementations exist.
- The assistant is read-only. It must not claim coupons, create orders, pay, or
  mutate community content unless a later reviewed spec explicitly adds that
  capability.
- Router and Answer are bounded model nodes. Research is the only autonomous
  Agent in the current target.
- The Agent and ordinary HTTP services deploy in one Go process until a
  measured scaling, isolation, or long-running-work requirement justifies a
  separate service.

## Spec Gate

Every feature or behavior change must have one authoritative active spec under
`specs/YYYYMMDD-feature-name/`. Present the spec, plan, tasks, and test plan for
review before changing production code.

Use `make verify` for the normal local gate and `make ci` for the repository CI
gate. Report checks that were not run; compilation or an empty test selection
is not test evidence.
