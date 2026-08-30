# Data Model

All tables include `created_at`; mutable aggregates also include `updated_at`.
Exact SQL is delivered in ordered migrations with integration tests.

## Account

- `user_account`: username, display_name, password_hash, role (`user|admin`),
  status (`active|disabled`), timestamps; unique case-insensitive username.
- `user_session`: user_id, token_hash, expires_at, last_seen_at, revoked_at,
  user_agent_hash; unique token_hash and expiry index.
- `player_profile`: one row per user, default_game_id/default_region and JSONB
  preferences; unique user_id.

Passwords and raw session tokens never appear outside the account adapter and
login response cookie boundary.

## Catalog

- `game`: slug, name, summary, description, developer, publisher, release_date,
  cover_url, status; unique slug.
- `game_edition`: game_id, code, name, description, status; unique game+code.
- `game_price`: edition_id, region_code, currency, amount_minor, active_from,
  active_until; checks for non-negative amount and valid interval.
- `game_entitlement`: user_id, edition_id, source_order_id, granted_at, status;
  unique active ownership per user+edition.

## Community

- `community_post`: author_id, optional game_id, title, content, status,
  deleted_at and timestamps; feed index on status+created_at+id and game feed.
- `community_comment`: post_id, author_id, content, status, deleted_at.
- `community_reaction`: post_id, user_id, reaction_type; unique
  post+user+reaction_type.

Content is soft deleted so ownership/audit and counts remain coherent.

## Promotion

- `coupon_campaign`: code, name, status, starts_at, ends_at.
- `coupon_definition`: campaign_id, code, discount_type (`fixed|percentage`),
  fixed_minor/percentage_bps, currency, minimum_minor, total_stock,
  claimed_stock, per_user_limit, optional game_id/edition_id; unique code and
  checks for mutually valid discount fields and stock bounds.
- `coupon_claim`: coupon_id, user_id, status (`claimed|redeemed|expired`),
  idempotency_key, claimed_at, redeemed_order_id; unique user+idempotency_key
  and indexes supporting per-user count.

## Order And Payment

- `purchase_order`: public order_no, user_id, status
  (`pending_payment|paid|cancelled|expired`), currency, subtotal_minor,
  discount_minor, total_minor, coupon_claim_id, idempotency_key, timestamps;
  unique user+idempotency_key and order_no.
- `purchase_order_item`: order_id, edition_id, game/edition name snapshot,
  unit_price_minor, quantity fixed to one initially; unique order+edition.
- `payment_record`: order_id, provider, provider_reference, status, amount_minor,
  idempotency_key, raw safe metadata; unique provider+provider_reference and
  order+idempotency_key.

Order totals and snapshots are immutable after leaving `pending_payment`.

## Assistant Ownership

- `conversation_session.user_id` binds authenticated conversations; guest
  sessions remain nullable and are accessible only by their unguessable key.
- Existing knowledge tables remain. Knowledge writes require admin role.
- Persistent Agent traces are excluded until retention/privacy are specified;
  safe operational traces go to logs.

## Retention

- Revoked/expired sessions: delete after 30 days.
- Orders/payments/entitlements: retain; deletion policy requires a legal/product
  decision before production real-money use.
- Soft-deleted community content: retain 30 days in this demo, then purge via a
  future scheduled maintenance command.
- Conversations: user deletion/export is FOLLOW_UP before public launch.
