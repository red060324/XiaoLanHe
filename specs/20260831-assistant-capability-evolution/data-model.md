# Data Model

- Status: `DRAFT`
- Authoritative spec: `./spec.md`

Migration 006 is additive. Existing migration files remain immutable.

## Conversation Summary

Add to `conversation_session`:

| Column | Type | Rule |
|---|---|---|
| `summary_text` | `text` | nullable; capped by UseCase before write |
| `summary_through_message_id` | `bigint` | nullable FK to `conversation_message(id)` |
| `summary_prompt_version` | `varchar(64)` | nullable |
| `summary_updated_at` | `timestamptz` | nullable |

The update condition accepts a new watermark only when it is greater than the
stored watermark. Deleting a conversation cascades messages; summary columns
disappear with the session.

## Explicit Player Profile

Reuse `player_profile` and add a unique index on non-null `user_id`. The
application stores only the reviewed keys in `preferences`:

```json
{
  "favoriteGenres": ["rpg"],
  "preferredPlatforms": ["pc"],
  "preferredLanguages": ["zh-CN"],
  "maxPriceMinor": 30000,
  "currency": "CNY"
}
```

`default_region` stores the validated region. One authenticated user has at
most one row. Replace uses an upsert; clear deletes the row. Entitlements remain
the only ownership source.

## Knowledge Graph

### `knowledge_entity`

| Column | Type | Rule |
|---|---|---|
| `id` | `bigserial` | primary key |
| `entity_type` | `varchar(32)` | reviewed enum-like value |
| `canonical_name` | `varchar(256)` | trimmed display value |
| `normalized_name` | `varchar(256)` | normalized lookup value |
| `description` | `text` | bounded extraction output |
| `embedding` | `vector(1536)` | nullable |
| timestamps | `timestamptz` | required |

Unique key: `(entity_type, normalized_name)`. Add a full-text index over name
and description plus an HNSW vector index when embeddings exist.

### `knowledge_relation`

| Column | Type | Rule |
|---|---|---|
| `id` | `bigserial` | primary key |
| `source_entity_id` | `bigint` | FK entity, cascade |
| `target_entity_id` | `bigint` | FK entity, cascade; source != target |
| `relation_type` | `varchar(64)` | normalized bounded value |
| `description` | `text` | bounded extraction output |
| `weight` | `real` | `0 < weight <= 1` |
| timestamps | `timestamptz` | required |

Unique key: `(source_entity_id, target_entity_id, relation_type)`; add source
and target indexes for bounded traversal.

### Provenance

- `knowledge_chunk_entity(chunk_id, entity_id, extraction_version)`
- `knowledge_relation_chunk(relation_id, chunk_id, extraction_version)`

Both use composite primary keys and cascading foreign keys. Graph evidence is
eligible for answers only through one of these joins. Rebuild deletes and
replaces provenance for the target document/extraction version in one
transaction, then removes orphan entities/relations.

## Cardinality And Limits

- maximum 100 extracted entities and 200 relations per document build;
- maximum 20 entity references and 20 relation references per chunk;
- retrieval expands at most two hops and 50 graph candidates;
- final Answer receives at most eight fused evidence items;
- no graph row stores prompts, raw model output, user messages, or profile data.

## Retention And Rollback

Profiles live until the user clears them or the account is deleted. Summaries
live with their conversation. Graph rows are derived data and may be deleted and
rebuilt from knowledge documents. Rollback disables new reads/writes and leaves
additive schema in place; a later reviewed migration may remove unused derived
data.
