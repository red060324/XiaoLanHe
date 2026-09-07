package postgrestomysql

import (
	"strings"
	"testing"
)

func TestCommerceStatusInvariantRejectsPendingPaymentRecords(t *testing.T) {
	const paymentStatusContract = "SELECT COUNT(*) FROM payment_record WHERE status NOT IN ('paid','failed')"

	var commerce invariantSpec
	for _, check := range invariants() {
		if check.category == "status" && check.name == "commerce" {
			commerce = check
			break
		}
	}
	if commerce.name == "" {
		t.Fatal("commerce status invariant is missing")
	}
	for name, query := range map[string]string{
		"PostgreSQL source": commerce.sourceSQL,
		"MySQL target":      commerce.targetSQL,
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, paymentStatusContract) {
				t.Fatalf("payment status contract missing from query: %s", query)
			}
			if strings.Contains(query, "payment_record WHERE status NOT IN ('pending'") {
				t.Fatalf("payment status contract still accepts pending: %s", query)
			}
		})
	}
}

func TestCouponRedemptionInvariantModelsOrderLifecycle(t *testing.T) {
	check := findInvariant(t, "claim", "redeemed_order_bidirectional")
	for name, query := range invariantQueries(check) {
		t.Run(name, func(t *testing.T) {
			for _, fragment := range []string{
				"FROM purchase_order o LEFT JOIN coupon_claim c ON c.id=o.coupon_claim_id",
				"o.coupon_claim_id IS NOT NULL",
				"c.user_id<>o.user_id",
				"o.status='paid' AND (c.status<>'redeemed'",
				"c.redeemed_order_id<>o.id",
				"o.status<>'paid' AND (c.status<>'claimed'",
				"FROM coupon_claim c LEFT JOIN purchase_order o ON o.id=c.redeemed_order_id",
				"c.status='redeemed'",
				"o.status<>'paid'",
				"o.coupon_claim_id<>c.id",
				"o.user_id<>c.user_id",
				"c.status<>'redeemed' AND c.redeemed_order_id IS NOT NULL",
			} {
				if !strings.Contains(query, fragment) {
					t.Errorf("coupon redemption contract is missing %q: %s", fragment, query)
				}
			}
			if strings.Contains(query, "FULL JOIN") {
				t.Fatalf("coupon redemption mismatch count must have portable two-way semantics: %s", query)
			}
		})
	}
}

func TestReservationFailureCodeInvariantAppliesToBothDialects(t *testing.T) {
	check := findInvariant(t, "flashsale", "reservation_failure_code")
	want := map[string][]string{
		"PostgreSQL source": {"status='failed'", "failure_code IS NULL", "char_length(btrim(failure_code))=0", "status<>'failed' AND failure_code IS NOT NULL"},
		"MySQL target":      {"status='failed'", "failure_code IS NULL", "CHAR_LENGTH(TRIM(failure_code))=0", "status<>'failed' AND failure_code IS NOT NULL"},
	}
	for name, query := range invariantQueries(check) {
		t.Run(name, func(t *testing.T) {
			for _, fragment := range want[name] {
				if !strings.Contains(query, fragment) {
					t.Errorf("reservation failure contract is missing %q: %s", fragment, query)
				}
			}
		})
	}
}

func TestReleaseJobLifecycleInvariantAppliesToBothDialects(t *testing.T) {
	check := findInvariant(t, "flashsale", "release_job_lifecycle")
	for name, query := range invariantQueries(check) {
		t.Run(name, func(t *testing.T) {
			for _, fragment := range []string{
				"status='pending' AND lease_until IS NULL AND completed_at IS NULL",
				"status='leased' AND lease_until IS NOT NULL AND completed_at IS NULL AND attempts>0",
				"status='done' AND lease_until IS NULL AND completed_at IS NOT NULL AND attempts>0",
			} {
				if !strings.Contains(query, fragment) {
					t.Errorf("release-job lifecycle contract is missing %q: %s", fragment, query)
				}
			}
		})
	}
}

func findInvariant(t *testing.T, category, name string) invariantSpec {
	t.Helper()
	for _, check := range invariants() {
		if check.category == category && check.name == name {
			return check
		}
	}
	t.Fatalf("invariant %s/%s is missing", category, name)
	return invariantSpec{}
}

func invariantQueries(check invariantSpec) map[string]string {
	return map[string]string{
		"PostgreSQL source": check.sourceSQL,
		"MySQL target":      check.targetSQL,
	}
}
