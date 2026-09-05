package usecase

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestReleaseWorkerCompletesAndRetriesJobs(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	store := &fakeStore{releaseJobs: []ReleaseJob{
		{ID: 1, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, UserID: 7, IdempotencyDigest: testDigest, ReservedAt: now, Reason: "final_guard", Attempts: 1},
		{ID: 2, RequestID: "fsr_15_fedcba9876543210fedcba9876543210", ActivityID: 41, UserID: 8, IdempotencyDigest: testDigest, ReservedAt: now, Reason: "payment_expired", Attempts: 3},
	}}
	compensator := &fakeCompensator{errorsByRequest: map[string]error{store.releaseJobs[1].RequestID: errors.New("redis down")}}
	worker := NewReleaseWorker(store, compensator, 10, 30*time.Second)
	worker.now = func() time.Time { return now }

	completed, err := worker.RunOnce(context.Background())
	if err != nil || completed != 1 || len(store.completedReleaseJobs) != 1 || store.completedReleaseJobs[0] != 1 {
		t.Fatalf("completed=%d ids=%v err=%v", completed, store.completedReleaseJobs, err)
	}
	if store.retriedReleaseJobID != 2 || !store.retriedAt.Equal(now.Add(4*time.Second)) || store.retryCode != "redis_unavailable" {
		t.Fatalf("retry id=%d at=%s code=%s", store.retriedReleaseJobID, store.retriedAt, store.retryCode)
	}
	if compensator.commands[0].RemoveBuyer || compensator.commands[1].RemoveBuyer {
		t.Fatalf("commands=%+v", compensator.commands)
	}
}

func TestReleaseWorkerOnlyRemovesBuyerForTechnicalRollback(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStore{releaseJobs: []ReleaseJob{{
		ID: 1, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, UserID: 7,
		IdempotencyDigest: testDigest, ReservedAt: now, Reason: "technical_rollback",
	}}}
	compensator := &fakeCompensator{}
	if _, err := NewReleaseWorker(store, compensator, 10, time.Second).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(compensator.commands) != 1 || !compensator.commands[0].RemoveBuyer {
		t.Fatalf("commands=%+v", compensator.commands)
	}
}

func TestReleaseWorkerLogsSafeRetryMetadata(t *testing.T) {
	now := time.Now().UTC()
	job := ReleaseJob{
		ID: 1, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, UserID: 7,
		IdempotencyDigest: testDigest, ReservedAt: now, Reason: "final_guard", Attempts: 1,
	}
	secret := "redis://:super-secret@private-redis:6379/0"
	store := &fakeStore{releaseJobs: []ReleaseJob{job}}
	compensator := &fakeCompensator{errorsByRequest: map[string]error{job.RequestID: errors.New(secret)}}
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, err := NewReleaseWorker(store, compensator, 10, time.Second).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs := output.String()
	if strings.Contains(logs, secret) || !strings.Contains(logs, "outcome=dependency_unavailable") || !strings.Contains(logs, "attempt=1") {
		t.Fatalf("logs=%q", logs)
	}
}

func TestExpiryReaperUsesBoundedBatch(t *testing.T) {
	store := &fakeStore{expiredCount: 3}
	count, err := NewExpiryReaper(store, 10).RunOnce(context.Background())
	if err != nil || count != 3 || store.expiryBatch != 10 {
		t.Fatalf("count=%d batch=%d err=%v", count, store.expiryBatch, err)
	}
}

func TestRecoveryDispatcherClaimsAndPublishesBoundedStaleEvents(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	event := Event{Version: 1, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, ActivityVersion: 1, UserID: 7, ReservedAt: now, IdempotencyDigest: testDigest}
	store := &fakeStore{activity: activeActivity(now)}
	pending := &fakePendingRecovery{events: []Event{event}}
	publisher := &fakeEventPublisher{}
	dispatcher := NewRecoveryDispatcher(store, pending, publisher, 10, 30*time.Second, 20*time.Second)

	count, err := dispatcher.RunOnce(context.Background())
	if err != nil || count != 1 || len(publisher.events) != 1 || publisher.events[0] != event {
		t.Fatalf("count=%d events=%+v err=%v", count, publisher.events, err)
	}
	if pending.activityID != 41 || pending.limit != 10 || pending.stale != 30*time.Second || pending.lease != 20*time.Second {
		t.Fatalf("claim activity=%d limit=%d stale=%s lease=%s", pending.activityID, pending.limit, pending.stale, pending.lease)
	}
}

func TestRecoveryDispatcherStopsAfterPublishFailure(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Version: 1, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, ActivityVersion: 1, UserID: 7, ReservedAt: now, IdempotencyDigest: testDigest},
		{Version: 1, RequestID: "fsr_15_fedcba9876543210fedcba9876543210", ActivityID: 41, ActivityVersion: 1, UserID: 8, ReservedAt: now, IdempotencyDigest: testDigest},
	}
	publisher := &fakeEventPublisher{errAt: 1}
	dispatcher := NewRecoveryDispatcher(&fakeStore{activity: activeActivity(now)}, &fakePendingRecovery{events: events}, publisher, 10, 30*time.Second, 20*time.Second)

	count, err := dispatcher.RunOnce(context.Background())
	if err == nil || count != 1 || len(publisher.events) != 2 {
		t.Fatalf("count=%d calls=%d err=%v", count, len(publisher.events), err)
	}
}

type fakeCompensator struct {
	commands        []ReleaseCommand
	errorsByRequest map[string]error
}

type fakePendingRecovery struct {
	events            []Event
	activityID, limit int64
	stale, lease      time.Duration
}

func (f *fakePendingRecovery) ClaimStale(_ context.Context, activityID int64, stale, lease time.Duration, limit int) ([]Event, error) {
	f.activityID, f.limit, f.stale, f.lease = activityID, int64(limit), stale, lease
	return f.events, nil
}

type fakeEventPublisher struct {
	events []Event
	errAt  int
}

func (f *fakeEventPublisher) Publish(_ context.Context, event Event) error {
	f.events = append(f.events, event)
	if f.errAt > 0 && len(f.events) > f.errAt {
		return errors.New("mq unavailable")
	}
	return nil
}

func (f *fakeCompensator) Release(_ context.Context, command ReleaseCommand) (bool, error) {
	f.commands = append(f.commands, command)
	if err := f.errorsByRequest[command.RequestID]; err != nil {
		return false, err
	}
	return true, nil
}
