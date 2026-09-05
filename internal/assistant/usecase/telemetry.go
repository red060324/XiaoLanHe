package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

// LogRunEvent deliberately accepts only bounded operational fields. User text,
// prompts, profiles, evidence and provider payloads do not cross this API.
func LogRunEvent(ctx context.Context, event, runID, role, operation, outcome, stopReason string, started time.Time, usage entity.BudgetUsage, fields ...slog.Attr) {
	route, skillID := "", ""
	attributes := []any{
		"event", event,
		"run_id", runID,
		"agent_role", role,
		"operation", operation,
		"outcome", outcome,
		"stop_reason", stopReason,
		"latency_ms", time.Since(started).Milliseconds(),
		"model_calls", usage.ModelCalls,
		"tool_calls", usage.ToolCalls,
		"delegations", usage.Delegations,
	}
	for _, field := range fields {
		attributes = append(attributes, field)
		switch field.Key {
		case "route":
			route = field.Value.String()
		case "skill_id":
			skillID = field.Value.String()
		}
	}
	slog.InfoContext(ctx, "assistant operation completed", attributes...)
	platformmetrics.Default().ObserveAssistant(platformmetrics.AssistantObservation{
		Event: event, Role: role, Operation: operation, Outcome: outcome, StopReason: stopReason, Route: route, Skill: skillID,
		Duration: time.Since(started), ModelCalls: usage.ModelCalls, ToolCalls: usage.ToolCalls, Delegations: usage.Delegations,
	})
}

// RecordAssistantEvent records bounded intermediate events that do not carry a
// complete request budget. Correlation IDs remain in logs and never become
// metric labels.
func RecordAssistantEvent(event, role, operation, outcome, stopReason, route, skillID string, started time.Time) {
	platformmetrics.Default().ObserveAssistant(platformmetrics.AssistantObservation{
		Event: event, Role: role, Operation: operation, Outcome: outcome, StopReason: stopReason, Route: route, Skill: skillID, Duration: time.Since(started),
	})
}

// ErrorClass maps internal failures to a small, stable vocabulary suitable for
// aggregation. It intentionally never returns err.Error().
func ErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrModelBudget):
		return "model_budget"
	case errors.Is(err, ErrToolBudget):
		return "tool_budget"
	case errors.Is(err, ErrDelegationBudget):
		return "delegation_budget"
	case errors.Is(err, ErrDelegationCycle):
		return "delegation_cycle"
	case errors.Is(err, entity.ErrInvalidAgentContract):
		return "invalid_contract"
	default:
		return "dependency"
	}
}

func StopReason(err error) string {
	switch ErrorClass(err) {
	case "none":
		return "complete"
	case "cancelled":
		return "cancelled"
	case "deadline":
		return "deadline"
	case "model_budget":
		return "max_model_calls"
	case "tool_budget":
		return "max_tool_calls"
	case "delegation_budget":
		return "max_delegations"
	case "invalid_contract":
		return "invalid_output"
	default:
		return "dependency_unavailable"
	}
}
