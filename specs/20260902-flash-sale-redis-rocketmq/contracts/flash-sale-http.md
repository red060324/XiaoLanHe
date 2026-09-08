# Flash-Sale HTTP Contract

- Status: APPROVED (2026-09-03)
- Base content type: application/json
- Authentication: required for reservation and request status; authenticated admin
  role required for every admin read and mutation
- Origin protection: reservation and admin mutations use the existing same-origin
  policy; admin GET list/detail require only authentication plus admin role

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
draft activities, `totalStock`, `paymentTimeoutSeconds`, per-user markers, total
allocated count, Redis keys, queue lag, or infrastructure details. Public detail
returns 404 for a draft.

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

Reserve processing order is normative:

1. derive the request ID and idempotency digest and return an authoritative exact
   replay from MySQL when present;
2. after a durable miss, validate that the activity exists before consulting Redis;
3. return a normal Redis exact replay when present; only a pre-durable Redis
   `failed/technical_rollback` marker falls through, and its replacement must have a
   strictly newer reservation timestamp so delayed release work cannot undo it;
4. if either durable storage or Redis has another reservation for the same
   activity/user, return `already_reserved`;
5. only for a wholly new request, perform the edition ownership precheck and
   return `already_owned` when applicable; and
6. only then enter Redis/RocketMQ admission.

Normal exact replay takes precedence over later ownership. These HTTP checks do not
replace the consumer and Order layer's final ownership, unique request/source,
and final-stock guards.

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
only in Redis; durable MySQL state becomes authoritative once present.

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
GET /api/admin/flash-sales?cursor=&limit=20
GET /api/admin/flash-sales/{activityId}
POST /api/admin/flash-sales
PUT /api/admin/flash-sales/{activityId}
POST /api/admin/flash-sales/{activityId}/activate
POST /api/admin/flash-sales/{activityId}/cancel
~~~

Admin list/detail include draft activities and use the public activity shape plus
the management fields below. List returns `{"items":[...],"nextCursor":"..."}`
and detail returns `{"flashSale":{...}}`.

~~~json
{
  "id": "41",
  "code": "AUTUMN-DELUXE",
  "status": "draft",
  "totalStock": 100,
  "paymentTimeoutSeconds": 900
}
~~~

Admin GET routes require an authenticated admin role and do not require an
Origin match. All four mutations below retain authenticated admin role and
same-origin enforcement.

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
The account-page admin UI consumes these endpoints, offers edit only for drafts,
and exposes activate/cancel only when the current activity state permits them.
