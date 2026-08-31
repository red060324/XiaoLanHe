# AI Coding Harness

XiaoLanHe keeps its development workflow inside the repository so public
contributors and coding agents can use the same documented process and checks.

## Execution Chain

```text
AGENTS.md / ARCHITECTURE.md
  -> repo-owned feature Skill
  -> reviewed spec + tasks + test plan
  -> implementation
  -> local hooks through Makefile
  -> the same gates in GitHub Actions
  -> report + readiness review
```

## Repository Components

| Component | Purpose |
|---|---|
| `AGENTS.md`, `ARCHITECTURE.md` | Progressive repository context and stable boundaries |
| `docs/guidance/development-lifecycle.md` | Requirement-to-rollout states and human gates |
| `docs/guidance/spec-driven-development.md` and templates | Reviewable intent, design, tasks, tests and evidence |
| `skills/plugins/xiaolanhe/` | One repository-owned repeatable feature workflow |
| `sync_repo_skills.py` | Install the checked-in Skill into a local agent directory |
| `.hooks/` and `.pre-commit-config.yaml` | Text, link, placeholder, architecture and spec-drift checks |
| `Makefile` | One command surface for people, agents and CI |
| `.github/workflows/go.yml` | Repeat repository gates in a clean public CI environment |
| PR/readiness/report files | Delivery evidence and explicit residual risk |

## Public Repository Boundary

- Documentation must stand alone without access to an employer, organization,
  private document, private issue tracker or private command-line tool.
- Runtime and CI may use only public open-source packages and public service
  contracts. Optional hosted providers are configured through environment
  variables and are replaceable by local implementations.
- Examples use localhost, reserved example domains or public product URLs.
- Secrets, credentials, private endpoints and organization-specific project
  names never enter source control.
- More Skills, Agents, generators or module maps are added only after a repeated
  workflow proves they are needed.

## Adding Backpressure

Use a document for contextual guidance, a test for behavior, a hook for a
mechanical invariant, and a Skill only for a repeated multi-step workflow.
Every local check must be runnable with public tools and must also run in CI.
