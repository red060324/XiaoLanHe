# Knowledge LightRAG HTTP Contract

- Status: `IMPLEMENTED`
- Authoritative spec: `../spec.md`

These XiaoLanHe administrator routes are a validated facade over official LightRAG.
All routes require an authenticated administrator; mutations also require same-origin
validation. Responses never expose LightRAG base URL, API key, workspace, raw errors,
internal metadata or response bodies.

This deliberately migrates the existing synchronous numeric-document contract when
advanced mode is enabled: LightRAG ingestion is asynchronous and owns string document
IDs. No local ID mapping is stored.

## `GET /api/knowledge/search`

The existing query parameters remain bounded. In advanced mode the endpoint calls
LightRAG `/query/data`; it does not query `knowledge_document` or `knowledge_chunk`.
Because LightRAG owns string reference identifiers, the response intentionally moves
from numeric `chunkId`/`documentId` fields to provider-neutral evidence:

```json
{
  "query": "co-op RPG",
  "provider": "lightrag",
  "mode": "mix",
  "items": [
    {
      "evidenceId": "ev_01...",
      "kind": "chunk",
      "text": "...",
      "sourceKey": "xlh-8de...64-lowercase-hex.txt",
      "referenceId": "1"
    }
  ]
}
```

Official `/query/data` references provide a bounded `reference_id` and `file_path`,
not an original source URL. `sourceKey` is therefore provenance text, not a clickable
link; XiaoLanHe never fabricates an admin/document URL from it.

`evidenceId` is request-local and cannot be used for document mutation. Entity and
relationship items use the same envelope with bounded typed attributes. In disabled
mode the legacy endpoint may return `provider=legacy_local` through this same new
envelope during migration; it is never relabeled as LightRAG.

## `POST /api/knowledge/documents`

The existing request fields remain accepted and strictly capped. Go canonicalizes
the validated metadata plus content, generates a SHA-256 `sourceKey`, encodes the
metadata header and calls LightRAG
`POST /documents/text`. On acceptance it returns `202`:

```json
{
  "trackId": "insert_20260904_100000_abcd",
  "sourceKey": "xlh-8de...64-lowercase-hex.txt",
  "status": "accepted"
}
```

`trackId` and `sourceKey` are opaque strings with response/request caps. The endpoint
does not return `chunkCount` or a fabricated local `documentId`.

## `GET /api/admin/knowledge/tracks/{trackId}`

Maps official track status and returns `200`:

```json
{
  "trackId": "insert_20260904_100000_abcd",
  "documents": [
    {
      "documentId": "doc-07c0...",
      "sourceKey": "xlh-8de...64-lowercase-hex.txt",
      "status": "PROCESSED",
      "contentLength": 2048,
      "chunksCount": 4,
      "createdAt": "2026-09-04T10:00:00Z",
      "updatedAt": "2026-09-04T10:00:08Z",
      "failureCode": null
    }
  ],
  "totalCount": 1,
  "statusCounts": {"PROCESSED": 1}
}
```

Raw LightRAG error text and arbitrary metadata are reduced to safe failure codes.

## `GET /api/admin/knowledge/documents`

Query parameters: `page` (>=1), `pageSize` (10-100), optional bounded status, and
`sortField=createdAt|updatedAt|documentId|sourceKey` with `asc|desc`. The Go adapter
maps them to `/documents/paginated`. Only managed `xlh-*.txt` and
`xlh-legacy-*.txt` source namespaces are returned.

```json
{
  "items": [
    {
      "documentId": "doc-07c0...",
      "sourceKey": "xlh-8de...64-lowercase-hex.txt",
      "status": "PROCESSED",
      "contentLength": 2048,
      "chunksCount": 4,
      "createdAt": "2026-09-04T10:00:00Z",
      "updatedAt": "2026-09-04T10:00:08Z",
      "failureCode": null
    }
  ],
  "page": 1,
  "pageSize": 20,
  "totalCount": 1,
  "totalPages": 1
}
```

## `DELETE /api/admin/knowledge/documents/{documentId}`

Calls the exact LightRAG deletion endpoint for one validated opaque ID, with
`delete_file=false` and `delete_llm_cache=false`. Returns `202` when deletion starts:

```json
{
  "documentId": "doc-07c0...",
  "status": "deletion_started"
}
```

There is no clear-all endpoint. Replacement is delete, confirm absence, then create.

## Errors

| Condition | HTTP | Code |
|---|---:|---|
| malformed/capped input or identifier | 400 | `invalid_request` |
| missing/expired session | 401 | `unauthenticated` |
| non-admin or same-origin failure | 403 | `forbidden` |
| missing track/document | 404 | `not_found` |
| source or pipeline conflict/busy | 409 | `knowledge_conflict` |
| LightRAG pending capacity reached | 429 | `capacity_exceeded` |
| upstream schema/version mismatch | 502 | `dependency_contract_error` |
| LightRAG unavailable/deadline/ambiguous write | 503/504 | shared dependency/deadline code |

An ambiguous write response includes the safe deterministic `sourceKey` but makes no
claim that ingestion failed or succeeded. A same-payload replay derives the same key;
on upstream 409 the facade performs one bounded source lookup. Exactly one managed
match returns its current status with `replayed=true`; no match or multiple matches
returns a conflict/unknown result rather than creating a new key.

## Private Upstream Allowlist

| Owner | Purpose | LightRAG endpoint |
|---|---|---|
| health adapter | authentication contract | `GET /auth/verify` |
| query adapter | readiness | `GET /health` |
| health adapter | recovery/pipeline contract | `GET /documents/pipeline_status` |
| query adapter | structured retrieval | `POST /query/data` |
| admin adapter | text creation | `POST /documents/text` |
| admin adapter | track status | `GET /documents/track_status/{track_id}` |
| admin adapter | bounded list | `POST /documents/paginated` |
| admin adapter | exact deletion | `DELETE /documents/delete_document` |

Clear-all, cache clear, force recovery, graph edit, arbitrary upload, directory scan,
failed-batch reprocess and source-conflict repair are not implemented in Go.
The health adapter accepts `/auth/verify` only with `{"status":"ok"}` and rejects
startup/readiness unless authenticated `/health` also reports the pinned
Gunicorn/two-worker topology. Deployment smoke separately asserts that unauthenticated
`/health` is liveness-only and protected endpoints reject missing or wrong keys.
