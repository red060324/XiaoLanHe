# Research Notes

- Status: `SUPERSEDED`
- Superseded by: `../20260904-advanced-ai-architecture/spec.md`
- Authoritative spec: `./spec.md`

## Public Sources

- [Eino](https://github.com/cloudwego/eino) is the already-installed public Go
  framework. Its ADK/graph abstractions cover bounded tool Agents and workflow
  composition without exposing framework types to business contracts.
- The public [Eino documentation index](https://github.com/cloudwego/eino/blob/main/llms.txt)
  lists Graph/Workflow, Skill and summarization middleware, Plan-and-Execute,
  Supervisor, and multi-agent patterns. Availability does not mean every
  pattern belongs in this product.
- The [LightRAG paper](https://arxiv.org/abs/2410.05779) motivates combining
  graph structures with vector retrieval and low/high-level knowledge search.
- The [LightRAG repository](https://github.com/HKUDS/LightRAG) exposes a Python
  server/SDK and separate KV/vector/graph/document-status storage contracts.
  Direct adoption would add a second runtime to the current Go deployment.
- [pgvector](https://github.com/pgvector/pgvector) is already deployed by the
  repository and keeps exact/approximate vector search inside PostgreSQL.

## Current Code Findings

1. `internal/adapter/eino/research_agent.go` is a real model-controlled Agent:
   it observes typed tool results and may refine queries, with four read-only
   tools and explicit iteration/tool/time budgets.
2. `internal/adapter/eino/nodes.go` currently combines route selection and
   subquery creation. Splitting a typed Planner makes query quality measurable
   without adding another Agent.
3. `internal/adapter/postgres/conversation.go` reads a `summary_text` metadata
   key but never refreshes it. The comment accurately identifies the upgrade
   trigger; the requested memory scope now satisfies that trigger.
4. `player_profile` is unused. Reusing it is cheaper than a second personal-data
   store, provided uniqueness and an explicit user-owned HTTP contract are
   added.
5. `knowledge_chunk` already has pgvector/HNSW. PostgreSQL tables for entities,
   relations, provenance, and FTS are the smallest graph-enhanced extension.
6. `uniqueEvidence` preserves first-seen tool order and ignores score
   calibration. RRF is deterministic and avoids pretending provider scores are
   directly comparable.

## Alternatives

| Alternative | Decision | Reason |
|---|---|---|
| Current single Research Agent + better Nodes/capabilities | selected | preserves autonomy where useful and deterministic control elsewhere |
| Supervisor plus three worker Agents now | rejected | no eval evidence justifies extra cost/failure/merge paths |
| Direct LightRAG Python service | rejected | violates Go-only/single-process constraint |
| PostgreSQL-native graph/vector/FTS | selected | reuses the existing durable dependency and backup model |
| Automatically inferred long-term profile | rejected | consent/correction and durable accuracy are unclear |
| Explicit user-managed profile | selected | observable, correctable, and no Agent write side effect |

## Resume Claim Boundary

Before implementation, describe the current system as a bounded Agentic RAG,
not Multi-Agent. After this spec passes, valid claims may mention structured
Plan-and-Execute, versioned Skills, layered memory, and PostgreSQL-native
graph/vector hybrid retrieval. "Significant improvement" and "Multi-Agent"
remain invalid until their respective eval and implementation evidence exists.
