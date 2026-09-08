package mysql

import (
	"strings"
	"testing"
)

func TestPurchaseOrderItemSchemaEnforcesOneItemPerOrder(t *testing.T) {
	schema := migrationSQL(t, "016_create_purchase_order_item.sql")

	for _, fragment := range []string{
		"CONSTRAINT uk_purchase_order_item UNIQUE(order_id)",
		"KEY idx_purchase_order_item_edition(edition_id)",
		"CONSTRAINT ck_purchase_order_item_quantity CHECK(quantity=1)",
	} {
		if !strings.Contains(schema, fragment) {
			t.Errorf("purchase-order item schema is missing %q", fragment)
		}
	}
	if strings.Contains(schema, "UNIQUE(order_id,edition_id)") {
		t.Fatal("purchase-order item schema still permits multiple editions for one order")
	}
}

func TestPaymentRecordSchemaEnforcesAtMostOnePaidPaymentPerOrder(t *testing.T) {
	schema := migrationSQL(t, "017_create_payment_record.sql")

	for _, fragment := range []string{
		"paid_payment TINYINT GENERATED ALWAYS AS(IF(status='paid',1,NULL)) STORED",
		"CONSTRAINT uk_payment_record_one_paid UNIQUE(order_id,paid_payment)",
		"CONSTRAINT ck_payment_record_status CHECK(status IN ('paid','failed'))",
	} {
		if !strings.Contains(schema, fragment) {
			t.Errorf("payment-record schema is missing %q", fragment)
		}
	}

	if strings.Contains(schema, "IF(status<>'paid',1,NULL)") {
		t.Fatal("payment marker must leave non-paid history nullable")
	}
	if strings.Contains(schema, "'pending'") {
		t.Fatal("payment-record schema must not accept the unsupported pending status")
	}
}

func TestCommerceSchemasEnforceStateDependentNullability(t *testing.T) {
	tests := []struct {
		migration  string
		constraint string
	}{
		{
			"012_create_coupon_claim.sql",
			"CONSTRAINT ck_coupon_claim_redemption CHECK((status='redeemed' AND redeemed_order_id IS NOT NULL) OR (status IN ('claimed','expired') AND redeemed_order_id IS NULL))",
		},
		{
			"024_create_flash_sale_reservation.sql",
			"CONSTRAINT ck_flash_sale_reservation_failure CHECK((status='failed' AND failure_code IS NOT NULL AND CHAR_LENGTH(TRIM(failure_code))>0) OR (status<>'failed' AND failure_code IS NULL))",
		},
		{
			"025_create_flash_sale_release_job.sql",
			"CONSTRAINT ck_flash_sale_release_job_lifecycle CHECK((status='pending' AND lease_until IS NULL AND completed_at IS NULL) OR (status='leased' AND lease_until IS NOT NULL AND completed_at IS NULL AND attempts>0) OR (status='done' AND lease_until IS NULL AND completed_at IS NOT NULL AND attempts>0))",
		},
	}
	for _, test := range tests {
		t.Run(test.migration, func(t *testing.T) {
			schema := migrationSQL(t, test.migration)
			if !strings.Contains(schema, test.constraint) {
				t.Fatalf("%s is missing exact state constraint %q", test.migration, test.constraint)
			}
		})
	}
}

func TestFlashSaleSchemasContainReconciliationIndexes(t *testing.T) {
	for _, tc := range []struct {
		migration string
		index     string
	}{
		{"023_create_flash_sale_activity.sql", "KEY idx_flash_sale_activity_scope_window(edition_id,region_code,currency,status,starts_at,ends_at,id)"},
		{"025_create_flash_sale_release_job.sql", "KEY idx_flash_sale_release_job_expired(status,lease_until,id)"},
		{"027_add_flash_sale_release_claimable_index.sql", "ADD KEY idx_flash_sale_release_job_claimable(claimable_at,id)"},
	} {
		schema := migrationSQL(t, tc.migration)
		if !strings.Contains(schema, tc.index) {
			t.Errorf("%s is missing %q", tc.migration, tc.index)
		}
	}
}

func TestFlashSaleReleaseClaimableAtUsesForwardMigrations(t *testing.T) {
	if schema := migrationSQL(t, "025_create_flash_sale_release_job.sql"); strings.Contains(schema, "claimable_at") {
		t.Fatal("migration 025 is immutable; claimable_at must be introduced by a forward migration")
	}

	const columnDDL = "ALTER TABLE flash_sale_release_job ADD COLUMN claimable_at DATETIME(6) GENERATED ALWAYS AS(IF(status='pending',next_attempt_at,IF(status='leased',lease_until,NULL))) STORED;"
	if schema := strings.TrimSpace(migrationSQL(t, "026_add_flash_sale_release_claimable_at.sql")); schema != columnDDL {
		t.Fatalf("unexpected claimable_at migration:\n%s", schema)
	}

	const indexDDL = "ALTER TABLE flash_sale_release_job ADD KEY idx_flash_sale_release_job_claimable(claimable_at,id);"
	if schema := strings.TrimSpace(migrationSQL(t, "027_add_flash_sale_release_claimable_index.sql")); schema != indexDDL {
		t.Fatalf("unexpected claimable index migration:\n%s", schema)
	}
}

func TestOrderInvariantMigrationsRemainSingleStatement(t *testing.T) {
	for _, name := range []string{"016_create_purchase_order_item.sql", "017_create_payment_record.sql", "026_add_flash_sale_release_claimable_at.sql", "027_add_flash_sale_release_claimable_index.sql"} {
		schema := strings.TrimSpace(migrationSQL(t, name))
		withoutTerminator := strings.TrimSpace(strings.TrimSuffix(schema, ";"))
		if withoutTerminator == "" || strings.Contains(withoutTerminator, ";") {
			t.Errorf("%s must contain exactly one MySQL statement", name)
		}
	}
}

func migrationSQL(t *testing.T, name string) string {
	t.Helper()
	data, err := Files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
