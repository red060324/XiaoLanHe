package entity

import (
	"errors"
	"testing"
	"time"
)

func TestCalculateTotals(t *testing.T) {
	for _, test := range []struct {
		name               string
		subtotal, discount int64
		want               int64
		wantErr            error
	}{
		{name: "discounted", subtotal: 1999, discount: 399, want: 1600},
		{name: "free", subtotal: 1999, discount: 1999, want: 0},
		{name: "negative subtotal", subtotal: -1, wantErr: ErrInvalidTotals},
		{name: "negative discount", subtotal: 10, discount: -1, wantErr: ErrInvalidTotals},
		{name: "discount exceeds subtotal", subtotal: 10, discount: 11, wantErr: ErrInvalidTotals},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := CalculateTotals(test.subtotal, test.discount)
			if got != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("total=%d error=%v want=%d,%v", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestOrderValidatePayAt(t *testing.T) {
	deadline := time.Date(2026, 9, 3, 10, 15, 0, 0, time.UTC)
	order := Order{Status: StatusPendingPayment, PaymentExpiresAt: deadline}
	if err := order.ValidatePayAt(deadline.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("before deadline error=%v", err)
	}
	if err := order.ValidatePayAt(deadline); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("deadline error=%v", err)
	}
}

func TestOrderValidatePay(t *testing.T) {
	if err := (Order{Status: StatusPendingPayment}).ValidatePay(); err != nil {
		t.Fatalf("pending order error=%v", err)
	}
	for _, status := range []Status{StatusPaid, StatusCancelled, StatusExpired, ""} {
		if err := (Order{Status: status}).ValidatePay(); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("status=%q error=%v", status, err)
		}
	}
}
