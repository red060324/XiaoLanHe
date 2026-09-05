package entity

import (
	"errors"
	"testing"
	"time"
)

func TestActivityValidateDraft(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	valid := Activity{
		Code: "AUTUMN-DELUXE", EditionID: 7, Region: "CN", Currency: "CNY",
		SalePriceMinor: 9900, TotalStock: 100, StartsAt: now.Add(time.Minute),
		EndsAt: now.Add(time.Hour), PaymentTimeout: 15 * time.Minute,
	}
	if err := valid.ValidateDraft(now, 12900); err != nil {
		t.Fatalf("valid draft: %v", err)
	}

	cases := []Activity{
		{Code: "x", EditionID: 7, Region: "CN", Currency: "CNY", SalePriceMinor: 9900, TotalStock: 1, StartsAt: now.Add(time.Minute), EndsAt: now.Add(time.Hour), PaymentTimeout: time.Minute},
		{Code: valid.Code, EditionID: 0, Region: "CN", Currency: "CNY", SalePriceMinor: 9900, TotalStock: 1, StartsAt: now.Add(time.Minute), EndsAt: now.Add(time.Hour), PaymentTimeout: time.Minute},
		{Code: valid.Code, EditionID: 7, Region: "CN", Currency: "CNY", SalePriceMinor: 13000, TotalStock: 1, StartsAt: now.Add(time.Minute), EndsAt: now.Add(time.Hour), PaymentTimeout: time.Minute},
		{Code: valid.Code, EditionID: 7, Region: "CN", Currency: "CNY", SalePriceMinor: 9900, TotalStock: 0, StartsAt: now.Add(time.Minute), EndsAt: now.Add(time.Hour), PaymentTimeout: time.Minute},
		{Code: valid.Code, EditionID: 7, Region: "CN", Currency: "CNY", SalePriceMinor: 9900, TotalStock: 1, StartsAt: now.Add(time.Hour), EndsAt: now.Add(time.Minute), PaymentTimeout: time.Minute},
	}
	for i, item := range cases {
		if err := item.ValidateDraft(now, 12900); !errors.Is(err, ErrInvalidActivity) {
			t.Fatalf("case %d error=%v", i, err)
		}
	}
}

func TestActivityLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	activity := Activity{Status: StatusDraft, StartsAt: now.Add(time.Minute), EndsAt: now.Add(time.Hour)}
	if err := activity.Activate(now); err != nil || activity.Status != StatusActive || activity.Version != 1 {
		t.Fatalf("activate=%+v err=%v", activity, err)
	}
	if err := activity.Activate(now); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second activate error=%v", err)
	}
	cutoff := now.Add(2 * time.Minute)
	if err := activity.Cancel(cutoff); err != nil || activity.Status != StatusCancelled || !activity.CancelledAt.Equal(cutoff) {
		t.Fatalf("cancel=%+v err=%v", activity, err)
	}
	if err := activity.Cancel(cutoff); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
}

func TestActivityAcceptsReservationTime(t *testing.T) {
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	activity := Activity{Status: StatusCancelled, StartsAt: start, EndsAt: start.Add(time.Hour), CancelledAt: start.Add(10 * time.Minute)}
	if !activity.AcceptsReservationTime(start.Add(5 * time.Minute)) {
		t.Fatal("accepted pre-cancellation reservation was rejected")
	}
	if activity.AcceptsReservationTime(activity.CancelledAt.Add(time.Nanosecond)) {
		t.Fatal("post-cancellation reservation was accepted")
	}
	if activity.AcceptsReservationTime(activity.EndsAt) {
		t.Fatal("end boundary must be exclusive")
	}
}
