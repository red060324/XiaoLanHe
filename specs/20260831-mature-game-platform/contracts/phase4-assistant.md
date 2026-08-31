# Phase 4 Assistant Contract

The Assistant remains request-scoped and read-only. Router and Answer each run
as one bounded model node. Research is the only autonomous Agent.

Conversation context contains only history that predates the current request.
The current user message is persisted before model execution, but is passed to
Router/Answer exactly once through the dedicated message field.

## Conversation Identity

`sessionId` is optional. When absent, the server returns a UUIDv4 identifier;
when supplied, it must be a UUIDv4 value or the request returns `400
invalid_request` before storage or model work.

Authentication is optional for chat. A new authenticated conversation is bound
to the trusted session user's ID. An existing guest conversation may be claimed
by the first authenticated request that presents its unguessable key. After a
conversation is bound, only that user may continue it; another user or an
anonymous request receives `403 conversation_forbidden` without loading or
writing conversation messages.

## Tool Allowlist

| Tool | Input | Evidence source | Citation URL |
|---|---|---|---|
| `search_knowledge` | `query`, optional `gameCode`, `regionCode` | approved local guide chunks | stored HTTP(S) source URL when present |
| `search_catalog` | `query`, optional `region`, `currency` | active catalog games | `/api/games/{slug}` |
| `search_forum` | `query`, optional `gameId` | published community posts | `/api/community/posts/{id}` |
| `search_web` | `query` | configured public SearXNG provider | result HTTP(S) URL |

Knowledge and Web search queries are trimmed and must contain 1-100 Unicode
characters before they reach storage, embedding, or a public search provider.
Invalid input cannot trigger provider work.

`search_web` is registered only when Web Search is enabled. Unknown and
mutation-like names such as `create_order`, `claim_coupon`, `pay_order`, or
`create_post` are not registered and cannot reach an ordinary service.
The current Web adapter is SearXNG, so its provider identity is fixed to
`searxng`. The HTTP response does not expose cache metadata because no cache is
implemented.

Every tool returns a typed observation with `ok`, `no_result`, `invalid`, or
`failed` status. Provider failures are observations that the Agent may refine;
they are not successful empty searches. Request cancellation, total deadline,
per-tool deadline, model-iteration budget, and tool-call budget remain in force.
Deterministic tool-input errors are `invalid`, not provider failures, and still
consume one tool call from the bounded budget.

## Citations

Evidence retains source, title, content, and URL through Research and Answer.
The Answer Node deterministically appends a Markdown source list for valid
HTTP(S) URLs and local `/api/` resource URLs, including for SSE responses.
Unsafe schemes and credential-bearing URLs are omitted. The model is not trusted
to reproduce citations itself. If a successful model stream contains no text,
the SSE path emits the same explicit fallback reply as the REST path before EOF.

## Failure Semantics

- Successful search with no matches: `no_result` and an explicit note.
- Some successful tools and some failed tools: `partial`, evidence retained.
- Every called tool fails: request fails with `all research tools failed`.
- Tool or iteration budget reached: `bounded`; partial evidence is retained.
- Request cancellation or deadline: propagated to model, tools, and caller.
- Successful empty model stream: explicit fallback reply, never an empty
  assistant message.

Real model and public Web calls are rollout checks. Deterministic fake-model and
fake-tool tests are the pre-merge evidence.
