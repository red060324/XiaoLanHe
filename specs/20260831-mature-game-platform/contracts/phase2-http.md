# Phase 2 Community HTTP Contract

All mutation routes require the `xlh_session` cookie and the same-origin policy
defined by Phase 1. IDs are decimal strings. Timestamps are RFC 3339 UTC.
Errors use the shared `{ "error": { "code", "message", "requestId" } }`
envelope.

## Posts

`GET /api/community/posts?gameId=&cursor=&limit=` returns published posts in
descending `(createdAt,id)` order:

```json
{
  "items": [{
    "id": "41",
    "title": "新手路线",
    "content": "先完成教学任务。",
    "status": "published",
    "author": {"id": "7", "username": "player", "displayName": "玩家"},
    "game": {"id": "3", "slug": "phase-game", "name": "Phase Game"},
    "commentCount": 2,
    "reactionCounts": {"like": 3, "helpful": 1, "funny": 0},
    "viewerReactions": ["like"],
    "createdAt": "2026-08-31T10:00:00Z",
    "updatedAt": "2026-08-31T10:00:00Z"
  }],
  "nextCursor": "opaque"
}
```

`GET /api/community/posts/{id}` returns `{ "post": ... }`. Hidden and deleted
posts return `404` on this public route.

`POST /api/community/posts` creates a post and returns `201`:

```json
{"gameId":"3","title":"新手路线","content":"先完成教学任务。"}
```

`gameId` is optional. A supplied game must exist. `PUT
/api/community/posts/{id}` uses the same full body and is allowed only for the
author. `DELETE /api/community/posts/{id}` soft-deletes an authored post and
returns `204`.

Title is 1–160 Unicode characters after trimming; content is 1–10,000.

## Comments

`GET /api/community/posts/{postId}/comments?cursor=&limit=` returns published
comments in ascending `(createdAt,id)` order:

```json
{
  "items": [{
    "id":"9",
    "postId":"41",
    "content":"有帮助，谢谢！",
    "status":"published",
    "author":{"id":"8","username":"reader","displayName":"读者"},
    "createdAt":"2026-08-31T10:01:00Z",
    "updatedAt":"2026-08-31T10:01:00Z"
  }],
  "nextCursor":"opaque"
}
```

`POST /api/community/posts/{postId}/comments` accepts `{ "content": "..." }`
and returns `201`. `PUT /api/community/comments/{id}` accepts the same body.
Only the author may update or soft-delete a comment. Comment content is
1–3,000 Unicode characters after trimming.

## Reactions

`PUT /api/community/posts/{postId}/reactions/{type}` is idempotent. `DELETE`
removes that reaction idempotently. Allowed types are `like`, `helpful`, and
`funny`. Both return the current summary:

```json
{
  "reactionCounts":{"like":3,"helpful":1,"funny":0},
  "viewerReactions":["like"]
}
```

The database permits at most one row per `(post,user,reaction_type)`.

## Moderation

Admins may set `published` or `hidden` with:

- `PUT /api/admin/community/posts/{id}/status`
- `PUT /api/admin/community/comments/{id}/status`

Body: `{ "status": "hidden" }`. Hidden content is excluded from public feeds;
moderation never hard-deletes rows.

## Status Mapping

| Condition | HTTP | Code |
|---|---:|---|
| malformed body, ID, cursor, limit, content, reaction, status | 400 | `invalid_request` |
| missing/invalid session | 401 | `unauthenticated` |
| wrong owner or non-admin moderation | 403 | `forbidden` |
| missing/hidden/deleted post or missing comment | 404 | `not_found` |
| supplied game does not exist | 404 | `game_not_found` |
| database/auth dependency failure | 503 | `dependency_unavailable` |
