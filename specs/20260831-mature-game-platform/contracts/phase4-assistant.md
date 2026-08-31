# Phase 4 Assistant Contract

The Assistant remains request-scoped and read-only. Router and Answer each run
as one bounded model node. Research is the only autonomous Agent.

## Tool Allowlist

| Tool | Input | Evidence source | Citation URL |
|---|---|---|---|
| `search_knowledge` | `query`, optional `gameCode`, `regionCode` | approved local guide chunks | stored HTTP(S) source URL when present |
| `search_catalog` | `query`, optional `region`, `currency` | active catalog games | `/api/games/{slug}` |
| `search_forum` | `query`, optional `gameId` | published community posts | `/api/community/posts/{id}` |
| `search_web` | `query` | configured public SearXNG provider | result HTTP(S) URL |

`search_web` is registered only when Web Search is enabled. Unknown and
mutation-like names such as `create_order`, `claim_coupon`, `pay_order`, or
`create_post` are not registered and cannot reach an ordinary service.

Every tool returns a typed observation with `ok`, `no_result`, `invalid`, or
`failed` status. Provider failures are observations that the Agent may refine;
they are not successful empty searches. Request cancellation, total deadline,
per-tool deadline, model-iteration budget, and tool-call budget remain in force.

## Citations

Evidence retains source, title, content, and URL through Research and Answer.
The Answer Node deterministically appends a Markdown source list for valid
HTTP(S) URLs and local `/api/` resource URLs, including for SSE responses.
Unsafe schemes and credential-bearing URLs are omitted. The model is not trusted
to reproduce citations itself.

## Failure Semantics

- Successful search with no matches: `no_result` and an explicit note.
- Some successful tools and some failed tools: `partial`, evidence retained.
- Every called tool fails: request fails with `all research tools failed`.
- Tool or iteration budget reached: `bounded`; partial evidence is retained.
- Request cancellation or deadline: propagated to model, tools, and caller.

Real model and public Web calls are rollout checks. Deterministic fake-model and
fake-tool tests are the pre-merge evidence.
