# Research

- Status: `DRAFT`
- Date: 2026-08-31
- Scope: public sources and current repository code only

## Public Guidance

- OWASP recommends login throttling and warns that account lockout needs a
  security/usability balance to avoid denial of service:
  <https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html#login-throttling>.
- OWASP API4:2023 identifies unrestricted provider/compute consumption and
  recommends endpoint-specific rate limits and provider spending limits:
  <https://owasp.org/API-Security/editions/2023/en/0xa4-unrestricted-resource-consumption/>.
- HTTP 429 is the standard response for excessive request frequency, and
  `Retry-After` communicates the retry delay:
  <https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Status/429>.
- `golang.org/x/time/rate` is the public Go supplementary token-bucket
  implementation and is safe for simultaneous goroutines:
  <https://pkg.go.dev/golang.org/x/time/rate>.
- Hertz v0.10.6 defaults `ClientIP` trusted CIDRs to all IPv4/IPv6 networks and
  reads forwarding headers. The current deployment has no trusted-proxy CIDR
  contract, so caller security must not depend on that default:
  <https://github.com/cloudwego/hertz/blob/v0.10.6/pkg/app/context.go>.

## Current Code Evidence

- `internal/account/entry/http.go` calls register/login without admission
  control.
- `internal/entry/http.go` exposes anonymous chat, embedding-backed knowledge
  search, and optional Web Search without admission or shared concurrency
  limits.
- Community, catalog admin, knowledge admin, promotion, and order mutation
  Entries authenticate and already expose a trusted Principal suitable for a
  user-keyed guard.
- `internal/presenter/chat.go` bounds message length but not `sessionId`;
  `migrations/001_initial_schema.sql` stores `session_key` as `varchar(64)`.
- Agent time/tool/iteration budgets already bound one request. They do not
  limit the number of concurrent or repeated requests and therefore do not
  solve API-level resource consumption.

## Alternatives

| Option | Result |
|---|---|
| process-local token bucket | selected for current single-instance topology; smallest public dependency and no schema/service |
| PostgreSQL quota rows | rejected now; abusive requests become database writes and add cleanup/migration risk |
| Redis/Valkey | defer until multi-instance scaling makes shared state necessary |
| IP-only limits | rejected; proxy identity is not safely configured and distributed attacks bypass it |
| permanent account lock | rejected; creates an account-denial primitive without recovery |
| provider budgets only | required at rollout but insufficient for database/CPU and non-provider endpoints |
