package redis

import (
	"strings"
	"testing"
	"time"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

func TestActivityIDFromRequestID(t *testing.T) {
	activityID, err := ActivityIDFromRequestID("fsr_15_0123456789abcdef0123456789abcdef")
	if err != nil || activityID != 41 {
		t.Fatalf("activityID=%d err=%v", activityID, err)
	}
	for _, value := range []string{"", "fsr_0_0123456789abcdef0123456789abcdef", "fsr_15_short", "fsr_15_0123456789abcdef0123456789abcdeg"} {
		if _, err := ActivityIDFromRequestID(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestParseMarker(t *testing.T) {
	marker, err := parseMarker("fsr_15_0123456789abcdef0123456789abcdef", 41,
		"7|0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef|queued|1788429600000")
	if err != nil || marker.UserID != 7 || marker.Status != "queued" || !marker.ReservedAt.Equal(time.UnixMilli(1788429600000).UTC()) {
		t.Fatalf("marker=%+v err=%v", marker, err)
	}
	released, err := parseMarker("fsr_15_0123456789abcdef0123456789abcdef", 41,
		"7|0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef|released|1788429600000|final_guard")
	if err != nil || released.Status != "released" || released.FailureCode != "final_guard" || !released.ReservedAt.Equal(marker.ReservedAt) {
		t.Fatalf("released=%+v err=%v", released, err)
	}
	for _, value := range []string{
		"7|0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef|queued|1788429600000|extra",
		"7|0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef|released|1788429600000|unknown",
	} {
		if _, err := parseMarker("fsr_15_0123456789abcdef0123456789abcdef", 41, value); err == nil {
			t.Fatalf("expected marker %q to fail", value)
		}
	}
}

func TestMapAdmissionOutcome(t *testing.T) {
	cases := map[int64]flashsale.AdmissionOutcome{
		1: flashsale.AdmissionAccepted, 2: flashsale.AdmissionReplay, -1: flashsale.AdmissionNotStarted,
		-2: flashsale.AdmissionEnded, -3: flashsale.AdmissionExhausted, -4: flashsale.AdmissionAlreadyReserved,
		-5: flashsale.AdmissionUnavailable, 99: flashsale.AdmissionUnavailable,
	}
	for code, want := range cases {
		if got := mapAdmissionOutcome(code); got != want {
			t.Fatalf("code=%d got=%s want=%s", code, got, want)
		}
	}
}

func TestAdmissionScriptCapturesReleasedMarkerTimestampOnly(t *testing.T) {
	if !strings.Contains(admitLua, "|released|([0-9]+)|[^|]+$") {
		t.Fatal("released replay must capture the timestamp before the release reason")
	}
	if strings.Contains(admitLua, "tonumber(string.sub(request, separator + 1))") {
		t.Fatal("released replay must not parse the timestamp and reason as one number")
	}
}
