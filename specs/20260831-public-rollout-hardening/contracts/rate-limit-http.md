# Rate Limit HTTP Contract

## Rejection

Every protected endpoint uses the existing error envelope:

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 10
X-Request-ID: 7d5a8db31f5b4d6ba0304128972c7191

{
  "error": {
    "code": "rate_limited",
    "message": "too many requests; try again later",
    "requestId": "7d5a8db31f5b4d6ba0304128972c7191"
  }
}
```

`Retry-After` is a non-negative integer number of seconds. The body never
reveals whether the global, keyed, or in-flight guard rejected the request.
That reason is available only as a safe structured log field.

## Compatibility

- Existing success status codes and bodies do not change.
- Existing authentication/authorization and idempotency errors win when they
  occur before rate admission; a rate rejection never reaches business logic.
- REST and SSE use the same pre-stream rejection envelope. Once SSE headers and
  events begin, normal stream cancellation/error behavior remains unchanged.
- Clients may retry only after the advertised delay and must create a new HTTP
  request; the server does not queue rejected work.
