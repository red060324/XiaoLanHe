# Technical Plan

- Status: `DRAFT`
- Authoritative spec: `./spec.md`

## Selected Approach

Add `internal/platform/httplimit`, a small HTTP-boundary guard backed by
`golang.org/x/time/rate`. One guard owns three named policies. Each policy has
a process-wide bucket and a bounded map of hashed caller-key buckets. Cleanup
is opportunistic during checks; no goroutine or external store is added.

```text
HTTP Entry
  -> decode/validate the minimum identity input
  -> httplimit.Check(scope, trusted key)
       -> global token bucket
       -> bounded per-key token bucket when key exists
       -> optional shared provider semaphore
  -> existing Presenter / UseCase / Entity / Repository flow
```

The guard is transport policy, not domain behavior. UseCases and Entities do
not import it. `cmd/xiaolanhe` constructs one guard from validated config and
passes the concrete capability to affected Entries; no one-implementation
interface or registry framework is added.

## Endpoint Mapping

| Scope | Endpoints | Caller key | Guard point |
|---|---|---|---|
| auth | register, login | normalized syntactically valid username | after bounded JSON decode, before Account UseCase |
| expensive | chat REST/SSE | validated session ID, or global-only when empty | after request validation, before conversation/model work |
| expensive | knowledge search, Web Search | global-only | after query validation, before embedding/provider work |
| write | community/admin mutations | Principal user ID | after authentication, before UseCase |
| write | coupon claim, order create/pay | Principal user ID | after authentication, before UseCase |
| write | catalog/knowledge admin mutations | Principal user ID | after admin authentication, before UseCase |

Successful login removes the username bucket so prior failed attempts do not
penalize the authenticated user. Invalid request bodies fail normally and do
not allocate per-key state.

## Error And Concurrency Semantics

- `Check` evaluates the global bucket first, then an existing/new keyed bucket.
- Rejection returns a decision with a bounded integer retry delay. Entry writes
  `Retry-After` and `rate_limited`; it does not call downstream code.
- Expensive provider calls acquire one of four shared permits after rate
  admission. Failure to acquire immediately returns 429 rather than queueing
  requests that may outlive the caller.
- REST releases the permit on handler return. SSE holds it until stream
  completion, provider failure, or client disconnect and releases exactly once.
- Context cancellation never consumes a waiting goroutine because acquisition
  is non-blocking.

## Memory And Trust Boundaries

- Raw keys are SHA-256 hashed with the scope before map insertion.
- The map is protected by one mutex; each `rate.Limiter` is concurrency-safe.
- At most 10,000 keyed entries exist. Once per minute, a request may remove
  entries idle for 15 minutes. This is O(n) but bounded and off the common path.
- If capacity remains full, no new key entry is created; only the global bucket
  decides admission.
- No IP or forwarding header participates in identity. This avoids depending on
  undocumented proxy chains or Hertz's trust-all default.

## Configuration

Add strictly positive integer settings:

| Variable | Default | Range |
|---|---:|---:|
| `XLH_AUTH_RATE_PER_MINUTE` | 5 | 1-600 |
| `XLH_EXPENSIVE_RATE_PER_MINUTE` | 6 | 1-600 |
| `XLH_WRITE_RATE_PER_MINUTE` | 30 | 1-6000 |
| `XLH_EXPENSIVE_MAX_IN_FLIGHT` | 4 | 1-100 |
| `XLH_RATE_LIMIT_MAX_KEYS` | 10000 | 100-100000 |

Process-wide rates are fixed at 20 times each per-key rate, with the same upper
bound multiplication checked for overflow. This keeps the public config small
while retaining a rotating-key backstop.

## Public Contract

See `contracts/rate-limit-http.md`. Existing success responses and idempotency
contracts do not change. Frontend fetch handling already displays the shared
error message, so no new state library or retry loop is planned.

## Observability

Emit one structured warning on rejection with request ID, scope,
`global|keyed|in_flight`, retry seconds, operation, and result. Emit no raw key,
username, session, IP, cookie, prompt, message, or token. A capacity-fallback
warning is rate-limited by the same cleanup interval.

## Rollout And Rollback

1. Deploy with defaults to an isolated single-instance environment.
2. Verify 429/`Retry-After`, downstream suppression, permit release, ordinary
   user flows, and safe logs.
3. Configure model/search provider spending limits and billing alerts.
4. Roll back the application revision; there is no schema migration or durable
   limiter state.

Horizontal scaling is not approved by this spec. Before adding a second app
instance, move admission control to a trusted edge or shared open-source store
and prove the same contract.

## Rejected Alternatives

- PostgreSQL counters: turns abusive traffic into write load on the primary
  database and requires retention/migration work.
- Redis/Valkey now: a new service is unnecessary for the approved one-instance
  topology.
- Raw `X-Forwarded-For`/Hertz `ClientIP`: proxy trust is not configured and the
  default accepts forwarded headers from all CIDRs.
- Permanent account lock: enables account-denial attacks and requires recovery
  behavior outside this slice.
- Custom token-bucket math: the public Go supplementary package already owns
  the concurrency and refill edge cases.

## Traceability

| Planned change | Owner | Requirement |
|---|---|---|
| bounded token buckets and cleanup | platform/httplimit | AC1, AC2, AC5 |
| 429 and Retry-After | HTTP Entries/httpx | AC3 |
| chat session validation | presenter/chat | AC4 |
| endpoint wiring | module Entries + cmd | AC1, AC7 |
| strict settings | config | AC6 |
| tests/docs/CI | repository harness | AC8 |
