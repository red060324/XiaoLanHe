package usecase

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

func TestSafeTelemetryNeverLogsErrorContent(t *testing.T) {
	const canary = "CANARY_PRIVATE_PROMPT_AND_KEY"
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	err := errors.New("provider returned " + canary)
	LogRunEvent(context.Background(), "assistant.agent", "run-safe", "research", "retrieve", "error", StopReason(err), time.Now(), entity.BudgetUsage{ModelCalls: 1}, slog.String("error_class", ErrorClass(err)))

	logs := output.String()
	if strings.Contains(logs, canary) {
		t.Fatalf("sensitive error content reached telemetry: %s", logs)
	}
	for _, expected := range []string{`"event":"assistant.agent"`, `"run_id":"run-safe"`, `"error_class":"dependency"`, `"model_calls":1`} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("missing %s in %s", expected, logs)
		}
	}
}

func TestErrorClassIsBounded(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "none"}, {context.Canceled, "cancelled"}, {context.DeadlineExceeded, "deadline"},
		{ErrModelBudget, "model_budget"}, {ErrToolBudget, "tool_budget"},
		{ErrDelegationBudget, "delegation_budget"}, {ErrDelegationCycle, "delegation_cycle"},
		{entity.ErrInvalidAgentContract, "invalid_contract"}, {errors.New("arbitrary provider detail"), "dependency"},
	}
	for _, test := range cases {
		if got := ErrorClass(test.err); got != test.want {
			t.Fatalf("err=%v got=%s want=%s", test.err, got, test.want)
		}
	}
}
