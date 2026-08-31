# Phase 3 HTTP Contract

All IDs are decimal strings. Money is an integer number of minor currency
units. Authenticated mutations require the session cookie, the repository's
same-origin policy, and the stated idempotency header. Errors use the repository-wide
`{"error":{"code","message","requestId"}}` envelope.

## Deals And Claims

### `GET /api/deals?gameId=&cursor=&limit=`

Public. Authentication is optional and only adds `viewerClaimCount`. Returns
campaigns whose status and time window currently permit claims. Exhausted deals
remain visible with `remainingStock: 0`.

```json
{
  "items": [{
    "id": "21",
    "code": "WELCOME20",
    "name": "Welcome discount",
    "discountType": "percentage",
    "percentageBps": 2000,
    "currency": "USD",
    "minimumMinor": 1000,
    "remainingStock": 98,
    "perUserLimit": 1,
    "gameId": "7",
    "editionId": "12",
    "startsAt": "2026-08-31T00:00:00Z",
    "endsAt": "2026-09-30T00:00:00Z",
    "viewerClaimCount": 0
  }],
  "nextCursor": "MjE"
}
```

`gameId` includes global deals plus deals scoped to that game. Pagination is a
stable descending coupon-ID cursor. Default limit is 20; maximum is 50.

### `POST /api/coupons/{code}/claims`

Authenticated. Requires `Idempotency-Key` containing 8-128 visible
alphanumeric/`._:-` characters and an empty body. Returns `201` for a new
claim and `200` when the same user replays the same key for the same coupon.

```json
{
  "claim": {
    "id": "91",
    "couponCode": "WELCOME20",
    "status": "claimed",
    "claimedAt": "2026-08-31T08:00:00Z"
  },
  "replayed": false
}
```

The key is scoped to the user, not the coupon. Reusing it for another coupon is
`409 idempotency_conflict`. Inactive/not-started/expired coupons return
`409 coupon_unavailable`; final-stock contention returns `409 coupon_exhausted`;
the per-user limit returns `409 claim_limit_reached`; an unknown code returns
`404 coupon_not_found`.

### `GET /api/coupon-claims?cursor=&limit=`

Authenticated. Returns the current user's unredeemed claims whose campaign is
still active and which are not already attached to an order. The response uses
the same claim shape as the claim mutation:

```json
{
  "items": [{
    "id": "91",
    "couponCode": "WELCOME20",
    "status": "claimed",
    "claimedAt": "2026-08-31T08:00:00Z"
  }],
  "nextCursor": "OTE"
}
```

Pagination is a stable descending claim-ID cursor. Default limit is 20;
maximum is 50. A claim disappears from this list as soon as an order reserves
it, preventing the browser from offering the same claim twice.

## Orders

These endpoints are implemented by T11 after coupon claims pass concurrency CI.

### `POST /api/orders`

Authenticated. Requires `Idempotency-Key`. Body:

```json
{"editionId":"12","region":"GLOBAL","currency":"USD","couponClaimId":"91"}
```

Unknown fields are rejected. The server reads the active catalog price, quotes
the coupon, and snapshots both. New order is `201`; replay is `200`.
Replaying the same key does not depend on the current catalog price. Reusing the
key with a different edition, region, currency, or coupon claim returns
`409 idempotency_conflict`.

```json
{
  "order": {
    "orderNo": "ord_4f8a6f...",
    "status": "pending_payment",
    "currency": "USD",
    "subtotalMinor": 1999,
    "discountMinor": 399,
    "totalMinor": 1600,
    "couponClaimId": "91",
    "item": {
      "editionId": "12",
      "gameSlug": "xiaolanhe-demo",
      "gameName": "小蓝盒 Demo",
      "editionCode": "standard",
      "editionName": "标准版",
      "unitPriceMinor": 1999,
      "region": "GLOBAL"
    },
    "createdAt": "2026-08-31T08:00:00Z",
    "updatedAt": "2026-08-31T08:00:00Z"
  },
  "replayed": false
}
```

### `GET /api/orders?cursor=&limit=`

Authenticated stable owner history. Admin access does not broaden this list.
Returns `{"items":[<order>],"nextCursor":"..."}` using descending
`(created_at,id)` keyset pagination. Default limit is 20; maximum is 50.

### `GET /api/orders/{orderNo}`

Authenticated owner or admin detail.

### `POST /api/orders/{orderNo}/payments/sandbox`

Authenticated owner. Requires `Idempotency-Key` and an empty body. It confirms
only the repository's sandbox payment: one payment row, coupon redemption, and
entitlement are committed atomically. New confirmation is `200`; replay returns
the same paid order with `"replayed": true`. A different payment key after the
order is paid returns `409 invalid_order_state`. No financial provider is
contacted.

If multiple pending orders exist for the same user and edition, at most one can
complete payment. Concurrent confirmations yield one paid order; the other
returns `409 already_owned`, stays pending, and records no payment.

Order errors distinguish `order_not_found`, `price_unavailable`,
`coupon_ineligible`, `already_owned`, `invalid_order_state`, and `forbidden`
without exposing SQL or provider details.
