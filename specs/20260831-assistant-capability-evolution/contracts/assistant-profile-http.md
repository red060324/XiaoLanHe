# Assistant Profile HTTP Contract

- Status: `DRAFT`
- Authoritative spec: `../spec.md`

All routes require `xlh_session`. Mutations also require the existing
same-origin policy. Responses use the repository-wide error envelope and never
return conversation summaries, credentials, sessions, or entitlements.

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
    "updatedAt": "2026-08-31T10:00:00Z"
  }
}
```

An authenticated user without a profile receives the same shape with empty
lists/strings, omitted price fields, and omitted `updatedAt`.

## `PUT /api/me/assistant-profile`

Replaces the complete profile and returns `200` with the stored representation.
Unknown fields are rejected.

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

- list values are trimmed, case-normalized where applicable, unique, and keep
  user order;
- genres/platforms: at most 10 values, each 1-32 Unicode characters;
- languages: at most 5 values, each 2-16 safe ASCII characters;
- region: empty or 2-16 uppercase ASCII letters/digits/`_-`;
- price: `1..1,000,000,000` minor units and a three-letter uppercase currency;
  price and currency are both present or both absent.

## `DELETE /api/me/assistant-profile`

Returns `204` and is idempotent. It clears explicit preferences only. It does
not delete the account, conversations, posts, orders, or entitlements.

## Errors

| Condition | HTTP | Code |
|---|---:|---|
| malformed/invalid/unknown field | 400 | `invalid_request` |
| missing/expired session | 401 | `unauthenticated` |
| same-origin failure | 403 | `forbidden` |
| storage unavailable | 503 | `dependency_unavailable` |
