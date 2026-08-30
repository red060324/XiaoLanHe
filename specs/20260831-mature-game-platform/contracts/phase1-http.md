# Phase 1 HTTP Contract

This contract covers foundation, Account, and Catalog. JSON fields use
lower-camel-case. Unknown request fields are rejected. Request bodies are
bounded before decoding.

## Common Error

```json
{
  "error": {
    "code": "invalid_request",
    "message": "request is invalid",
    "requestId": "01J...",
    "fields": {
      "username": "must match [a-z0-9_]{3,32}"
    }
  }
}
```

`fields` is omitted when there is no safe field-level detail. Internal SQL,
provider, credential, and stack details never appear in the response.

| UseCase result | HTTP | Code |
|---|---:|---|
| malformed/invalid input | 400 | `invalid_request` |
| missing/invalid/expired session | 401 | `unauthenticated` |
| authenticated but unauthorized | 403 | `forbidden` |
| resource absent/hidden | 404 | `not_found` |
| duplicate username/slug or invalid state | 409 | `conflict` |
| required dependency unavailable | 503 | `dependency_unavailable` |
| deadline | 504 | `deadline_exceeded` |
| unexpected failure | 500 | `internal_error` |

Login always returns the same `401 invalid_credentials` response for an unknown
username, a wrong password, disabled account, or account without credentials.

## Request Identity And CSRF

- Request ID is accepted from a syntactically safe `X-Request-ID` or generated
  server-side, returned as `X-Request-ID`, and included in error responses.
- Auth cookie name: `xlh_session`.
- Cookie: HttpOnly, SameSite=Lax, Path=/, Max-Age=604800. Secure is mandatory
  outside an explicitly configured local HTTP environment.
- Cookie value is a 32-byte random base64url token. Only its SHA-256 digest is
  stored.
- Cookie-authenticated mutation requests with an `Origin` header must match the
  configured public origin. Requests without browser Origin still require a
  valid session and UseCase authorization.

## Account

Usernames are normalized to lowercase ASCII and must match
`[a-z0-9_]{3,32}`. Display names contain 1-64 Unicode runes after trimming.
Passwords contain 8-72 bytes and are never trimmed or logged.

### `POST /api/auth/register`

Request:

```json
{"username":"player_one","displayName":"Player One","password":"correct horse battery staple"}
```

Success: `201`, sets a fresh session cookie.

```json
{"user":{"id":"42","username":"player_one","displayName":"Player One","role":"user"}}
```

Duplicate normalized username returns `409 conflict` without revealing more
account data.

### `POST /api/auth/login`

Request:

```json
{"username":"player_one","password":"correct horse battery staple"}
```

Success: `200`, revokes the presented session if any and sets a fresh cookie.
Response body uses the same public user shape as registration.

### `POST /api/auth/logout`

Requires a valid session. Success: `204`, revokes it and clears the cookie.
Repeating logout without a valid session returns `204` and clears the cookie so
the operation is safely idempotent.

### `GET /api/me`

Success: `200` with the public user shape. Anonymous requests return `401`.

## Catalog Read

Public game shape:

```json
{
  "id": "100",
  "slug": "example-game",
  "name": "Example Game",
  "summary": "Short summary",
  "description": "Long description",
  "developer": "Studio",
  "publisher": "Publisher",
  "releaseDate": "2026-08-31",
  "coverUrl": "https://example.invalid/cover.jpg",
  "owned": false
}
```

`owned` is computed for an authenticated principal and is false for guests.
Inactive games/editions/prices are absent from public responses.

### `GET /api/games`

Query parameters:

- `query`: optional trimmed name/slug search, at most 100 runes;
- `cursor`: optional opaque cursor returned by the previous page;
- `limit`: default 20, range 1-50;
- `region`: optional uppercase region code, default `GLOBAL`;
- `currency`: optional uppercase ISO-like three-letter code, default `USD`.

Ordering is `id DESC`; the opaque cursor contains the last ID. Success:

```json
{
  "items": [{"id":"100","slug":"example-game","name":"Example Game","summary":"Short summary","coverUrl":"https://example.invalid/cover.jpg","owned":false}],
  "nextCursor": "MTAw"
}
```

`nextCursor` is omitted when no further row exists.

### `GET /api/games/{slug}`

Returns the game shape plus active editions and the selected regional price:

```json
{
  "game": {
    "id":"100",
    "slug":"example-game",
    "name":"Example Game",
    "summary":"Short summary",
    "description":"Long description",
    "developer":"Studio",
    "publisher":"Publisher",
    "releaseDate":"2026-08-31",
    "coverUrl":"https://example.invalid/cover.jpg",
    "owned":false,
    "editions":[{"id":"200","code":"standard","name":"Standard","price":{"amountMinor":5999,"currency":"USD","region":"GLOBAL"}}]
  }
}
```

## Catalog Admin

Both endpoints require `admin`. The aggregate request includes the complete
active edition/price set for phase one; updates are transactional.

### `POST /api/admin/games`

```json
{
  "slug":"example-game",
  "name":"Example Game",
  "summary":"Short summary",
  "description":"Long description",
  "developer":"Studio",
  "publisher":"Publisher",
  "releaseDate":"2026-08-31",
  "coverUrl":"https://example.invalid/cover.jpg",
  "editions":[{
    "code":"standard",
    "name":"Standard",
    "description":"Base game",
    "prices":[{"region":"GLOBAL","currency":"USD","amountMinor":5999}]
  }]
}
```

Success: `201` with the public detail representation.

### `PUT /api/admin/games/{id}`

Uses the same aggregate request. Success: `200`. Missing aggregate returns
`404`; slug collision returns `409`. An empty editions/prices list is valid for
an announced game that is not yet purchasable.

## Existing Endpoint Changes

- `POST /api/knowledge/documents` now requires `admin`.
- Read-only knowledge/search endpoints remain public in phase one.
- Existing successful chat REST/SSE response fields remain compatible.
- Existing chat/knowledge/search errors adopt the common error envelope in the
  same frontend/backend release.
