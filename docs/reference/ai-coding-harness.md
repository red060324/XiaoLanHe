# AI Coding Harness

XiaoLanHe borrows auto_msg's closed-loop development flow while removing its
ByteDance-only infrastructure.

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

## auto_msg Sources And XiaoLanHe Adaptation

| auto_msg source | Purpose | XiaoLanHe result |
|---|---|---|
| `AGENTS.md`, `ARCHITECTURE.md`, module `AGENTS.md` | Progressive repository context | Root map plus durable architecture; add module maps only when modules exist |
| `docs/guidance/development-lifecycle.md` | Requirement-to-rollout states and human gates | Retained with open-source deployment language |
| `docs/guidance/spec-driven-development.md` and reference templates | Reviewable intent, design, tasks, tests, evidence | Lite/full specs and four concise templates |
| `skills/plugins/message-ads-service/` | Repo-owned repeatable workflow | `skills/plugins/xiaolanhe/` with one feature workflow |
| `sync_repo_skills.py` | Keep checked-in Skill authoritative | Local-only safe sync with an owned lock file |
| `.hooks/` and `.pre-commit-config.yaml` | Structural backpressure before review | Text, links, placeholders, architecture, and spec drift only |
| `Makefile` | One command surface for people, agents, and CI | `make verify` and `make ci` |
| `.codebase/pipelines/ci.yaml` | Clean environment repeats repository gates | `.github/workflows/go.yml` |
| PR/readiness/report files | Delivery evidence and explicit residual risk | PR template, readiness checklist, report template |

## Deliberately Not Copied

- TP, RGO/Overpass, BITS, SCM, PPE, BAM, TCC, and ByteDance authentication.
- Hooks for historical auto_msg debt or generated-code boundaries.
- A large-file hook: the checked-in frontend bundle already contains large
  assets; asset policy should be decided separately.
- A blanket debug-print ban: frontend warnings and command-line output need a
  project-specific logging rule before this can be low-noise.
- More Skills, Agents, generators, or module maps before a repeated workflow
  proves they are needed.

## Adding Backpressure

Use a document for contextual guidance, a test for behavior, a hook for a
mechanical invariant, and a Skill only for a repeated multi-step workflow.
Every local check must be runnable without private infrastructure and must also
run in CI.
