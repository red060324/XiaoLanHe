# Assistant Profile HTTP Contract

- Status: `IMPLEMENTED`
- Authoritative spec: `../spec.md`

All routes require the existing `xlh_session` cookie. Mutations also require the
existing same-origin policy. Responses use the repository-wide error envelope and
never include conversations, summaries, entitlements, credentials or sessions.

## `GET /api/me/assistant-profile`

Returns `200`:

```json
{
  "profile": {
    "favoriteGenres": ["rpg", "strategy"],
    "preferredPlatforms": ["pc"],
    "defaultRegion": "GLOBAL",
    "preferredLanguages": ["zh-CN"],
    "maxPriceMinor": 30000,
    "currency": "CNY",
    "updatedAt": "2026-09-04T10:00:00Z"
  }
}
```

An authenticated user without an Assistant profile receives the same object with
empty lists/strings and omitted price, currency and `updatedAt`.

## `PUT /api/me/assistant-profile`

Replaces only the Assistant-owned profile namespace and returns `200` with the
stored representation. Unknown fields are rejected.

```json
{
  "favoriteGenres": ["rpg", "strategy"],
  "preferredPlatforms": ["pc"],
  "defaultRegion": "GLOBAL",
  "preferredLanguages": ["zh-CN"],
  "maxPriceMinor": 30000,
  "currency": "CNY"
}
```

Validation:

- list values are trimmed, normalized where applicable, unique and keep user order;
- genres/platforms: at most 10 values, each 1-32 Unicode characters;
- languages: at most 5 values, each 2-16 safe ASCII characters;
- region: empty or 2-16 uppercase ASCII letters/digits/`_-`;
- price: `1..1,000,000,000` minor units and a three-letter uppercase currency;
- price and currency must both be present or both absent;
- the encoded Assistant namespace is capped at 4 KiB.

## `DELETE /api/me/assistant-profile`

Returns `204` and is idempotent. It clears Assistant preferences and default region
only. It does not delete unrelated profile namespaces, account, conversations,
posts, orders, entitlements or knowledge.

## Errors

| Condition | HTTP | Code |
|---|---:|---|
| malformed, invalid, unknown field or body too large | 400 | `invalid_request` |
| missing or expired session | 401 | `unauthenticated` |
| same-origin failure | 403 | `forbidden` |
| storage unavailable or request deadline | 503/504 | existing shared dependency/deadline code |
