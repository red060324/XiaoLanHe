package postgrestomysql

import (
	"context"
	"database/sql"
	"fmt"
)

type targetReconciliationReader interface {
	sqlReader
	inventory(context.Context, tableSpec, int64, int64) (TableInventory, error)
}

type invariantSpec struct {
	category, name, sourceSQL, targetSQL string
}

type metricSpec struct {
	category, name, sourceSQL, targetSQL string
}

func invariants() []invariantSpec {
	return []invariantSpec{
		{"orphan", "user_session.user", `SELECT COUNT(*) FROM user_session c LEFT JOIN user_account p ON p.id=c.user_id WHERE p.id IS NULL`, `SELECT COUNT(*) FROM user_session c LEFT JOIN user_account p ON p.id=c.user_id WHERE p.id IS NULL`},
		{"orphan", "player_profile.user", `SELECT COUNT(*) FROM player_profile c LEFT JOIN user_account p ON p.id=c.user_id WHERE c.user_id IS NOT NULL AND p.id IS NULL`, `SELECT COUNT(*) FROM player_profile c LEFT JOIN user_account p ON p.id=c.user_id WHERE c.user_id IS NOT NULL AND p.id IS NULL`},
		{"orphan", "conversation_session.user", `SELECT COUNT(*) FROM conversation_session c LEFT JOIN user_account u ON u.id=c.user_id WHERE c.user_id IS NOT NULL AND u.id IS NULL`, `SELECT COUNT(*) FROM conversation_session c LEFT JOIN user_account u ON u.id=c.user_id WHERE c.user_id IS NOT NULL AND u.id IS NULL`},
		{"orphan", "conversation_message.session", `SELECT COUNT(*) FROM conversation_message c LEFT JOIN conversation_session p ON p.id=c.session_id WHERE p.id IS NULL`, `SELECT COUNT(*) FROM conversation_message c LEFT JOIN conversation_session p ON p.id=c.session_id WHERE p.id IS NULL`},
		{"orphan", "conversation.summary_message", `SELECT COUNT(*) FROM conversation_session s LEFT JOIN conversation_message m ON m.id=s.summary_through_message_id WHERE s.summary_through_message_id IS NOT NULL AND (m.id IS NULL OR m.session_id<>s.id)`, `SELECT COUNT(*) FROM conversation_session s LEFT JOIN conversation_message m ON m.id=s.summary_through_message_id WHERE s.summary_through_message_id IS NOT NULL AND (m.id IS NULL OR m.session_id<>s.id)`},
		{"orphan", "edition.game", `SELECT COUNT(*) FROM game_edition c LEFT JOIN game p ON p.id=c.game_id WHERE p.id IS NULL`, `SELECT COUNT(*) FROM game_edition c LEFT JOIN game p ON p.id=c.game_id WHERE p.id IS NULL`},
		{"orphan", "price.edition", `SELECT COUNT(*) FROM game_price c LEFT JOIN game_edition p ON p.id=c.edition_id WHERE p.id IS NULL`, `SELECT COUNT(*) FROM game_price c LEFT JOIN game_edition p ON p.id=c.edition_id WHERE p.id IS NULL`},
		{"orphan", "community_post.references", `SELECT COUNT(*) FROM community_post p LEFT JOIN user_account u ON u.id=p.author_id LEFT JOIN game g ON g.id=p.game_id WHERE u.id IS NULL OR (p.game_id IS NOT NULL AND g.id IS NULL)`, `SELECT COUNT(*) FROM community_post p LEFT JOIN user_account u ON u.id=p.author_id LEFT JOIN game g ON g.id=p.game_id WHERE u.id IS NULL OR (p.game_id IS NOT NULL AND g.id IS NULL)`},
		{"orphan", "community_comment.references", `SELECT COUNT(*) FROM community_comment c LEFT JOIN community_post p ON p.id=c.post_id LEFT JOIN user_account u ON u.id=c.author_id WHERE p.id IS NULL OR u.id IS NULL`, `SELECT COUNT(*) FROM community_comment c LEFT JOIN community_post p ON p.id=c.post_id LEFT JOIN user_account u ON u.id=c.author_id WHERE p.id IS NULL OR u.id IS NULL`},
		{"orphan", "community_reaction.references", `SELECT COUNT(*) FROM community_reaction r LEFT JOIN community_post p ON p.id=r.post_id LEFT JOIN user_account u ON u.id=r.user_id WHERE p.id IS NULL OR u.id IS NULL`, `SELECT COUNT(*) FROM community_reaction r LEFT JOIN community_post p ON p.id=r.post_id LEFT JOIN user_account u ON u.id=r.user_id WHERE p.id IS NULL OR u.id IS NULL`},
		{"orphan", "coupon.scope", `SELECT COUNT(*) FROM coupon_definition d LEFT JOIN coupon_campaign c ON c.id=d.campaign_id LEFT JOIN game g ON g.id=d.game_id LEFT JOIN game_edition e ON e.id=d.edition_id WHERE c.id IS NULL OR (d.game_id IS NOT NULL AND g.id IS NULL) OR (d.edition_id IS NOT NULL AND (e.id IS NULL OR e.game_id<>d.game_id))`, `SELECT COUNT(*) FROM coupon_definition d LEFT JOIN coupon_campaign c ON c.id=d.campaign_id LEFT JOIN game g ON g.id=d.game_id LEFT JOIN game_edition e ON e.id=d.edition_id WHERE c.id IS NULL OR (d.game_id IS NOT NULL AND g.id IS NULL) OR (d.edition_id IS NOT NULL AND (e.id IS NULL OR e.game_id<>d.game_id))`},
		{"orphan", "claim.references", `SELECT COUNT(*) FROM coupon_claim c LEFT JOIN coupon_definition d ON d.id=c.coupon_id LEFT JOIN user_account u ON u.id=c.user_id LEFT JOIN purchase_order o ON o.id=c.redeemed_order_id WHERE d.id IS NULL OR u.id IS NULL OR (c.redeemed_order_id IS NOT NULL AND o.id IS NULL)`, `SELECT COUNT(*) FROM coupon_claim c LEFT JOIN coupon_definition d ON d.id=c.coupon_id LEFT JOIN user_account u ON u.id=c.user_id LEFT JOIN purchase_order o ON o.id=c.redeemed_order_id WHERE d.id IS NULL OR u.id IS NULL OR (c.redeemed_order_id IS NOT NULL AND o.id IS NULL)`},
		{"orphan", "order.references", `SELECT COUNT(*) FROM purchase_order o LEFT JOIN user_account u ON u.id=o.user_id LEFT JOIN coupon_claim c ON c.id=o.coupon_claim_id WHERE u.id IS NULL OR (o.coupon_claim_id IS NOT NULL AND c.id IS NULL)`, `SELECT COUNT(*) FROM purchase_order o LEFT JOIN user_account u ON u.id=o.user_id LEFT JOIN coupon_claim c ON c.id=o.coupon_claim_id WHERE u.id IS NULL OR (o.coupon_claim_id IS NOT NULL AND c.id IS NULL)`},
		{"orphan", "order_item.references", `SELECT COUNT(*) FROM purchase_order_item i LEFT JOIN purchase_order o ON o.id=i.order_id LEFT JOIN game_edition e ON e.id=i.edition_id LEFT JOIN game g ON g.id=i.game_id WHERE o.id IS NULL OR e.id IS NULL OR g.id IS NULL OR e.game_id<>i.game_id`, `SELECT COUNT(*) FROM purchase_order_item i LEFT JOIN purchase_order o ON o.id=i.order_id LEFT JOIN game_edition e ON e.id=i.edition_id LEFT JOIN game g ON g.id=i.game_id WHERE o.id IS NULL OR e.id IS NULL OR g.id IS NULL OR e.game_id<>i.game_id`},
		{"orphan", "payment.order", `SELECT COUNT(*) FROM payment_record p LEFT JOIN purchase_order o ON o.id=p.order_id WHERE o.id IS NULL`, `SELECT COUNT(*) FROM payment_record p LEFT JOIN purchase_order o ON o.id=p.order_id WHERE o.id IS NULL`},
		{"orphan", "entitlement.references", `SELECT COUNT(*) FROM game_entitlement e LEFT JOIN user_account u ON u.id=e.user_id LEFT JOIN game_edition ge ON ge.id=e.edition_id LEFT JOIN purchase_order o ON o.id=e.source_order_id WHERE u.id IS NULL OR ge.id IS NULL OR (e.source_order_id IS NOT NULL AND o.id IS NULL)`, `SELECT COUNT(*) FROM game_entitlement e LEFT JOIN user_account u ON u.id=e.user_id LEFT JOIN game_edition ge ON ge.id=e.edition_id LEFT JOIN purchase_order o ON o.id=e.source_order_id WHERE u.id IS NULL OR ge.id IS NULL OR (e.source_order_id IS NOT NULL AND o.id IS NULL)`},
		{"orphan", "flash_sale_activity.references", `SELECT COUNT(*) FROM flash_sale_activity a LEFT JOIN game_edition e ON e.id=a.edition_id LEFT JOIN user_account u ON u.id=a.created_by WHERE e.id IS NULL OR u.id IS NULL`, `SELECT COUNT(*) FROM flash_sale_activity a LEFT JOIN game_edition e ON e.id=a.edition_id LEFT JOIN user_account u ON u.id=a.created_by WHERE e.id IS NULL OR u.id IS NULL`},
		{"orphan", "flash_sale_reservation.references", `SELECT COUNT(*) FROM flash_sale_reservation r LEFT JOIN flash_sale_activity a ON a.id=r.activity_id LEFT JOIN user_account u ON u.id=r.user_id LEFT JOIN purchase_order o ON o.id=r.order_id WHERE a.id IS NULL OR u.id IS NULL OR (r.order_id IS NOT NULL AND o.id IS NULL)`, `SELECT COUNT(*) FROM flash_sale_reservation r LEFT JOIN flash_sale_activity a ON a.id=r.activity_id LEFT JOIN user_account u ON u.id=r.user_id LEFT JOIN purchase_order o ON o.id=r.order_id WHERE a.id IS NULL OR u.id IS NULL OR (r.order_id IS NOT NULL AND o.id IS NULL)`},
		{"orphan", "flash_sale_release_job.references", `SELECT COUNT(*) FROM flash_sale_release_job j LEFT JOIN flash_sale_activity a ON a.id=j.activity_id LEFT JOIN user_account u ON u.id=j.user_id WHERE a.id IS NULL OR u.id IS NULL`, `SELECT COUNT(*) FROM flash_sale_release_job j LEFT JOIN flash_sale_activity a ON a.id=j.activity_id LEFT JOIN user_account u ON u.id=j.user_id WHERE a.id IS NULL OR u.id IS NULL`},
		{"unique", "session_token", `SELECT COUNT(*) FROM (SELECT token_hash FROM user_session GROUP BY token_hash HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT token_hash FROM user_session GROUP BY token_hash HAVING COUNT(*)>1) x`},
		{"unique", "player_profile.user", `SELECT COUNT(*) FROM (SELECT user_id FROM player_profile WHERE user_id IS NOT NULL GROUP BY user_id HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT user_id FROM player_profile WHERE user_id IS NOT NULL GROUP BY user_id HAVING COUNT(*)>1) x`},
		{"unique", "conversation_key", `SELECT COUNT(*) FROM (SELECT session_key FROM conversation_session GROUP BY session_key HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT session_key FROM conversation_session GROUP BY session_key HAVING COUNT(*)>1) x`},
		{"unique", "edition_game_code", `SELECT COUNT(*) FROM (SELECT game_id,code FROM game_edition GROUP BY game_id,code HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT game_id,code FROM game_edition GROUP BY game_id,code HAVING COUNT(*)>1) x`},
		{"unique", "price_exact_key", `SELECT COUNT(*) FROM (SELECT edition_id,region_code,currency,active_from FROM game_price GROUP BY edition_id,region_code,currency,active_from HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT edition_id,region_code,currency,active_from FROM game_price GROUP BY edition_id,region_code,currency,active_from HAVING COUNT(*)>1) x`},
		{"unique", "open_price", `SELECT COUNT(*) FROM (SELECT edition_id,region_code,currency FROM game_price WHERE active_until IS NULL GROUP BY edition_id,region_code,currency HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT edition_id,region_code,currency FROM game_price WHERE active_until IS NULL GROUP BY edition_id,region_code,currency HAVING COUNT(*)>1) x`},
		{"unique", "reaction_tuple", `SELECT COUNT(*) FROM (SELECT post_id,user_id,reaction_type FROM community_reaction GROUP BY post_id,user_id,reaction_type HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT post_id,user_id,reaction_type FROM community_reaction GROUP BY post_id,user_id,reaction_type HAVING COUNT(*)>1) x`},
		{"unique", "active_entitlement", `SELECT COUNT(*) FROM (SELECT user_id,edition_id FROM game_entitlement WHERE status='active' GROUP BY user_id,edition_id HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT user_id,edition_id FROM game_entitlement WHERE status='active' GROUP BY user_id,edition_id HAVING COUNT(*)>1) x`},
		{"unique", "claim_idempotency", `SELECT COUNT(*) FROM (SELECT user_id,idempotency_key FROM coupon_claim GROUP BY user_id,idempotency_key HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT user_id,idempotency_key FROM coupon_claim GROUP BY user_id,idempotency_key HAVING COUNT(*)>1) x`},
		{"unique", "order_identity", `SELECT (SELECT COUNT(*) FROM (SELECT order_no FROM purchase_order GROUP BY order_no HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT user_id,idempotency_key FROM purchase_order GROUP BY user_id,idempotency_key HAVING COUNT(*)>1) b)`, `SELECT (SELECT COUNT(*) FROM (SELECT order_no FROM purchase_order GROUP BY order_no HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT user_id,idempotency_key FROM purchase_order GROUP BY user_id,idempotency_key HAVING COUNT(*)>1) b)`},
		{"unique", "one_order_item_per_order", `SELECT COUNT(*) FROM (SELECT order_id FROM purchase_order_item GROUP BY order_id HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT order_id FROM purchase_order_item GROUP BY order_id HAVING COUNT(*)>1) x`},
		{"unique", "payment_identity", `SELECT (SELECT COUNT(*) FROM (SELECT provider,provider_reference FROM payment_record GROUP BY provider,provider_reference HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT order_id,idempotency_key FROM payment_record GROUP BY order_id,idempotency_key HAVING COUNT(*)>1) b)`, `SELECT (SELECT COUNT(*) FROM (SELECT provider,provider_reference FROM payment_record GROUP BY provider,provider_reference HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT order_id,idempotency_key FROM payment_record GROUP BY order_id,idempotency_key HAVING COUNT(*)>1) b)`},
		{"unique", "one_paid_payment_per_order", `SELECT COUNT(*) FROM (SELECT order_id FROM payment_record WHERE status='paid' GROUP BY order_id HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT order_id FROM payment_record WHERE status='paid' GROUP BY order_id HAVING COUNT(*)>1) x`},
		{"unique", "flash_sale_reservation_identity", `SELECT (SELECT COUNT(*) FROM (SELECT activity_id,user_id FROM flash_sale_reservation GROUP BY activity_id,user_id HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT activity_id,user_id,idempotency_digest FROM flash_sale_reservation GROUP BY activity_id,user_id,idempotency_digest HAVING COUNT(*)>1) b)+(SELECT COUNT(*) FROM (SELECT order_id FROM flash_sale_reservation WHERE order_id IS NOT NULL GROUP BY order_id HAVING COUNT(*)>1) c)`, `SELECT (SELECT COUNT(*) FROM (SELECT activity_id,user_id FROM flash_sale_reservation GROUP BY activity_id,user_id HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT activity_id,user_id,idempotency_digest FROM flash_sale_reservation GROUP BY activity_id,user_id,idempotency_digest HAVING COUNT(*)>1) b)+(SELECT COUNT(*) FROM (SELECT order_id FROM flash_sale_reservation WHERE order_id IS NOT NULL GROUP BY order_id HAVING COUNT(*)>1) c)`},
		{"unique", "flash_sale_release_request", `SELECT COUNT(*) FROM (SELECT request_id FROM flash_sale_release_job GROUP BY request_id HAVING COUNT(*)>1) x`, `SELECT COUNT(*) FROM (SELECT request_id FROM flash_sale_release_job GROUP BY request_id HAVING COUNT(*)>1) x`},
		{"format", "normalized_unique_identifiers", `SELECT (SELECT COUNT(*) FROM user_account WHERE user_name !~ '^[a-z0-9_]{3,32}$')+(SELECT COUNT(*) FROM game WHERE slug !~ '^[a-z0-9-]{3,64}$')+(SELECT COUNT(*) FROM game_edition WHERE code !~ '^[a-z0-9-]{2,64}$')+(SELECT COUNT(*) FROM coupon_campaign WHERE code !~ '^[A-Z0-9-]{3,32}$')+(SELECT COUNT(*) FROM coupon_definition WHERE code !~ '^[A-Z0-9-]{3,32}$')+(SELECT COUNT(*) FROM flash_sale_activity WHERE code !~ '^[A-Z0-9-]{3,64}$')`, `SELECT (SELECT COUNT(*) FROM user_account WHERE NOT REGEXP_LIKE(user_name,'^[a-z0-9_]{3,32}$','c'))+(SELECT COUNT(*) FROM game WHERE NOT REGEXP_LIKE(slug,'^[a-z0-9-]{3,64}$','c'))+(SELECT COUNT(*) FROM game_edition WHERE NOT REGEXP_LIKE(code,'^[a-z0-9-]{2,64}$','c'))+(SELECT COUNT(*) FROM coupon_campaign WHERE NOT REGEXP_LIKE(code,'^[A-Z0-9-]{3,32}$','c'))+(SELECT COUNT(*) FROM coupon_definition WHERE NOT REGEXP_LIKE(code,'^[A-Z0-9-]{3,32}$','c'))+(SELECT COUNT(*) FROM flash_sale_activity WHERE NOT REGEXP_LIKE(code,'^[A-Z0-9-]{3,64}$','c'))`},
		{"unique", "case_folded_identifiers", `SELECT (SELECT COUNT(*) FROM (SELECT lower(user_name) FROM user_account GROUP BY lower(user_name) HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT lower(slug) FROM game GROUP BY lower(slug) HAVING COUNT(*)>1) b)+(SELECT COUNT(*) FROM (SELECT lower(code) FROM coupon_campaign GROUP BY lower(code) HAVING COUNT(*)>1) c)+(SELECT COUNT(*) FROM (SELECT lower(code) FROM coupon_definition GROUP BY lower(code) HAVING COUNT(*)>1) d)+(SELECT COUNT(*) FROM (SELECT lower(code) FROM flash_sale_activity GROUP BY lower(code) HAVING COUNT(*)>1) e)`, `SELECT (SELECT COUNT(*) FROM (SELECT LOWER(user_name) FROM user_account GROUP BY LOWER(user_name) HAVING COUNT(*)>1) a)+(SELECT COUNT(*) FROM (SELECT LOWER(slug) FROM game GROUP BY LOWER(slug) HAVING COUNT(*)>1) b)+(SELECT COUNT(*) FROM (SELECT LOWER(code) FROM coupon_campaign GROUP BY LOWER(code) HAVING COUNT(*)>1) c)+(SELECT COUNT(*) FROM (SELECT LOWER(code) FROM coupon_definition GROUP BY LOWER(code) HAVING COUNT(*)>1) d)+(SELECT COUNT(*) FROM (SELECT LOWER(code) FROM flash_sale_activity GROUP BY LOWER(code) HAVING COUNT(*)>1) e)`},
		{"format", "region_currency", `SELECT (SELECT COUNT(*) FROM game_price WHERE region_code !~ '^[A-Z0-9-]{2,16}$' OR currency !~ '^[A-Z]{3}$')+(SELECT COUNT(*) FROM purchase_order WHERE region_code !~ '^[A-Z0-9-]{2,16}$' OR currency !~ '^[A-Z]{3}$')+(SELECT COUNT(*) FROM flash_sale_activity WHERE region_code !~ '^[A-Z0-9-]{2,16}$' OR currency !~ '^[A-Z]{3}$')`, `SELECT (SELECT COUNT(*) FROM game_price WHERE NOT REGEXP_LIKE(region_code,'^[A-Z0-9-]{2,16}$','c') OR NOT REGEXP_LIKE(currency,'^[A-Z]{3}$','c'))+(SELECT COUNT(*) FROM purchase_order WHERE NOT REGEXP_LIKE(region_code,'^[A-Z0-9-]{2,16}$','c') OR NOT REGEXP_LIKE(currency,'^[A-Z]{3}$','c'))+(SELECT COUNT(*) FROM flash_sale_activity WHERE NOT REGEXP_LIKE(region_code,'^[A-Z0-9-]{2,16}$','c') OR NOT REGEXP_LIKE(currency,'^[A-Z]{3}$','c'))`},
		{"format", "opaque_identifiers", `SELECT (SELECT COUNT(*) FROM user_session WHERE token_hash !~ '^[0-9a-f]{64}$')+(SELECT COUNT(*) FROM coupon_claim WHERE idempotency_key !~ '^[A-Za-z0-9._:-]{8,128}$')+(SELECT COUNT(*) FROM purchase_order WHERE order_no !~ '^ord_[a-f0-9]{32}$' OR idempotency_key !~ '^[A-Za-z0-9._:-]{8,128}$' OR (source_reference IS NOT NULL AND source_reference !~ '^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$'))+(SELECT COUNT(*) FROM payment_record WHERE idempotency_key !~ '^[A-Za-z0-9._:-]{8,128}$')+(SELECT COUNT(*) FROM flash_sale_reservation WHERE request_id !~ '^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$' OR octet_length(idempotency_digest)<>32)+(SELECT COUNT(*) FROM flash_sale_release_job WHERE request_id !~ '^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$' OR octet_length(idempotency_digest)<>32)`, `SELECT (SELECT COUNT(*) FROM user_session WHERE OCTET_LENGTH(token_hash)<>32)+(SELECT COUNT(*) FROM coupon_claim WHERE NOT REGEXP_LIKE(idempotency_key,'^[A-Za-z0-9._:-]{8,128}$','c'))+(SELECT COUNT(*) FROM purchase_order WHERE NOT REGEXP_LIKE(order_no,'^ord_[a-f0-9]{32}$','c') OR NOT REGEXP_LIKE(idempotency_key,'^[A-Za-z0-9._:-]{8,128}$','c') OR (source_reference IS NOT NULL AND NOT REGEXP_LIKE(source_reference,'^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$','c')))+(SELECT COUNT(*) FROM payment_record WHERE NOT REGEXP_LIKE(idempotency_key,'^[A-Za-z0-9._:-]{8,128}$','c'))+(SELECT COUNT(*) FROM flash_sale_reservation WHERE NOT REGEXP_LIKE(request_id,'^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$','c') OR OCTET_LENGTH(idempotency_digest)<>32)+(SELECT COUNT(*) FROM flash_sale_release_job WHERE NOT REGEXP_LIKE(request_id,'^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$','c') OR OCTET_LENGTH(idempotency_digest)<>32)`},
		{"format", "text_lengths_and_quantity", `SELECT (SELECT COUNT(*) FROM user_account WHERE display_name IS NOT NULL AND char_length(btrim(display_name)) NOT BETWEEN 1 AND 64)+(SELECT COUNT(*) FROM game WHERE char_length(btrim(name)) NOT BETWEEN 1 AND 160 OR char_length(summary)>500 OR char_length(description)>20000)+(SELECT COUNT(*) FROM game_edition WHERE char_length(btrim(name)) NOT BETWEEN 1 AND 160)+(SELECT COUNT(*) FROM community_post WHERE char_length(btrim(title)) NOT BETWEEN 1 AND 160 OR char_length(btrim(content)) NOT BETWEEN 1 AND 10000 OR octet_length(content)>65535)+(SELECT COUNT(*) FROM community_comment WHERE char_length(btrim(content)) NOT BETWEEN 1 AND 3000 OR octet_length(content)>65535)+(SELECT COUNT(*) FROM purchase_order_item WHERE quantity<>1)`, `SELECT (SELECT COUNT(*) FROM user_account WHERE display_name IS NOT NULL AND CHAR_LENGTH(TRIM(display_name)) NOT BETWEEN 1 AND 64)+(SELECT COUNT(*) FROM game WHERE CHAR_LENGTH(TRIM(name)) NOT BETWEEN 1 AND 160 OR CHAR_LENGTH(summary)>500 OR CHAR_LENGTH(description)>20000)+(SELECT COUNT(*) FROM game_edition WHERE CHAR_LENGTH(TRIM(name)) NOT BETWEEN 1 AND 160)+(SELECT COUNT(*) FROM community_post WHERE CHAR_LENGTH(TRIM(title)) NOT BETWEEN 1 AND 160 OR CHAR_LENGTH(TRIM(content)) NOT BETWEEN 1 AND 10000 OR OCTET_LENGTH(content)>65535)+(SELECT COUNT(*) FROM community_comment WHERE CHAR_LENGTH(TRIM(content)) NOT BETWEEN 1 AND 3000 OR OCTET_LENGTH(content)>65535)+(SELECT COUNT(*) FROM purchase_order_item WHERE quantity<>1)`},
		{"status", "accounts_catalog", `SELECT (SELECT COUNT(*) FROM user_account WHERE role NOT IN ('user','admin') OR status NOT IN ('active','disabled'))+(SELECT COUNT(*) FROM game WHERE status NOT IN ('active','inactive'))+(SELECT COUNT(*) FROM game_edition WHERE status NOT IN ('active','inactive'))`, `SELECT (SELECT COUNT(*) FROM user_account WHERE role NOT IN ('user','admin') OR status NOT IN ('active','disabled'))+(SELECT COUNT(*) FROM game WHERE status NOT IN ('active','inactive'))+(SELECT COUNT(*) FROM game_edition WHERE status NOT IN ('active','inactive'))`},
		{"status", "community", `SELECT (SELECT COUNT(*) FROM community_post WHERE status NOT IN ('published','hidden','deleted') OR ((status='deleted')<>(deleted_at IS NOT NULL)))+(SELECT COUNT(*) FROM community_comment WHERE status NOT IN ('published','hidden','deleted') OR ((status='deleted')<>(deleted_at IS NOT NULL)))+(SELECT COUNT(*) FROM community_reaction WHERE reaction_type NOT IN ('like','helpful','funny'))`, `SELECT (SELECT COUNT(*) FROM community_post WHERE status NOT IN ('published','hidden','deleted') OR ((status='deleted')<>(deleted_at IS NOT NULL)))+(SELECT COUNT(*) FROM community_comment WHERE status NOT IN ('published','hidden','deleted') OR ((status='deleted')<>(deleted_at IS NOT NULL)))+(SELECT COUNT(*) FROM community_reaction WHERE reaction_type NOT IN ('like','helpful','funny'))`},
		{"status", "commerce", `SELECT (SELECT COUNT(*) FROM coupon_campaign WHERE status NOT IN ('draft','active','paused','ended'))+(SELECT COUNT(*) FROM coupon_claim WHERE status NOT IN ('claimed','redeemed','expired'))+(SELECT COUNT(*) FROM purchase_order WHERE status NOT IN ('pending_payment','paid','cancelled','expired'))+(SELECT COUNT(*) FROM payment_record WHERE status NOT IN ('paid','failed'))+(SELECT COUNT(*) FROM game_entitlement WHERE status NOT IN ('active','revoked'))`, `SELECT (SELECT COUNT(*) FROM coupon_campaign WHERE status NOT IN ('draft','active','paused','ended'))+(SELECT COUNT(*) FROM coupon_claim WHERE status NOT IN ('claimed','redeemed','expired'))+(SELECT COUNT(*) FROM purchase_order WHERE status NOT IN ('pending_payment','paid','cancelled','expired'))+(SELECT COUNT(*) FROM payment_record WHERE status NOT IN ('paid','failed'))+(SELECT COUNT(*) FROM game_entitlement WHERE status NOT IN ('active','revoked'))`},
		{"status", "conversation_flashsale", `SELECT (SELECT COUNT(*) FROM conversation_message WHERE role NOT IN ('user','assistant'))+(SELECT COUNT(*) FROM flash_sale_activity WHERE status NOT IN ('draft','active','cancelled','ended'))+(SELECT COUNT(*) FROM flash_sale_reservation WHERE status NOT IN ('reserved','order_ready','failed','expired'))+(SELECT COUNT(*) FROM flash_sale_release_job WHERE status NOT IN ('pending','leased','done') OR reason NOT IN ('technical_rollback','final_guard','payment_expired','admin_repair'))`, `SELECT (SELECT COUNT(*) FROM conversation_message WHERE role NOT IN ('user','assistant'))+(SELECT COUNT(*) FROM flash_sale_activity WHERE status NOT IN ('draft','active','cancelled','ended'))+(SELECT COUNT(*) FROM flash_sale_reservation WHERE status NOT IN ('reserved','order_ready','failed','expired'))+(SELECT COUNT(*) FROM flash_sale_release_job WHERE status NOT IN ('pending','leased','done') OR reason NOT IN ('technical_rollback','final_guard','payment_expired','admin_repair'))`},
		{"inventory", "coupon_claimed_stock", `SELECT COUNT(*) FROM coupon_definition d WHERE d.claimed_stock<>(SELECT COUNT(*) FROM coupon_claim c WHERE c.coupon_id=d.id) OR d.claimed_stock<0 OR d.claimed_stock>d.total_stock`, `SELECT COUNT(*) FROM coupon_definition d WHERE d.claimed_stock<>(SELECT COUNT(*) FROM coupon_claim c WHERE c.coupon_id=d.id) OR d.claimed_stock<0 OR d.claimed_stock>d.total_stock`},
		{"claim", "redeemed_order_bidirectional", `SELECT (SELECT COUNT(*) FROM purchase_order o LEFT JOIN coupon_claim c ON c.id=o.coupon_claim_id WHERE o.coupon_claim_id IS NOT NULL AND (c.id IS NULL OR c.user_id<>o.user_id OR (o.status='paid' AND (c.status<>'redeemed' OR c.redeemed_order_id IS NULL OR c.redeemed_order_id<>o.id)) OR (o.status<>'paid' AND (c.status<>'claimed' OR c.redeemed_order_id IS NOT NULL))))+(SELECT COUNT(*) FROM coupon_claim c LEFT JOIN purchase_order o ON o.id=c.redeemed_order_id WHERE (c.status='redeemed' AND (c.redeemed_order_id IS NULL OR o.id IS NULL OR o.status<>'paid' OR o.coupon_claim_id IS NULL OR o.coupon_claim_id<>c.id OR o.user_id<>c.user_id)) OR (c.status<>'redeemed' AND c.redeemed_order_id IS NOT NULL))`, `SELECT (SELECT COUNT(*) FROM purchase_order o LEFT JOIN coupon_claim c ON c.id=o.coupon_claim_id WHERE o.coupon_claim_id IS NOT NULL AND (c.id IS NULL OR c.user_id<>o.user_id OR (o.status='paid' AND (c.status<>'redeemed' OR c.redeemed_order_id IS NULL OR c.redeemed_order_id<>o.id)) OR (o.status<>'paid' AND (c.status<>'claimed' OR c.redeemed_order_id IS NOT NULL))))+(SELECT COUNT(*) FROM coupon_claim c LEFT JOIN purchase_order o ON o.id=c.redeemed_order_id WHERE (c.status='redeemed' AND (c.redeemed_order_id IS NULL OR o.id IS NULL OR o.status<>'paid' OR o.coupon_claim_id IS NULL OR o.coupon_claim_id<>c.id OR o.user_id<>c.user_id)) OR (c.status<>'redeemed' AND c.redeemed_order_id IS NOT NULL))`},
		{"order", "financial_totals", `SELECT COUNT(*) FROM purchase_order o WHERE o.total_minor<>o.subtotal_minor-o.discount_minor OR o.subtotal_minor<>COALESCE((SELECT SUM(i.unit_price_minor*i.quantity) FROM purchase_order_item i WHERE i.order_id=o.id),0)`, `SELECT COUNT(*) FROM purchase_order o WHERE o.total_minor<>o.subtotal_minor-o.discount_minor OR o.subtotal_minor<>COALESCE((SELECT SUM(i.unit_price_minor*i.quantity) FROM purchase_order_item i WHERE i.order_id=o.id),0)`},
		{"payment", "paid_order_payment", `SELECT COUNT(*) FROM purchase_order o WHERE (o.status='paid')<>(EXISTS(SELECT 1 FROM payment_record p WHERE p.order_id=o.id AND p.status='paid' AND p.amount_minor=o.total_minor))`, `SELECT COUNT(*) FROM purchase_order o WHERE (o.status='paid')<>(EXISTS(SELECT 1 FROM payment_record p WHERE p.order_id=o.id AND p.status='paid' AND p.amount_minor=o.total_minor))`},
		{"entitlement", "paid_source_order", `SELECT COUNT(*) FROM game_entitlement e LEFT JOIN purchase_order o ON o.id=e.source_order_id LEFT JOIN purchase_order_item i ON i.order_id=o.id AND i.edition_id=e.edition_id WHERE e.source_order_id IS NOT NULL AND (o.status<>'paid' OR o.user_id<>e.user_id OR i.id IS NULL)`, `SELECT COUNT(*) FROM game_entitlement e LEFT JOIN purchase_order o ON o.id=e.source_order_id LEFT JOIN purchase_order_item i ON i.order_id=o.id AND i.edition_id=e.edition_id WHERE e.source_order_id IS NOT NULL AND (o.status<>'paid' OR o.user_id<>e.user_id OR i.id IS NULL)`},
		{"flashsale", "activity_allocated_stock", `SELECT COUNT(*) FROM flash_sale_activity a WHERE a.allocated_stock<>(SELECT COUNT(*) FROM flash_sale_reservation r WHERE r.activity_id=a.id AND r.status IN ('reserved','order_ready')) OR a.allocated_stock<0 OR a.allocated_stock>a.total_stock`, `SELECT COUNT(*) FROM flash_sale_activity a WHERE a.allocated_stock<>(SELECT COUNT(*) FROM flash_sale_reservation r WHERE r.activity_id=a.id AND r.status IN ('reserved','order_ready')) OR a.allocated_stock<0 OR a.allocated_stock>a.total_stock`},
		{"flashsale", "reservation_order", `SELECT COUNT(*) FROM flash_sale_reservation r LEFT JOIN purchase_order o ON o.id=r.order_id WHERE (r.status IN ('reserved','failed') AND r.order_id IS NOT NULL) OR (r.status IN ('order_ready','expired') AND (o.id IS NULL OR o.source_type<>'flash_sale' OR o.source_reference<>r.request_id OR o.user_id<>r.user_id))`, `SELECT COUNT(*) FROM flash_sale_reservation r LEFT JOIN purchase_order o ON o.id=r.order_id WHERE (r.status IN ('reserved','failed') AND r.order_id IS NOT NULL) OR (r.status IN ('order_ready','expired') AND (o.id IS NULL OR o.source_type<>'flash_sale' OR o.source_reference<>r.request_id OR o.user_id<>r.user_id))`},
		{"flashsale", "reservation_failure_code", `SELECT COUNT(*) FROM flash_sale_reservation WHERE (status='failed' AND (failure_code IS NULL OR char_length(btrim(failure_code))=0)) OR (status<>'failed' AND failure_code IS NOT NULL)`, `SELECT COUNT(*) FROM flash_sale_reservation WHERE (status='failed' AND (failure_code IS NULL OR CHAR_LENGTH(TRIM(failure_code))=0)) OR (status<>'failed' AND failure_code IS NOT NULL)`},
		{"flashsale", "release_job_identity", `SELECT COUNT(*) FROM flash_sale_release_job j LEFT JOIN flash_sale_reservation r ON r.request_id=j.request_id WHERE r.request_id IS NULL OR r.activity_id<>j.activity_id OR r.user_id<>j.user_id OR r.idempotency_digest<>j.idempotency_digest OR r.reserved_at<>j.reserved_at`, `SELECT COUNT(*) FROM flash_sale_release_job j LEFT JOIN flash_sale_reservation r ON r.request_id=j.request_id WHERE r.request_id IS NULL OR r.activity_id<>j.activity_id OR r.user_id<>j.user_id OR r.idempotency_digest<>j.idempotency_digest OR r.reserved_at<>j.reserved_at`},
		{"flashsale", "release_job_lifecycle", `SELECT COUNT(*) FROM flash_sale_release_job WHERE NOT ((status='pending' AND lease_until IS NULL AND completed_at IS NULL) OR (status='leased' AND lease_until IS NOT NULL AND completed_at IS NULL AND attempts>0) OR (status='done' AND lease_until IS NULL AND completed_at IS NOT NULL AND attempts>0))`, `SELECT COUNT(*) FROM flash_sale_release_job WHERE NOT ((status='pending' AND lease_until IS NULL AND completed_at IS NULL) OR (status='leased' AND lease_until IS NOT NULL AND completed_at IS NULL AND attempts>0) OR (status='done' AND lease_until IS NULL AND completed_at IS NOT NULL AND attempts>0))`},
		{"flashsale", "scope_lock_exact_set", `SELECT 0`, `SELECT (SELECT COUNT(*) FROM flash_sale_scope_lock s LEFT JOIN flash_sale_activity a ON a.edition_id=s.edition_id AND a.region_code=s.region_code AND a.currency=s.currency WHERE a.id IS NULL)+(SELECT COUNT(*) FROM (SELECT DISTINCT edition_id,region_code,currency FROM flash_sale_activity) a LEFT JOIN flash_sale_scope_lock s ON s.edition_id=a.edition_id AND s.region_code=a.region_code AND s.currency=a.currency WHERE s.edition_id IS NULL)`},
	}
}

func metrics() []metricSpec {
	return []metricSpec{
		{"inventory", "coupon.total_stock", `SELECT COALESCE(SUM(total_stock),0) FROM coupon_definition`, `SELECT COALESCE(SUM(total_stock),0) FROM coupon_definition`},
		{"inventory", "coupon.claimed_stock", `SELECT COALESCE(SUM(claimed_stock),0) FROM coupon_definition`, `SELECT COALESCE(SUM(claimed_stock),0) FROM coupon_definition`},
		{"claims", "claims.total", `SELECT COUNT(*) FROM coupon_claim`, `SELECT COUNT(*) FROM coupon_claim`},
		{"claims", "claims.redeemed", `SELECT COUNT(*) FROM coupon_claim WHERE status='redeemed'`, `SELECT COUNT(*) FROM coupon_claim WHERE status='redeemed'`},
		{"orders", "orders.total_minor", `SELECT COALESCE(SUM(total_minor),0) FROM purchase_order`, `SELECT COALESCE(SUM(total_minor),0) FROM purchase_order`},
		{"orders", "orders.paid", `SELECT COUNT(*) FROM purchase_order WHERE status='paid'`, `SELECT COUNT(*) FROM purchase_order WHERE status='paid'`},
		{"payments", "payments.paid_minor", `SELECT COALESCE(SUM(amount_minor),0) FROM payment_record WHERE status='paid'`, `SELECT COALESCE(SUM(amount_minor),0) FROM payment_record WHERE status='paid'`},
		{"entitlements", "entitlements.active", `SELECT COUNT(*) FROM game_entitlement WHERE status='active'`, `SELECT COUNT(*) FROM game_entitlement WHERE status='active'`},
		{"flashsale", "flashsale.total_stock", `SELECT COALESCE(SUM(total_stock),0) FROM flash_sale_activity`, `SELECT COALESCE(SUM(total_stock),0) FROM flash_sale_activity`},
		{"flashsale", "flashsale.allocated_stock", `SELECT COALESCE(SUM(allocated_stock),0) FROM flash_sale_activity`, `SELECT COALESCE(SUM(allocated_stock),0) FROM flash_sale_activity`},
		{"flashsale", "flashsale.reservations", `SELECT COUNT(*) FROM flash_sale_reservation`, `SELECT COUNT(*) FROM flash_sale_reservation`},
		{"flashsale", "flashsale.release_jobs", `SELECT COUNT(*) FROM flash_sale_release_job`, `SELECT COUNT(*) FROM flash_sale_release_job`},
	}
}

func sourceChecks(ctx context.Context, source *sourceSnapshot) ([]CheckResult, error) {
	result := make([]CheckResult, 0, len(invariants()))
	for _, check := range invariants() {
		var count int64
		if err := source.tx.QueryRow(ctx, check.sourceSQL).Scan(&count); err != nil {
			return nil, fmt.Errorf("run PostgreSQL check %s: %w", check.name, err)
		}
		result = append(result, CheckResult{Category: check.category, Name: check.name, SourceMismatches: count, Match: count == 0})
	}
	return result, nil
}

func buildReconciliation(ctx context.Context, source *sourceSnapshot, target targetReconciliationReader, specs []tableSpec, sourceInventory map[string]TableInventory, maxBytes int64, identitySHA string, deferredStatus DeferredStatus) (ReconciliationReport, error) {
	report := ReconciliationReport{SchemaVersion: ReportSchemaVersion, IdentitySHA256: identitySHA, GeneratedAt: nowUTC(), DeferredForeignKeys: deferredStatus}
	for _, table := range specs {
		sourceItem := sourceInventory[table.name]
		targetItem, err := target.inventory(ctx, table, sourceItem.Rows+1, maxBytes)
		if err != nil {
			return report, err
		}
		item := TableResult{Table: table.name, SourceRows: sourceItem.Rows, TargetRows: targetItem.Rows, SourceDigest: sourceItem.DigestSHA256, TargetDigest: targetItem.DigestSHA256}
		item.Match = item.SourceRows == item.TargetRows && item.SourceDigest == item.TargetDigest
		report.Tables = append(report.Tables, item)
	}
	for _, check := range invariants() {
		var sourceCount, targetCount int64
		if err := source.tx.QueryRow(ctx, check.sourceSQL).Scan(&sourceCount); err != nil {
			return report, fmt.Errorf("run PostgreSQL check %s: %w", check.name, err)
		}
		if err := target.QueryRowContext(ctx, check.targetSQL).Scan(&targetCount); err != nil {
			return report, fmt.Errorf("run MySQL check %s: %w", check.name, err)
		}
		report.Checks = append(report.Checks, CheckResult{Category: check.category, Name: check.name, SourceMismatches: sourceCount, TargetMismatches: targetCount, Match: sourceCount == 0 && targetCount == 0})
	}
	for _, metric := range metrics() {
		var sourceValue, targetValue int64
		if err := source.tx.QueryRow(ctx, metric.sourceSQL).Scan(&sourceValue); err != nil {
			return report, fmt.Errorf("read PostgreSQL metric %s: %w", metric.name, err)
		}
		if err := target.QueryRowContext(ctx, metric.targetSQL).Scan(&targetValue); err != nil {
			return report, fmt.Errorf("read MySQL metric %s: %w", metric.name, err)
		}
		report.Metrics = append(report.Metrics, MetricResult{Category: metric.category, Name: metric.name, Source: sourceValue, Target: targetValue, Match: sourceValue == targetValue})
	}
	finalizeReport(&report)
	return report, nil
}

func finalizeReport(report *ReconciliationReport) {
	report.MismatchCount = 0
	for _, table := range report.Tables {
		if !table.Match {
			report.MismatchCount++
		}
	}
	for _, check := range report.Checks {
		if !check.Match {
			report.MismatchCount++
		}
	}
	for _, metric := range report.Metrics {
		if !metric.Match {
			report.MismatchCount++
		}
	}
	if report.DeferredForeignKeys != DeferredComplete {
		report.MismatchCount++
	}
	report.CutoverReady = report.MismatchCount == 0
	report.DigestSHA256 = ""
	digest, err := digestJSON(report)
	if err == nil {
		report.DigestSHA256 = digest
	}
}

func verifyAutoIncrements(ctx context.Context, target sqlReader, specs []tableSpec) ([]CheckResult, error) {
	var result []CheckResult
	for _, table := range specs {
		if !table.autoIncrement {
			continue
		}
		var maximum int64
		if err := target.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM `"+table.name+"`").Scan(&maximum); err != nil {
			return nil, err
		}
		var next sql.NullInt64
		if err := target.QueryRowContext(ctx, `SELECT AUTO_INCREMENT FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, table.name).Scan(&next); err != nil {
			return nil, err
		}
		mismatch := int64(0)
		if !next.Valid || next.Int64 <= maximum {
			mismatch = 1
		}
		result = append(result, CheckResult{Category: "auto_increment", Name: table.name, TargetMismatches: mismatch, Match: mismatch == 0})
	}
	return result, nil
}
