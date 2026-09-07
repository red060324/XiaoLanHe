# Data Model

- Status: `IMPLEMENTED — PRE_MERGE ENVIRONMENT BLOCKED`
- Authoritative spec: `./spec.md`
- Relational owner: MySQL 8.4/InnoDB
- Knowledge owner: official LightRAG 1.5.7
- Vector projection owner: LightRAG `MilvusVectorDBStorage`

## MySQL Schema Conventions

- Every table uses `ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`.
- Human-readable `VARCHAR`/`TEXT` columns explicitly use `utf8mb4_0900_ai_ci`.
  Normalized identifiers, statuses, digests, idempotency keys and opaque references use
  `CHARACTER SET ascii COLLATE ascii_bin`; their case and bytes are significant.
- Existing relational IDs are signed `BIGINT`; surrogate primary keys are
  `BIGINT NOT NULL AUTO_INCREMENT`, and cutover advances each sequence above `MAX(id)`.
- All timestamps are UTC `DATETIME(6)`. Zero dates, infinity and out-of-range source
  values fail preflight. `updated_at` has no implicit `ON UPDATE` behavior.
- Money, counts and versions remain signed integers with explicit nonnegative checks.
- PostgreSQL `jsonb` becomes native `JSON`. Empty objects use
  `DEFAULT (JSON_OBJECT())` on pinned MySQL 8.4; repositories still bind explicit JSON.
- SHA-256 values use `BINARY(32)` and cross HTTP/report boundaries as lowercase hex.
- Every foreign key is named. An omitted PostgreSQL delete action is preserved as
  `ON DELETE RESTRICT`; the explicit legacy cascades and `SET NULL` are listed below.
- MySQL `UNIQUE` allows multiple `NULL` values. Predicate uniqueness uses either that
  native behavior or a nullable stored generated discriminator.
- `CHECK` constraints below are enforcement requirements, not documentation comments.
  Regex checks use `REGEXP_LIKE(value, pattern, 'c')` to force case-sensitive matching.

Notation used in column tables: `U(n)` is `VARCHAR(n) CHARACTER SET utf8mb4 COLLATE
utf8mb4_0900_ai_ci`; `UTEXT`/`UMEDIUMTEXT` use the same character set and collation;
`A(n)` is `VARCHAR(n) CHARACTER SET ascii COLLATE ascii_bin`; `AC(n)` is the analogous
fixed-width `CHAR(n)`.

## Account

### `user_account`

| Column | Type | Null | Default / generation |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `user_name` | `A(128)` | no | — |
| `display_name` | `U(128)` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `password_hash` | `AC(60)` | yes | `NULL` |
| `role` | `A(16)` | no | `'user'` |
| `status` | `A(16)` | no | `'active'` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_user_account_name (user_name)`. The
  lowercase format check plus binary uniqueness replaces both legacy raw and
  `lower(user_name)` unique indexes.
- Checks: `role IN ('user','admin')`; `status IN ('active','disabled')`;
  `REGEXP_LIKE(user_name,'^[a-z0-9_]{3,32}$','c')`; nullable display name is either
  absent or `CHAR_LENGTH(TRIM(display_name)) BETWEEN 1 AND 64`.
- FKs/indexes: none beyond the primary and unique keys. `password_hash` remains nullable
  for migrated pre-auth accounts.

### `user_session`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `user_id` | `BIGINT` | no | — |
| `token_hash` | `BINARY(32)` | no | — |
| `expires_at` | `DATETIME(6)` | no | — |
| `last_seen_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `revoked_at` | `DATETIME(6)` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_user_session_token_hash (token_hash)`.
- FK: `fk_user_session_user (user_id) REFERENCES user_account(id) ON DELETE CASCADE`.
- Check: `ck_user_session_expiry (expires_at > created_at)`.
- Index: `idx_user_session_user_expiry (user_id, expires_at DESC)`.
- The legacy 64-character lowercase SHA-256 hex is decoded to exactly 32 bytes.

## Assistant And Conversation

### `player_profile`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `user_id` | `BIGINT` | yes | `NULL` |
| `default_game` | `A(64)` | yes | `NULL` |
| `default_region` | `A(32)` | yes | `NULL` |
| `preferences` | `JSON` | no | `(JSON_OBJECT())` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_player_profile_user (user_id)`. Multiple
  historical `NULL` owners remain legal, while each non-null user has one profile.
- FK: `fk_player_profile_user (user_id) REFERENCES user_account(id) ON DELETE RESTRICT`.
- Check: nullable `default_region` matches `^[A-Z0-9_-]{2,16}$`.
- Assistant profile payload inside `preferences.assistant` remains application-validated
  at 4096 encoded bytes. That cap does not constrain unrelated legacy preferences.

### `conversation_session`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `session_key` | `A(64)` | no | — |
| `user_id` | `BIGINT` | yes | `NULL` |
| `title` | `U(255)` | yes | `NULL` |
| `metadata` | `JSON` | no | `(JSON_OBJECT())` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `summary_text` | `UMEDIUMTEXT` | yes | `NULL` |
| `summary_through_message_id` | `BIGINT` | yes | `NULL` |
| `summary_prompt_version` | `A(64)` | yes | `NULL` |
| `summary_updated_at` | `DATETIME(6)` | yes | `NULL` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_conversation_session_key (session_key)`.
- FKs: `fk_conversation_session_user (user_id) REFERENCES user_account(id) ON DELETE
  RESTRICT`; after both conversation tables exist, add `fk_conversation_summary_message
  (summary_through_message_id) REFERENCES conversation_message(id) ON DELETE SET NULL`.
- Check: nullable prompt version matches
  `^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`.
- Indexes: `idx_conversation_session_user (user_id)` and
  `idx_conversation_session_summary_message (summary_through_message_id)`.
- The summary update transaction must additionally prove that the watermark message
  belongs to this session; the single-column FK cannot express that cross-row invariant.

### `conversation_message`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `session_id` | `BIGINT` | no | — |
| `message_key` | `A(36)` | yes | `NULL` |
| `role` | `A(32)` | no | — |
| `content` | `UMEDIUMTEXT` | no | — |
| `model_name` | `A(128)` | yes | `NULL` |
| `metadata` | `JSON` | no | `(JSON_OBJECT())` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_conversation_message_key
  (session_id,message_key)`. MySQL permits multiple legacy `NULL` keys per session,
  while every new runtime message supplies a stable key and cannot be duplicated.
- FK: `fk_conversation_message_session (session_id) REFERENCES
  conversation_session(id) ON DELETE CASCADE`.
- Checks: `role IN ('user','assistant')`; a non-null `message_key` is a lowercase
  RFC 4122 UUIDv4 (`8-4-4-4-12`, version nibble `4`, variant nibble `8|9|a|b`).
- Indexes: `idx_conversation_message_session (session_id, created_at)` and
  `idx_conversation_message_cursor (session_id,id)` for stable ID cursor reads.
- The PostgreSQL source predates `message_key`; the cutover projects
  `NULL::text` into this target-only column. New MySQL runtime writes always use a
  generated UUIDv4, including retries after ambiguous commits, so exact durable
  identity is available without inventing identities for historical messages.

## Catalog

### `game`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `slug` | `A(64)` | no | — |
| `name` | `U(160)` | no | — |
| `summary` | `U(500)` | no | `''` |
| `description` | `UMEDIUMTEXT` | no | `('')` |
| `developer` | `U(160)` | no | `''` |
| `publisher` | `U(160)` | no | `''` |
| `release_date` | `DATE` | yes | `NULL` |
| `cover_url` | `UTEXT` | no | `('')` |
| `status` | `A(16)` | no | `'active'` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_game_slug (slug)`.
- Checks: slug matches `^[a-z0-9-]{3,64}$`; trimmed name length is 1–160;
  `CHAR_LENGTH(summary)<=500`; `CHAR_LENGTH(description)<=20000`; developer and
  publisher are at most 160 characters; `status IN ('active','inactive')`.
- Index: `idx_game_public (status, id DESC)`.

### `game_edition`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `game_id` | `BIGINT` | no | — |
| `code` | `A(64)` | no | — |
| `name` | `U(160)` | no | — |
| `description` | `UMEDIUMTEXT` | no | `('')` |
| `status` | `A(16)` | no | `'active'` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_game_edition_code (game_id, code)`.
- FK: `fk_game_edition_game (game_id) REFERENCES game(id) ON DELETE CASCADE`.
- Checks: code matches `^[a-z0-9-]{2,64}$`; trimmed name
  length is 1–160; `status IN ('active','inactive')`.

### `game_price`

| Column | Type | Null | Default / generation |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `edition_id` | `BIGINT` | no | — |
| `region_code` | `A(16)` | no | — |
| `currency` | `AC(3)` | no | — |
| `amount_minor` | `BIGINT` | no | — |
| `active_from` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `active_until` | `DATETIME(6)` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `open_price` | `TINYINT` | generated nullable | `IF(active_until IS NULL,1,NULL) STORED` |

- PK: `PRIMARY KEY (id)`.
- FK: `fk_game_price_edition (edition_id) REFERENCES game_edition(id) ON DELETE CASCADE`.
- Checks: region matches `^[A-Z0-9-]{2,16}$`; currency matches `^[A-Z]{3}$`;
  `amount_minor>=0`; `active_until IS NULL OR active_until>active_from`.
- Unique keys: `uk_game_price_active_key (edition_id,region_code,currency,active_from)`;
  `uk_game_price_one_active (edition_id,region_code,currency,open_price)`.
- Index: `idx_game_price_lookup
  (edition_id,region_code,currency,active_from DESC)`.

### `game_entitlement`

| Column | Type | Null | Default / generation |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `user_id` | `BIGINT` | no | — |
| `edition_id` | `BIGINT` | no | — |
| `source_order_id` | `BIGINT` | yes | `NULL` |
| `status` | `A(16)` | no | `'active'` |
| `granted_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `active_entitlement` | `TINYINT` | generated nullable | `IF(status='active',1,NULL) STORED` |

- PK: `PRIMARY KEY (id)`.
- FKs: `fk_game_entitlement_user (user_id) REFERENCES user_account(id) ON DELETE
  CASCADE`; `fk_game_entitlement_edition (edition_id) REFERENCES game_edition(id) ON
  DELETE RESTRICT`; add `fk_game_entitlement_source_order (source_order_id) REFERENCES
  purchase_order(id) ON DELETE RESTRICT` after `purchase_order` exists.
- Check: `status IN ('active','revoked')`.
- Unique: `uk_game_entitlement_owner
  (user_id,edition_id,active_entitlement)`. Revoked history may coexist.
- Indexes: `idx_game_entitlement_edition (edition_id)` and
  `idx_game_entitlement_source_order (source_order_id)`.
- A duplicate-key during grant is not automatically a successful payment replay. In the
  same transaction, lock and read the existing active entitlement, source order and
  payment record. Treat it as idempotent only when `user_id`, `edition_id` and
  `source_order_id` equal the current paid order, the order is `paid`, and the persisted
  payment row is also `paid` with `provider`, `provider_reference` and `amount_minor`
  equal to the payment being replayed and the order total. Any
  absent/mismatched source order, payment reference or non-paid order is a conflict and
  must roll back; never relink an existing entitlement to a different payment.

## Community

### `community_post`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `author_id` | `BIGINT` | no | — |
| `game_id` | `BIGINT` | yes | `NULL` |
| `title` | `U(160)` | no | — |
| `content` | `UTEXT` | no | — |
| `status` | `A(16)` | no | `'published'` |
| `deleted_at` | `DATETIME(6)` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- FKs: author and game reference `user_account(id)` and `game(id)`, both `ON DELETE
  RESTRICT`. PK is `(id)`.
- Checks: status is `published|hidden|deleted`; `(status='deleted')=(deleted_at IS NOT
  NULL)`; trimmed title length 1–160; trimmed content length 1–10000.
- Indexes: `idx_community_post_feed (status,created_at DESC,id DESC)`;
  `idx_community_post_game_feed (game_id,status,created_at DESC,id DESC)`;
  `idx_community_post_author (author_id,created_at DESC,id DESC)`. Status prefixes
  replace the two PostgreSQL partial feed indexes.

### `community_comment`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `post_id` | `BIGINT` | no | — |
| `author_id` | `BIGINT` | no | — |
| `content` | `UTEXT` | no | — |
| `status` | `A(16)` | no | `'published'` |
| `deleted_at` | `DATETIME(6)` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- FKs: post and author reference `community_post(id)` and `user_account(id)`, both
  `ON DELETE RESTRICT`. PK is `(id)`.
- Checks: status is `published|hidden|deleted`; deleted state equals non-null
  `deleted_at`; trimmed content length 1–3000.
- Indexes: `idx_community_comment_post (post_id,status,created_at,id)` and
  `idx_community_comment_author (author_id,created_at DESC,id DESC)`.

### `community_reaction`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `post_id` | `BIGINT` | no | — |
| `user_id` | `BIGINT` | no | — |
| `reaction_type` | `A(16)` | no | — |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_community_reaction
  (post_id,user_id,reaction_type)`.
- FKs: post and user references both use `ON DELETE CASCADE`.
- Check: `reaction_type IN ('like','helpful','funny')`.
- Indexes: `idx_community_reaction_post (post_id,reaction_type)` and
  `idx_community_reaction_user (user_id)`.

## Promotion

### `coupon_campaign`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `code` | `A(32)` | no | — |
| `name` | `U(160)` | no | — |
| `status` | `A(16)` | no | `'draft'` |
| `starts_at` | `DATETIME(6)` | no | — |
| `ends_at` | `DATETIME(6)` | no | — |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_coupon_campaign_code (code)`.
- Checks: code matches `^[A-Z0-9-]{3,32}$`; status is
  `draft|active|paused|ended`; `ends_at>starts_at`.
- Index: `idx_coupon_campaign_active (status,starts_at,ends_at)`.

### `coupon_definition`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `campaign_id` | `BIGINT` | no | — |
| `code` | `A(32)` | no | — |
| `name` | `U(160)` | no | — |
| `discount_type` | `A(16)` | no | — |
| `fixed_minor` | `BIGINT` | yes | `NULL` |
| `percentage_bps` | `INT` | yes | `NULL` |
| `currency` | `AC(3)` | no | — |
| `minimum_minor` | `BIGINT` | no | `0` |
| `total_stock` | `BIGINT` | no | — |
| `claimed_stock` | `BIGINT` | no | `0` |
| `per_user_limit` | `INT` | no | `1` |
| `game_id` | `BIGINT` | yes | `NULL` |
| `edition_id` | `BIGINT` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_coupon_definition_code (code)`.
- FKs: campaign, game and edition reference their parent IDs with `ON DELETE RESTRICT`.
- Checks: code matches `^[A-Z0-9-]{3,32}$`; currency matches `^[A-Z]{3}$`; exactly
  one discount shape is valid: fixed has `fixed_minor>0` and null percentage, percentage
  has null fixed and `percentage_bps BETWEEN 1 AND 10000`; `minimum_minor>=0`;
  `total_stock>0 AND claimed_stock BETWEEN 0 AND total_stock`; `per_user_limit>0`;
  `edition_id IS NULL OR game_id IS NOT NULL`.
- Indexes: `idx_coupon_definition_campaign (campaign_id,id DESC)`,
  `idx_coupon_definition_game (game_id,id DESC)`, and
  `idx_coupon_definition_edition (edition_id)`. Edition-to-game consistency remains a
  locked application check because the legacy schema does not carry a composite FK.

### `coupon_claim`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `coupon_id` | `BIGINT` | no | — |
| `user_id` | `BIGINT` | no | — |
| `status` | `A(16)` | no | `'claimed'` |
| `idempotency_key` | `A(128)` | no | — |
| `claimed_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `redeemed_order_id` | `BIGINT` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_coupon_claim_idempotency
  (user_id,idempotency_key)`.
- FKs: coupon and user references use `ON DELETE RESTRICT`; the redeemed-order FK is
  added after both sides exist and also uses `ON DELETE RESTRICT`.
- Checks: status is `claimed|redeemed|expired`; `status='redeemed'` if and only if
  `redeemed_order_id IS NOT NULL`, so claimed and expired rows carry no redeemed-order
  link; idempotency key matches `^[A-Za-z0-9._:-]{8,128}$`.
- Indexes: `idx_coupon_claim_user (user_id,status,claimed_at DESC,id DESC)`,
  `idx_coupon_claim_coupon (coupon_id,user_id,status)`, and
  `idx_coupon_claim_redeemed_order (redeemed_order_id)`.

## Order And Payment

### `purchase_order`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `order_no` | `A(40)` | no | — |
| `user_id` | `BIGINT` | no | — |
| `status` | `A(24)` | no | `'pending_payment'` |
| `currency` | `AC(3)` | no | — |
| `region_code` | `A(16)` | no | — |
| `subtotal_minor` | `BIGINT` | no | — |
| `discount_minor` | `BIGINT` | no | `0` |
| `total_minor` | `BIGINT` | no | — |
| `coupon_claim_id` | `BIGINT` | yes | `NULL` |
| `idempotency_key` | `A(128)` | no | — |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `source_type` | `A(24)` | no | `'standard'` |
| `source_reference` | `A(64)` | yes | `NULL` |
| `payment_expires_at` | `DATETIME(6)` | yes | `NULL` |

- PK/unique: `PRIMARY KEY (id)`; unique keys on `order_no`,
  `(user_id,idempotency_key)`, nullable `coupon_claim_id`, and
  `(source_type,source_reference)`. Multiple standard-order `NULL` references remain legal.
- FKs: user and coupon claim use `ON DELETE RESTRICT`; add the coupon FK only after both
  cyclic tables exist.
- Checks: order number matches `^ord_[a-f0-9]{32}$`; status is
  `pending_payment|paid|cancelled|expired`; currency is three uppercase ASCII letters;
  region matches `^[A-Z0-9-]{2,16}$`; idempotency key matches
  `^[A-Za-z0-9._:-]{8,128}$`; subtotal/discount are nonnegative, discount does not
  exceed subtotal, and total equals subtotal minus discount; source type is
  `standard|flash_sale`; standard orders have null source reference and expiry, while
  flash-sale orders have both non-null; non-null source references match the flash-sale
  request-ID pattern.
- Indexes: `idx_purchase_order_owner (user_id,created_at DESC,id DESC)` and
  `idx_purchase_order_flash_expiry
  (source_type,status,payment_expires_at,id)`. The leading state columns replace the
  PostgreSQL partial expiry index.

### `purchase_order_item`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `order_id` | `BIGINT` | no | — |
| `edition_id` | `BIGINT` | no | — |
| `game_id` | `BIGINT` | no | — |
| `game_slug_snapshot` | `A(64)` | no | — |
| `game_name_snapshot` | `U(160)` | no | — |
| `edition_code_snapshot` | `A(64)` | no | — |
| `edition_name_snapshot` | `U(160)` | no | — |
| `unit_price_minor` | `BIGINT` | no | — |
| `quantity` | `INT` | no | `1` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_purchase_order_item
  (order_id,edition_id)`.
- FKs: order uses `ON DELETE CASCADE`; edition and game use `ON DELETE RESTRICT`.
- Checks: `unit_price_minor>=0`; `quantity=1`.
- Indexes: `idx_purchase_order_item_edition (edition_id)` and
  `idx_purchase_order_item_game (game_id)`. Snapshot columns remain immutable facts.

### `payment_record`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `order_id` | `BIGINT` | no | — |
| `provider` | `A(32)` | no | — |
| `provider_reference` | `A(96)` | no | — |
| `status` | `A(16)` | no | — |
| `amount_minor` | `BIGINT` | no | — |
| `idempotency_key` | `A(128)` | no | — |
| `metadata` | `JSON` | no | `(JSON_OBJECT())` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_payment_record_reference
  (provider,provider_reference)`; `UNIQUE uk_payment_record_idempotency
  (order_id,idempotency_key)`.
- FK: order uses `ON DELETE RESTRICT`.
- Checks: status is `paid|failed`; `amount_minor>=0`; idempotency key matches
  `^[A-Za-z0-9._:-]{8,128}$`.

## Flash Sale

### `flash_sale_scope_lock`

| Column | Type | Null | Default |
|---|---|---:|---|
| `edition_id` | `BIGINT` | no | — |
| `region_code` | `A(16)` | no | — |
| `currency` | `AC(3)` | no | — |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK: `(edition_id,region_code,currency)`.
- FK: edition uses `ON DELETE RESTRICT`.
- Checks: region matches `^[A-Z0-9-]{2,16}$`; currency matches `^[A-Z]{3}$`.
- There is no separate `GET_LOCK` for business concurrency. Activity creation and
  activation begin a transaction, execute an idempotent
  `INSERT ... ON DUPLICATE KEY UPDATE edition_id=VALUES(edition_id)` for the exact scope,
  then `SELECT ... FOR UPDATE` that same row before reading or writing activities. The
  upsert and row lock must be in the same transaction; committing between them loses the
  serialization guarantee. All paths lock scopes in `(edition_id,region_code,currency)`
  order before any activity row, then check that no active interval overlaps.
- Baseline/cutover backfill inserts the distinct scope tuple of every existing
  `flash_sale_activity`, verifies the number and exact set against `SELECT DISTINCT`, and
  fails on invalid normalized values. Backfill completes before activation traffic is
  enabled. Rows are retained even when no activity is active so the lock target remains
  stable; cleanup, if ever needed, requires a separate reviewed design.

### `flash_sale_activity`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `code` | `A(64)` | no | — |
| `edition_id` | `BIGINT` | no | — |
| `region_code` | `A(16)` | no | — |
| `currency` | `AC(3)` | no | — |
| `sale_price_minor` | `BIGINT` | no | — |
| `total_stock` | `BIGINT` | no | — |
| `allocated_stock` | `BIGINT` | no | `0` |
| `status` | `A(16)` | no | `'draft'` |
| `starts_at` | `DATETIME(6)` | no | — |
| `ends_at` | `DATETIME(6)` | no | — |
| `payment_timeout_seconds` | `INT` | no | `900` |
| `version` | `BIGINT` | no | `0` |
| `created_by` | `BIGINT` | no | — |
| `activated_at` | `DATETIME(6)` | yes | `NULL` |
| `cancelled_at` | `DATETIME(6)` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_flash_sale_activity_code (code)`.
- FKs: edition and creator references use `ON DELETE RESTRICT`.
- Checks: code `^[A-Z0-9-]{3,64}$`; region `^[A-Z0-9-]{2,16}$`; currency
  `^[A-Z]{3}$`; price nonnegative; `total_stock>0` and allocated stock between zero and
  total; status `draft|active|cancelled|ended`; `starts_at<ends_at`; timeout 60–86400;
  version nonnegative.
- Indexes: `idx_flash_sale_activity_window (status,starts_at,ends_at,id)`,
  `idx_flash_sale_activity_scope_window
  (edition_id,region_code,currency,status,starts_at,ends_at,id)` for the exact
  serialized overlap check, `idx_flash_sale_activity_edition
  (edition_id,created_at DESC,id DESC)`, and `idx_flash_sale_activity_creator
  (created_by)`.

### `flash_sale_reservation`

| Column | Type | Null | Default |
|---|---|---:|---|
| `request_id` | `A(64)` | no | — |
| `activity_id` | `BIGINT` | no | — |
| `user_id` | `BIGINT` | no | — |
| `idempotency_digest` | `BINARY(32)` | no | — |
| `status` | `A(16)` | no | `'reserved'` |
| `order_id` | `BIGINT` | yes | `NULL` |
| `failure_code` | `A(48)` | yes | `NULL` |
| `reserved_at` | `DATETIME(6)` | no | — |
| `payment_expires_at` | `DATETIME(6)` | no | — |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (request_id)`; unique keys `(activity_id,user_id)`,
  `(activity_id,user_id,idempotency_digest)`, and nullable `order_id`.
- FKs: activity, user and order references use `ON DELETE RESTRICT`.
- Checks: request ID matches
  `^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$`; status is
  `reserved|order_ready|failed|expired`; reserved/failed have null order while
  order_ready/expired have a non-null order. A failed reservation has a non-null,
  non-blank `failure_code`, and every non-failed reservation has a null failure code.
  `BINARY(32)` enforces digest length.
- Index: `idx_flash_sale_reservation_owner (user_id,created_at DESC,request_id)`.

### `flash_sale_release_job`

| Column | Type | Null | Default |
|---|---|---:|---|
| `id` | `BIGINT AUTO_INCREMENT` | no | — |
| `request_id` | `A(64)` | no | — |
| `activity_id` | `BIGINT` | no | — |
| `user_id` | `BIGINT` | no | — |
| `idempotency_digest` | `BINARY(32)` | no | — |
| `reserved_at` | `DATETIME(6)` | no | — |
| `reason` | `A(32)` | no | — |
| `status` | `A(16)` | no | `'pending'` |
| `attempts` | `INT` | no | `0` |
| `next_attempt_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `lease_until` | `DATETIME(6)` | yes | `NULL` |
| `last_error_code` | `A(48)` | yes | `NULL` |
| `completed_at` | `DATETIME(6)` | yes | `NULL` |
| `created_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `updated_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |

- PK/unique: `PRIMARY KEY (id)`; `UNIQUE uk_flash_sale_release_job_request
  (request_id)`.
- FKs: activity and user references use `ON DELETE RESTRICT`. There is deliberately no
  reservation FK because final repair jobs must remain durable independently.
- Checks: request-ID regex above; reason is
  `technical_rollback|final_guard|payment_expired|admin_repair`; status is
  `pending|leased|done`; `attempts>=0`; pending has null lease and completion, leased
  has a non-null lease, null completion and positive attempts, and done has a null lease,
  non-null completion and positive attempts. Digest length is enforced by its type.
- Indexes: `idx_flash_sale_release_job_due (status,next_attempt_at,id)`,
  `idx_flash_sale_release_job_expired (status,lease_until,id)`,
  `idx_flash_sale_release_job_activity (activity_id)`, and
  `idx_flash_sale_release_job_user (user_id)`. Pending and expired lease scans
  use separate leading-column indexes; the combined worker predicate may use
  index merge and is verified separately on MySQL 8.4.

## Migration Metadata

### `schema_migration`

| Column | Type | Null | Default |
|---|---|---:|---|
| `version` | `A(255)` | no | — |
| `name` | `U(255)` | no | — |
| `checksum_sha256` | `BINARY(32)` | no | — |
| `dirty` | `TINYINT(1)` | no | `1` |
| `started_at` | `DATETIME(6)` | no | `CURRENT_TIMESTAMP(6)` |
| `completed_at` | `DATETIME(6)` | yes | `NULL` |
| `failure_context` | `U(1024)` | yes | `NULL` |

- PK: `PRIMARY KEY (version)`; check `dirty IN (0,1)` and
  `(dirty=1 AND completed_at IS NULL) OR (dirty=0 AND completed_at IS NOT NULL)`.
- The runner holds `GET_LOCK('xiaolanhe:mysql:migrate:v1', timeout)` on one dedicated
  connection, verifies every clean checksum and absence of dirty rows, writes dirty state
  in autocommit before each one-statement migration, validates the postcondition, then
  marks it clean. Process death never gets inferred as success. Failure context is bounded
  and excludes SQL values, DSNs, content and secrets.
- PostgreSQL migration names/checksums never enter this independent history.

## Foreign-Key Creation And Cutover Order

Create parent tables before children in this order:

```text
user_account
  -> user_session, player_profile, conversation_session, game
conversation_session -> conversation_message
game -> game_edition -> game_price
coupon_campaign -> coupon_definition -> coupon_claim
purchase_order -> purchase_order_item, payment_record
game_entitlement
community_post -> community_comment, community_reaction
flash_sale_scope_lock -> flash_sale_activity
flash_sale_activity -> flash_sale_reservation, flash_sale_release_job
```

Add these references only after both tables exist:

1. `conversation_session.summary_through_message_id -> conversation_message.id`;
2. `purchase_order.coupon_claim_id -> coupon_claim.id`;
3. `coupon_claim.redeemed_order_id -> purchase_order.id`;
4. `game_entitlement.source_order_id -> purchase_order.id`.

The claim/order cycle is copied with both nullable links unset. A source redeemed claim
is additionally staged as `claimed`, producing the CHECK-valid intermediate pair
`claimed + NULL`; the source status remains part of the snapshot and checkpoint digest.
The deferred phase restores each claim's status and `redeemed_order_id` together in one
MySQL `UPDATE` inside the batch transaction, then restores order claim pointers. Resume
accepts only an exact staged row or exact final row, never a half-restored pair. Foreign-key
checks are not globally disabled. After data copy, verify orphans and all unique/check
invariants before advancing `AUTO_INCREMENT`.

## Text Capacity Decisions

| Column | Current contract | Worst-case UTF-8 | MySQL type |
|---|---:|---:|---|
| `game.description` | 20,000 runes | 80,000 bytes | `MEDIUMTEXT` |
| `game_edition.description` | no durable cap | unbounded by current code | `MEDIUMTEXT` |
| `community_post.content` | 10,000 runes | 40,000 bytes | `TEXT` |
| `community_comment.content` | 3,000 runes | 12,000 bytes | `TEXT` |
| `conversation_message.content` | no durable cap | unbounded by current code | `MEDIUMTEXT` |
| `conversation_session.summary_text` | configurable rune cap | may grow beyond 64 KiB contract | `MEDIUMTEXT` |
| `game.cover_url` | no stable DB limit, not indexed | normally small | `TEXT` |

MySQL `TEXT` is limited to 65,535 bytes, not characters. No prefix indexes are created
on these fields. The application retains rune limits where one exists and additionally
rejects request bodies above its transport cap.

## Retired PostgreSQL Knowledge Tables

`knowledge_document`, `knowledge_chunk` and `tool_call_log` are deliberately absent from
the MySQL baseline. This is an ownership decision, not an accidental copy omission.

### Legacy document import mapping

The isolated PostgreSQL import command reads `knowledge_document` in ascending `id`
batches. Its exact LightRAG text envelope is:

```text
XiaoLanHe-Knowledge-v1
Title: <title>
Source-Type: <source_type>
Source-URL: <source_url-or-empty>
Game-Code: <game_code-or-empty>
Region-Code: <region_code-or-empty>
Patch-Version: <patch_version-or-empty>

<content_text>
```

`file_source` is the deterministic, binary source key
`xlh-legacy-<knowledge_document.id>.txt`. The six headers and content are trimmed and
validated by the existing versioned normalizer. Source key identity is based on the
immutable legacy ID, while a separate SHA-256 of the canonical envelope proves content
identity. A 409 replay is accepted only when exactly one managed target document has the
expected source key and the frozen manifest proves the same envelope digest; source-key
equality alone is insufficient. No LightRAG ID is written back to PostgreSQL.

The following source columns are represented directly: `source_type`, `title`,
`source_url`, `game_code`, `region_code`, `patch_version`, and `content_text`. Legacy
`metadata`, `published_at`, `created_at` and `updated_at` are not currently represented;
the cutover manifest must either preserve approved values in a versioned envelope or
mark each field deliberately discarded. It may not omit their disposition.

No `knowledge_chunk` row is copied. Legacy `chunk_no`, `chunk_text`, 1536-dimensional
pgvector embedding and chunk metadata are derived state. LightRAG re-chunks the canonical
documents, extracts graph entities/relationships and embeds all three vector projections
with the pinned 1024-dimensional model. Source and target chunk counts are therefore
recorded separately and are not required to be equal.

### Import checkpoint and retirement gate

- Freeze legacy knowledge writes, then generate a stable manifest ordered by legacy ID:
  source key, canonical envelope SHA-256, rune/byte length, old chunk count and disposition
  of non-mapped metadata/timestamps. Record an aggregate manifest hash.
- Dry-run validation precedes explicit execution. A resumable success watermark advances
  only across a contiguous prefix of successfully processed or digest-verified replayed
  IDs. Failed IDs are explicit; a scanned `lastId` is not a safe resume checkpoint.
- Poll `/documents/track_status/<track-id>` through
  `PENDING|PARSING|ANALYZING|PREPROCESSED|PROCESSING` to exactly one expected source.
  `PROCESSED` succeeds; `FAILED`, timeout, missing/multiple source or contract drift fails
  the batch without blind write retry.
- The final report records source document/chunk counts, submitted/replayed/processed/
  failed counts, status distribution, failed IDs/codes, source/target key differences,
  content lengths, target chunk totals and manifest hash. More than the client's bounded
  managed-document listing capacity requires a dedicated bounded verifier.
- Retirement requires an exact one-to-one source-key set, unique document IDs and keys,
  all target documents `PROCESSED`, zero failed/nonterminal rows, pipeline idle with no
  recovery required, per-document digest/length reconciliation, and approved retrieval
  fixtures in `local`, `global`, `hybrid` and `mix` modes with managed citations.
- It additionally requires a successful backup/restore drill of the complete LightRAG
  workspace plus Milvus, etcd, MinIO and pinned configuration, and static/runtime proof
  that no normal application path reads, writes or falls back to PostgreSQL knowledge.
  PostgreSQL knowledge remains read-only through the rollback window. Physical deletion
  requires separate approval.
- `tool_call_log` is also retired rather than copied. Privacy-safe operational telemetry
  remains logs/metrics unless a separately reviewed audit-retention schema is approved.

## LightRAG And Milvus Ownership Boundary

| Concern | Owner | Persisted form |
|---|---|---|
| full documents, chunks and caches | LightRAG | `JsonKVStorage` in `WORKING_DIR` |
| entity/relation graph | LightRAG | `NetworkXStorage` in `WORKING_DIR` |
| ingestion/document status | LightRAG | `JsonDocStatusStorage` in `WORKING_DIR` |
| chunk/entity/relationship vectors | LightRAG via Milvus adapter | Milvus collections |
| accounts, catalog, community, money, orders | XiaoLanHe | MySQL tables above |

The application uses only the authenticated official LightRAG HTTP API; it neither
constructs Milvus schemas nor reads Milvus directly. Readiness must prove LightRAG
1.5.7/API 0344, the expected workspace and working directory, `JsonKVStorage`,
`NetworkXStorage`, `JsonDocStatusStorage`, and `MilvusVectorDBStorage`. It also gates on
healthy Milvus 2.6.11 collections with `FLOAT_VECTOR` dimension 1024, `AUTOINDEX`,
`COSINE`, plus the externally persisted successful rebuild record. `/health` alone is
not evidence that the NanoVectorDB-to-Milvus rebuild completed.

The rebuild runs offline with all LightRAG writers stopped, the complete old workspace
backed up, and only the vector backend changed. The controller calls the pinned official
entity, relationship and chunk rebuild library functions; it does not drive the
interactive menu. Its structured report records `source_total`, `prepared`, `rebuilt`,
`staged`, `skipped`, `duplicates`, `failed_batches` and bounded `errors`, then verifies
the exact expected and actual ID sets independently. Interruption, missing collections,
source mutation, malformed stats, count/ID mismatch or retrieval/citation failure keeps
application readiness disabled. The normative state machine and report contract are in
`rebuild-fence.md`.

Knowledge backup consistency includes the complete `WORKING_DIR`, Milvus, etcd, MinIO
and versioned LightRAG/Milvus/embedding configuration. Partial restore is rejected.
Knowledge-dependent requests fail explicitly and boundedly when LightRAG or Milvus is
unavailable; they never unlock a relational fallback or relabel another source as
LightRAG.
