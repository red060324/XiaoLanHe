package presenter

import (
	"errors"
	"testing"
	"time"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

func TestActivityRequestRejectsInvalidInput(t *testing.T) {
	_, err := (ActivityRequest{EditionID: "bad", StartsAt: "now", EndsAt: "later"}).Activity(0)
	if !errors.Is(err, flashsale.ErrInvalidInput) {
		t.Fatalf("err=%v", err)
	}
}

func TestPresentRequestUsesStringIDsAndUTC(t *testing.T) {
	deadline := time.Date(2026, 9, 3, 12, 15, 0, 0, time.FixedZone("CST", 8*60*60))
	result := PresentRequest(flashsale.Request{RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, Status: flashsale.RequestOrderReady, PaymentExpiresAt: deadline})
	if result.ActivityID != "41" || result.PaymentExpiresAt != "2026-09-03T04:15:00Z" {
		t.Fatalf("result=%+v", result)
	}
}
