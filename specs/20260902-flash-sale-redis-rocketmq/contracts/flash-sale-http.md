# Flash-Sale HTTP Contract

- Status: APPROVED (2026-09-03)
- Base content type: application/json
- Authentication: required for reservation and request status; admin role required
  for activity mutations
- Mutation protection: existing same-origin policy plus authenticated session

IDs are JSON strings. Timestamps are UTC RFC3339Nano. Money is integer minor
units. Every error uses the repository's standard envelope and request ID.

## Public Activity Read

~~~http
GET /api/flash-sales?cursor=&limit=20
GET /api/flash-sales/{activityId}
~~~

~~~json
{
  "id": "41",
  "code": "AUTUMN-DELUXE",
  "gameSlug": "example-game",
  "gameName": "Example Game",
  "editionId": "7",
  "editionName": "Deluxe",
  "region": "CN",
  "currency": "CNY",
  "salePriceMinor": 9900,
  "status": "active",
  "startsAt": "2026-09-02T12:00:00Z",
  "endsAt": "2026-09-02T13:00:00Z",
  "availability": "available"
}
~~~

availability is upcoming, available, exhausted, ended, cancelled, or unavailable.
It is not an exact remaining-stock promise. Public responses do not expose
per-user markers, total allocated count, Redis keys, queue lag, or infrastructure
details.

## Reserve One Unit

~~~http
POST /api/flash-sales/{activityId}/reservations
Idempotency-Key: 8-128 bounded visible ASCII characters
Content-Length: 0
~~~

New acceptance returns 202 Accepted; an exact replay returns 200 OK or the same
202 while still queued, with replayed=true. Both use the same request ID.

~~~json
{
  "request": {
    "requestId": "fsr_15_0123456789abcdef0123456789abcdef",
    "activityId": "41",
    "status": "queued",
    "orderNo": "",
    "failureCode": "",
    "paymentExpiresAt": ""
  },
  "replayed": false
}
~~~

The endpoint accepts no quantity, user ID, price, currency, coupon, status, or
order number from the browser. Queue acceptance is not presented as an order.

Business errors:

| HTTP | Code | Meaning |
|---:|---|---|
| 400 | invalid_request | invalid activity ID/body/idempotency key |
| 401 | unauthenticated | login required |
| 404 | flash_sale_not_found | no visible activity |
| 409 | flash_sale_not_started | window not open |
| 409 | flash_sale_ended | ended or cancelled |
| 409 | stock_exhausted | no unit admitted |
| 409 | already_reserved | same user used a different idempotency key |
| 409 | already_owned | user already owns the edition |
| 503 | flash_sale_unavailable | fail-closed Redis/MQ/config uncertainty |

Dependency errors never disclose which private host, key, topic, or broker failed.

## Poll Request

~~~http
GET /api/flash-sale-requests/{requestId}
~~~

Only the owner or an admin may read it. A queued request may temporarily exist
only in Redis; durable PostgreSQL state becomes authoritative once present.

~~~json
{
  "request": {
    "requestId": "fsr_15_0123456789abcdef0123456789abcdef",
    "activityId": "41",
    "status": "order_ready",
    "orderNo": "ord_0123456789abcdef0123456789abcdef",
    "failureCode": "",
    "paymentExpiresAt": "2026-09-02T12:15:00Z"
  }
}
~~~

Status values are:

- queued: Redis accepted and transport/durable consumption is pending;
- processing: durable reservation exists and order creation is retrying;
- order_ready: order exists and may be paid before its deadline;
- failed: terminal safe failure such as final-stock guard or already owned;
- expired: the unpaid order/allocation expired.

An unknown or non-owned request returns 404 to avoid ownership disclosure.

## Admin Activity APIs

~~~http
POST /api/admin/flash-sales
PUT /api/admin/flash-sales/{activityId}
POST /api/admin/flash-sales/{activityId}/activate
POST /api/admin/flash-sales/{activityId}/cancel
~~~

Create/update draft request:

~~~json
{
  "code": "AUTUMN-DELUXE",
  "editionId": "7",
  "region": "CN",
  "currency": "CNY",
  "salePriceMinor": 9900,
  "totalStock": 100,
  "startsAt": "2026-09-02T12:00:00Z",
  "endsAt": "2026-09-02T13:00:00Z",
  "paymentTimeoutSeconds": 900
}
~~~

Activation/cancellation bodies are empty and idempotent. Only drafts are editable.
Activation returns success only after the durable row is active and Redis admission
has been enabled. An interrupted partial activation remains fail closed and can be
retried. Cancellation stops new admissions; it does not revoke paid orders or
silently delete accepted requests.
