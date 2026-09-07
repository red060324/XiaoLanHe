package postgrestomysql

import "strings"

type cellKind string

const (
	kindString cellKind = "string"
	kindInt    cellKind = "integer"
	kindTime   cellKind = "utc-time"
	kindDate   cellKind = "date"
	kindJSON   cellKind = "json"
	kindBinary cellKind = "binary"
)

type columnSpec struct {
	name               string
	sourceExpr         string
	sourceDependencies []string
	kind               cellKind
	deferred           bool
	initialValue       *Cell
}

type tableSpec struct {
	name          string
	sourceFrom    string
	columns       []columnSpec
	keyColumns    []int
	autoIncrement bool
}

func col(name string, kind cellKind) columnSpec {
	return columnSpec{name: name, sourceExpr: name, sourceDependencies: []string{name}, kind: kind}
}

func deferred(name string, kind cellKind) columnSpec {
	return columnSpec{name: name, sourceExpr: name, sourceDependencies: []string{name}, kind: kind, deferred: true}
}

func deferredWithInitial(name string, kind cellKind, value string) columnSpec {
	return columnSpec{
		name:               name,
		sourceExpr:         name,
		sourceDependencies: []string{name},
		kind:               kind,
		deferred:           true,
		initialValue:       &Cell{Kind: kind, Value: value},
	}
}

func expression(name, sourceExpr string, kind cellKind, sourceDependencies ...string) columnSpec {
	return columnSpec{name: name, sourceExpr: sourceExpr, sourceDependencies: append([]string(nil), sourceDependencies...), kind: kind}
}

func table(name string, columns []columnSpec, keys ...int) tableSpec {
	return tableSpec{name: name, sourceFrom: name, columns: columns, keyColumns: keys, autoIncrement: true}
}

func tables() []tableSpec {
	result := []tableSpec{
		table("user_account", []columnSpec{col("id", kindInt), col("user_name", kindString), col("display_name", kindString), col("password_hash", kindString), col("role", kindString), col("status", kindString), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("user_session", []columnSpec{col("id", kindInt), col("user_id", kindInt), expression("token_hash", "decode(token_hash, 'hex')", kindBinary, "token_hash"), col("expires_at", kindTime), col("last_seen_at", kindTime), col("revoked_at", kindTime), col("created_at", kindTime)}, 0),
		table("player_profile", []columnSpec{col("id", kindInt), col("user_id", kindInt), col("default_game", kindString), col("default_region", kindString), col("preferences", kindJSON), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("conversation_session", []columnSpec{col("id", kindInt), col("session_key", kindString), col("user_id", kindInt), col("title", kindString), col("metadata", kindJSON), col("summary_text", kindString), deferred("summary_through_message_id", kindInt), col("summary_prompt_version", kindString), col("summary_updated_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("conversation_message", []columnSpec{col("id", kindInt), col("session_id", kindInt), expression("message_key", "NULL::text", kindString), col("role", kindString), col("content", kindString), col("model_name", kindString), col("metadata", kindJSON), col("created_at", kindTime)}, 0),
		table("game", []columnSpec{col("id", kindInt), col("slug", kindString), col("name", kindString), col("summary", kindString), col("description", kindString), col("developer", kindString), col("publisher", kindString), col("release_date", kindDate), col("cover_url", kindString), col("status", kindString), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("game_edition", []columnSpec{col("id", kindInt), col("game_id", kindInt), col("code", kindString), col("name", kindString), col("description", kindString), col("status", kindString), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("game_price", []columnSpec{col("id", kindInt), col("edition_id", kindInt), col("region_code", kindString), col("currency", kindString), col("amount_minor", kindInt), col("active_from", kindTime), col("active_until", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("coupon_campaign", []columnSpec{col("id", kindInt), col("code", kindString), col("name", kindString), col("status", kindString), col("starts_at", kindTime), col("ends_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("coupon_definition", []columnSpec{col("id", kindInt), col("campaign_id", kindInt), col("code", kindString), col("name", kindString), col("discount_type", kindString), col("fixed_minor", kindInt), col("percentage_bps", kindInt), col("currency", kindString), col("minimum_minor", kindInt), col("total_stock", kindInt), col("claimed_stock", kindInt), col("per_user_limit", kindInt), col("game_id", kindInt), col("edition_id", kindInt), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("coupon_claim", []columnSpec{col("id", kindInt), col("coupon_id", kindInt), col("user_id", kindInt), deferredWithInitial("status", kindString, "claimed"), col("idempotency_key", kindString), col("claimed_at", kindTime), deferred("redeemed_order_id", kindInt), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("purchase_order", []columnSpec{col("id", kindInt), col("order_no", kindString), col("user_id", kindInt), col("status", kindString), col("currency", kindString), col("region_code", kindString), col("subtotal_minor", kindInt), col("discount_minor", kindInt), col("total_minor", kindInt), deferred("coupon_claim_id", kindInt), col("idempotency_key", kindString), col("source_type", kindString), col("source_reference", kindString), col("payment_expires_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("purchase_order_item", []columnSpec{col("id", kindInt), col("order_id", kindInt), col("edition_id", kindInt), col("game_id", kindInt), col("game_slug_snapshot", kindString), col("game_name_snapshot", kindString), col("edition_code_snapshot", kindString), col("edition_name_snapshot", kindString), col("unit_price_minor", kindInt), col("quantity", kindInt), col("created_at", kindTime)}, 0),
		table("payment_record", []columnSpec{col("id", kindInt), col("order_id", kindInt), col("provider", kindString), col("provider_reference", kindString), col("status", kindString), col("amount_minor", kindInt), col("idempotency_key", kindString), col("metadata", kindJSON), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("game_entitlement", []columnSpec{col("id", kindInt), col("user_id", kindInt), col("edition_id", kindInt), deferred("source_order_id", kindInt), col("status", kindString), col("granted_at", kindTime), col("created_at", kindTime)}, 0),
		table("community_post", []columnSpec{col("id", kindInt), col("author_id", kindInt), col("game_id", kindInt), col("title", kindString), col("content", kindString), col("status", kindString), col("deleted_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("community_comment", []columnSpec{col("id", kindInt), col("post_id", kindInt), col("author_id", kindInt), col("content", kindString), col("status", kindString), col("deleted_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		table("community_reaction", []columnSpec{col("id", kindInt), col("post_id", kindInt), col("user_id", kindInt), col("reaction_type", kindString), col("created_at", kindTime)}, 0),
		{
			name:       "flash_sale_scope_lock",
			sourceFrom: "(SELECT edition_id,region_code,currency,MIN(created_at) AS created_at FROM flash_sale_activity GROUP BY edition_id,region_code,currency) AS flash_sale_scope_lock",
			columns:    []columnSpec{col("edition_id", kindInt), col("region_code", kindString), col("currency", kindString), col("created_at", kindTime)},
			keyColumns: []int{0, 1, 2},
		},
		table("flash_sale_activity", []columnSpec{col("id", kindInt), col("code", kindString), col("edition_id", kindInt), col("region_code", kindString), col("currency", kindString), col("sale_price_minor", kindInt), col("total_stock", kindInt), col("allocated_stock", kindInt), col("status", kindString), col("starts_at", kindTime), col("ends_at", kindTime), col("payment_timeout_seconds", kindInt), col("version", kindInt), col("created_by", kindInt), col("activated_at", kindTime), col("cancelled_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
		{
			name: "flash_sale_reservation", sourceFrom: "flash_sale_reservation",
			columns:    []columnSpec{col("request_id", kindString), col("activity_id", kindInt), col("user_id", kindInt), col("idempotency_digest", kindBinary), col("status", kindString), col("order_id", kindInt), col("failure_code", kindString), col("reserved_at", kindTime), col("payment_expires_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)},
			keyColumns: []int{0},
		},
		table("flash_sale_release_job", []columnSpec{col("id", kindInt), col("request_id", kindString), col("activity_id", kindInt), col("user_id", kindInt), col("idempotency_digest", kindBinary), col("reserved_at", kindTime), col("reason", kindString), col("status", kindString), col("attempts", kindInt), col("next_attempt_at", kindTime), col("lease_until", kindTime), col("last_error_code", kindString), col("completed_at", kindTime), col("created_at", kindTime), col("updated_at", kindTime)}, 0),
	}
	return result
}

func (t tableSpec) sourceProjection() string {
	parts := make([]string, len(t.columns))
	for i, column := range t.columns {
		parts[i] = column.sourceExpr
	}
	return strings.Join(parts, ",")
}

func (t tableSpec) targetProjection() string {
	parts := make([]string, len(t.columns))
	for i, column := range t.columns {
		parts[i] = "`" + column.name + "`"
	}
	return strings.Join(parts, ",")
}

func (t tableSpec) keyNames(quoted bool) []string {
	result := make([]string, len(t.keyColumns))
	for i, index := range t.keyColumns {
		result[i] = t.columns[index].name
		if quoted {
			result[i] = "`" + result[i] + "`"
		}
	}
	return result
}

func (t tableSpec) initialRows(rows []Row) []Row {
	result := make([]Row, len(rows))
	for i := range rows {
		result[i] = append(Row(nil), rows[i]...)
		for column, spec := range t.columns {
			if spec.deferred {
				if spec.initialValue != nil {
					result[i][column] = *spec.initialValue
				} else {
					result[i][column] = Cell{Kind: spec.kind, Null: true}
				}
			}
		}
	}
	return result
}

func (t tableSpec) deferredColumns() []int {
	var result []int
	for i, column := range t.columns {
		if column.deferred {
			result = append(result, i)
		}
	}
	return result
}

func tableNames(specs []tableSpec) []string {
	result := make([]string, len(specs))
	for i, spec := range specs {
		result[i] = spec.name
	}
	return result
}
