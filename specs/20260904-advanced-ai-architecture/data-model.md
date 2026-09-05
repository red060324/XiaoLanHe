# Data Model

- Status: `IMPLEMENTED — DATABASE VERIFIED; LIGHTRAG RESTORE BLOCKED`
- Authoritative spec: `./spec.md`
- XiaoLanHe migration: `007_advanced_ai.sql`
- Knowledge-data owner in advanced mode: pinned official LightRAG server

Migration 007 is additive and applies only to XiaoLanHe's business PostgreSQL.
Existing migrations 001-006 remain immutable. It creates no LightRAG document,
mapping, projection, chunk, vector, graph or status table.

## Conversation Summary

Add to `conversation_session`:

| Column | Type | Rule |
|---|---|---|
| `summary_text` | `text` | nullable; UseCase caps at 2,000 Unicode characters |
| `summary_through_message_id` | `bigint` | nullable FK to message, `ON DELETE SET NULL` |
| `summary_prompt_version` | `varchar(64)` | nullable validated version |
| `summary_updated_at` | `timestamptz` | nullable |

Compare-and-set accepts a watermark only when the message belongs to the same session
and is greater than the stored value. Summary, watermark, prompt version and time
update atomically. Conversation deletion removes its summary.

## Explicit Assistant Profile

Reuse `player_profile`. Migration 007 detects duplicate non-null `user_id` rows and
fails clearly, then creates a unique partial index. Assistant-owned data is namespaced
inside `preferences`:

```json
{
  "assistant": {
    "favoriteGenres": ["rpg"],
    "preferredPlatforms": ["pc"],
    "preferredLanguages": ["zh-CN"],
    "maxPriceMinor": 30000,
    "currency": "CNY"
  }
}
```

`default_region` stores the validated region. Replace touches only the Assistant
namespace and preserves unrelated profile data. Clear removes that namespace and
default region; the row is deleted only when no unrelated data remains. Entitlements
remain the sole ownership source.

## LightRAG-Owned Knowledge Data

Advanced-mode knowledge data has no XiaoLanHe relational representation. LightRAG's
HTTP contract owns the identifiers and lifecycle:

| Concept | Owner/shape |
|---|---|
| source key | Go generates `xlh-<sha256(canonical metadata + content)>.txt`; stored by LightRAG as `file_path` |
| create operation | LightRAG `track_id` returned asynchronously |
| document ID | LightRAG string `doc_id`, discovered through track/list responses |
| processing state | LightRAG `DocStatus` and bounded safe error projection |
| content/metadata | `XiaoLanHe-Knowledge-v1` header plus body in LightRAG full-doc storage |
| chunks/vectors/entities/relations | derived and owned only by LightRAG |

The Go facade does not persist source-key, track-ID or document-ID mappings. Clients
retain `trackId` until terminal status and then use returned `documentId` for exact
deletion. Operators can recover identifiers from the paginated document list.

The metadata envelope prepended to text is deterministic and bounded:

```text
XiaoLanHe-Knowledge-v1
Title: ...
Source-Type: ...
Source-URL: ...
Game-Code: ...
Region-Code: ...
Patch-Version: ...

<content text>
```

Header values reject control characters and receive field-specific size caps. This
envelope is searchable content, not an authorization source.

## LightRAG Native Workspace

The pinned server initializes one workspace under the mounted `WORKING_DIR` using:

- `JsonKVStorage`;
- `NanoVectorDBStorage`;
- `NetworkXStorage`;
- `JsonDocStatusStorage`;
- `WORKSPACE=xiaolanhe_v1`.

The upstream workspace file layout is deliberately not copied into this repository's
schema contract. All files in the workspace are one consistency unit for backup and
restore. The stores are loaded into process memory and local files are persistence,
so the deployment is limited to one owner replica and a measured small corpus.

## Retention, Deletion And Backup

- Summaries live with conversations; Assistant profiles live until explicit clear or
  account deletion.
- LightRAG source and derived records live until exact document deletion or whole
  workspace retirement. Delete is asynchronous and must be confirmed by later list/
  query checks.
- Rollback retains the LightRAG volume and legacy PostgreSQL knowledge rows.
- A consistent LightRAG backup stops mutations, drains the pipeline, cleanly stops the
  only owner and snapshots the entire workspace volume plus versioned runtime config.
- Restore targets an empty volume and the same image, embedding model/dimension,
  prefixes and workspace. It must pass document-count and retrieval fixtures.
- Changing embedding semantics creates a new workspace and full reindex.

Fresh, repeat, concurrent and 001-006 upgrade tests remain required for migration 007,
but they cover summary/profile changes only. LightRAG persistence is verified through
whole-volume restart and restore tests rather than SQL migrations.
