package entity

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestCouponRemainingStock(t *testing.T) {
	if got := (Coupon{TotalStock: 10, ClaimedStock: 4}).RemainingStock(); got != 6 {
		t.Fatalf("remaining stock=%d", got)
	}
	if got := (Coupon{TotalStock: 10, ClaimedStock: 10}).RemainingStock(); got != 0 {
		t.Fatalf("exhausted stock=%d", got)
	}
}

func TestCouponValidateClaim(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	base := Coupon{CampaignStatus: "active", StartsAt: now, EndsAt: now.Add(time.Hour), TotalStock: 2}

	tests := []struct {
		name    string
		coupon  Coupon
		now     time.Time
		wantErr error
	}{
		{name: "start inclusive", coupon: base, now: now},
		{name: "inactive", coupon: withStatus(base, "paused"), now: now, wantErr: ErrUnavailable},
		{name: "not started", coupon: base, now: now.Add(-time.Nanosecond), wantErr: ErrUnavailable},
		{name: "end exclusive", coupon: base, now: base.EndsAt, wantErr: ErrUnavailable},
		{name: "exhausted", coupon: withClaimed(base, 2), now: now, wantErr: ErrExhausted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.coupon.ValidateClaim(tt.now); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error=%v want=%v", err, tt.wantErr)
			}
		})
	}
}

func TestCouponValidateUse(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	active := Coupon{CampaignStatus: "active", StartsAt: now, EndsAt: now.Add(time.Hour)}
	if err := active.ValidateUse(now); err != nil {
		t.Fatalf("active coupon error=%v", err)
	}
	for _, coupon := range []Coupon{withStatus(active, "paused"), {CampaignStatus: "active", StartsAt: now.Add(time.Second), EndsAt: now.Add(time.Hour)}, {CampaignStatus: "active", StartsAt: now.Add(-time.Hour), EndsAt: now}} {
		if err := coupon.ValidateUse(now); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("coupon=%+v error=%v", coupon, err)
		}
	}
}

func TestCouponDiscount(t *testing.T) {
	percentage := Coupon{DiscountType: DiscountPercentage, PercentageBps: 2500, Currency: "USD", MinimumMinor: 1000}
	fixed := Coupon{DiscountType: DiscountFixed, FixedMinor: 1200, Currency: "USD"}
	scoped := Coupon{DiscountType: DiscountFixed, FixedMinor: 500, Currency: "USD", GameID: 7, EditionID: 12}

	tests := []struct {
		name              string
		coupon            Coupon
		subtotal          int64
		currency          string
		gameID, editionID int64
		want              int64
		wantErr           error
	}{
		{name: "fixed", coupon: fixed, subtotal: 2000, currency: "USD", want: 1200},
		{name: "fixed capped at subtotal", coupon: fixed, subtotal: 500, currency: "USD", want: 500},
		{name: "percentage rounds down", coupon: percentage, subtotal: 1999, currency: "USD", want: 499},
		{name: "percentage avoids overflow", coupon: Coupon{DiscountType: DiscountPercentage, PercentageBps: 10000, Currency: "USD"}, subtotal: math.MaxInt64, currency: "USD", want: math.MaxInt64},
		{name: "minimum spend", coupon: percentage, subtotal: 999, currency: "USD", wantErr: ErrIneligible},
		{name: "currency mismatch", coupon: percentage, subtotal: 1000, currency: "CNY", wantErr: ErrIneligible},
		{name: "game mismatch", coupon: scoped, subtotal: 1000, currency: "USD", gameID: 8, editionID: 12, wantErr: ErrIneligible},
		{name: "edition mismatch", coupon: scoped, subtotal: 1000, currency: "USD", gameID: 7, editionID: 13, wantErr: ErrIneligible},
		{name: "matching scope", coupon: scoped, subtotal: 1000, currency: "USD", gameID: 7, editionID: 12, want: 500},
		{name: "zero total", coupon: fixed, subtotal: 0, currency: "USD", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.coupon.Discount(tt.subtotal, tt.currency, tt.gameID, tt.editionID)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("discount=%d error=%v want=%d,%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func withStatus(c Coupon, status string) Coupon  { c.CampaignStatus = status; return c }
func withClaimed(c Coupon, claimed int64) Coupon { c.ClaimedStock = claimed; return c }
