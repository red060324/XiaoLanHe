package rocketmq

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/apache/rocketmq-client-go/v2/primitive"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

type transactionListener struct {
	admission flashsale.Admission
	inspector flashsale.AdmissionInspector
	timeout   time.Duration
	results   sync.Map
}

func (l *transactionListener) ExecuteLocalTransaction(message *primitive.Message) primitive.LocalTransactionState {
	event, err := decodeEvent(message.Body)
	if err != nil {
		slog.Warn("flash sale transaction check rejected", "transaction_state", "rollback", "outcome", "invalid_event")
		return primitive.RollbackMessageState
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	result, err := l.admission.Reserve(ctx, flashsale.AdmissionCommand{
		RequestID: event.RequestID, ActivityID: event.ActivityID, ActivityVersion: event.ActivityVersion,
		UserID: event.UserID, IdempotencyDigest: event.IdempotencyDigest, ReservedAt: event.ReservedAt,
	})
	if err != nil {
		return primitive.UnknowState
	}
	l.results.Store(transactionResultKey(message), result)
	switch result.Outcome {
	case flashsale.AdmissionAccepted:
		return primitive.CommitMessageState
	case flashsale.AdmissionReplay, flashsale.AdmissionNotStarted, flashsale.AdmissionEnded, flashsale.AdmissionExhausted, flashsale.AdmissionAlreadyReserved:
		return primitive.RollbackMessageState
	default:
		return primitive.UnknowState
	}
}

func (l *transactionListener) CheckLocalTransaction(message *primitive.MessageExt) (state primitive.LocalTransactionState) {
	started := time.Now()
	defer func() {
		platformmetrics.Default().ObserveFlashSale("transaction_check", transactionStateName(state), time.Since(started), 1, 0)
	}()
	event, err := decodeEvent(message.Body)
	if err != nil {
		return primitive.RollbackMessageState
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	record, found, err := l.inspector.Lookup(ctx, event.RequestID, event.ActivityID)
	if err != nil {
		slog.Warn("flash sale transaction check deferred", "request_id", event.RequestID, "activity_id", event.ActivityID, "transaction_state", "unknown", "outcome", "dependency_unavailable")
		return primitive.UnknowState
	}
	if !found {
		slog.Info("flash sale transaction checked", "request_id", event.RequestID, "activity_id", event.ActivityID, "transaction_state", "rollback", "outcome", "marker_absent")
		return primitive.RollbackMessageState
	}
	if record.RequestID == event.RequestID && record.ActivityID == event.ActivityID && record.UserID == event.UserID &&
		record.IdempotencyDigest == event.IdempotencyDigest && record.Status == "queued" && record.ReservedAt.Equal(event.ReservedAt) {
		slog.Info("flash sale transaction checked", "request_id", event.RequestID, "activity_id", event.ActivityID, "transaction_state", "commit", "outcome", "marker_matched")
		return primitive.CommitMessageState
	}
	slog.Warn("flash sale transaction check rejected", "request_id", event.RequestID, "activity_id", event.ActivityID, "transaction_state", "rollback", "outcome", "marker_mismatch")
	return primitive.RollbackMessageState
}

func (l *transactionListener) takeResult(key string) (flashsale.AdmissionResult, bool) {
	value, ok := l.results.LoadAndDelete(key)
	if !ok {
		return flashsale.AdmissionResult{}, false
	}
	result, ok := value.(flashsale.AdmissionResult)
	return result, ok
}

func transactionResultKey(message *primitive.Message) string {
	if message == nil {
		return ""
	}
	return message.GetProperty(primitive.PropertyUniqueClientMessageIdKeyIndex)
}
