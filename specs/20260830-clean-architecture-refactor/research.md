# Research

Baseline: master@49004fd55807085939508220fe01e88846fdffd2

## Repository Findings

- Repository is small (about 90 tracked files, 10 commits) but split into seven Maven modules.
- The package names imply layering, yet ChatService is the effective application core and directly coordinates memory, planning, search, synthesis, verification and persistence.
- Search performs up to six subqueries serially and merges incompatible source scores with direct sorting.
- RAG combines chunking, embedding, database access, fallback and fusion in one service.
- Current test coverage is one Spring context smoke test.
- There is no README, CI, Maven wrapper or runnable environment checked in.
- Existing frontend depends on two chat endpoints and should remain unchanged during migration.

## Options

### A. Keep Java and add Spring AI Alibaba Graph

Pros: least rewrite, retains current Spring AI integrations.
Cons: preserves the current Java stack, requires careful module surgery, and does not align with the requested Go clean-architecture direction.

### B. Big-bang Go rewrite

Pros: clean final tree immediately.
Cons: no behavior baseline, high regression risk, difficult rollback, easy to mix architecture and product changes.

### C. Incremental Go vertical slice — selected

Pros: preserves contracts/data, produces runnable milestones, supports parallel comparison and rollback.
Cons: temporary dual-stack repository and some duplicated adapter work.

## Framework Evidence

- Eino is a Go AI application framework with ChatModel, Embedding, Retriever, Tool, Chain/Graph, streaming, callbacks and Agent/ADK capabilities:
  https://www.cloudwego.io/docs/eino/core_modules/
- Eino Graph supports branches, parallelism and loops; its docs recommend keeping orchestration clear and business logic inside components:
  https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/
- Eino's own guidance treats Agent and Graph as complementary; deterministic Graph capabilities can be exposed as Agent tools:
  https://www.cloudwego.io/docs/eino/overview/graph_or_agent/
- Latest stable Eino release observed during research is v0.9.13; v0.10 is alpha, so migration should stay on v0.9 stable:
  https://github.com/cloudwego/eino/releases
- Hertz provides middleware, validation, streaming and a native SSE package with disconnect-aware patterns:
  https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/sse/
- pgx/v5 is the stable PostgreSQL-specific Go driver/toolkit and recommends its native interface for PostgreSQL-only applications:
  https://github.com/jackc/pgx
- pgvector-go supports pgx and provides official Go vector types:
  https://github.com/pgvector/pgvector-go
- Go 1.27 was released on 2026-08-19 and is the current stable major release:
  https://go.dev/blog/go1.27

## Minimality Check

Skipped in the base migration:

- no custom Agent framework because Eino already supplies Graph/streaming/callbacks;
- no ORM because existing SQL is small and PostgreSQL-specific;
- no DI framework because one composition root is sufficient;
- no Redis/MQ/microservices because no measured need exists;
- no new Agent roles because current refactor is about boundaries and parity.

Add those only when a measured requirement cannot be met by the selected stack.
