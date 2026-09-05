package usecase

import (
	"context"
	"log/slog"
	"time"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

type ExpiryReaper struct {
	store Store
	batch int
}

func NewExpiryReaper(store Store, batch int) *ExpiryReaper {
	return &ExpiryReaper{store: store, batch: batch}
}

func (w *ExpiryReaper) RunOnce(ctx context.Context) (processed int, resultErr error) {
	started := time.Now()
	defer func() {
		platformmetrics.Default().ObserveFlashSale("expiry", workerOutcome(resultErr, processed), time.Since(started), processed, 0)
	}()
	if w == nil || w.store == nil || w.batch < 1 || w.batch > 1000 {
		return 0, ErrInvalidInput
	}
	return w.store.ExpireDue(ctx, w.batch)
}

type ReleaseWorker struct {
	store       Store
	compensator Compensator
	batch       int
	lease       time.Duration
	now         func() time.Time
}

type RecoveryDispatcher struct {
	store     Store
	pending   PendingRecovery
	publisher EventPublisher
	batch     int
	stale     time.Duration
	lease     time.Duration
	cursor    string
}

func NewRecoveryDispatcher(store Store, pending PendingRecovery, publisher EventPublisher, batch int, stale, lease time.Duration) *RecoveryDispatcher {
	return &RecoveryDispatcher{store: store, pending: pending, publisher: publisher, batch: batch, stale: stale, lease: lease}
}

func (w *RecoveryDispatcher) RunOnce(ctx context.Context) (completed int, resultErr error) {
	started := time.Now()
	oldestAge := time.Duration(0)
	defer func() {
		platformmetrics.Default().ObserveFlashSale("recovery", workerOutcome(resultErr, completed), time.Since(started), completed, oldestAge)
	}()
	if w == nil || w.store == nil || w.pending == nil || w.publisher == nil || w.batch < 1 || w.batch > 1000 || w.stale <= 0 || w.lease <= 0 {
		return 0, ErrInvalidInput
	}
	activityLimit := w.batch
	if activityLimit > 50 {
		activityLimit = 50
	}
	activities, next, err := (&Service{store: w.store}).ListActivities(ctx, w.cursor, activityLimit)
	if err != nil {
		return 0, err
	}
	w.cursor = next
	if next == "" {
		w.cursor = ""
	}
	remaining := w.batch
	for _, activity := range activities {
		if remaining == 0 {
			break
		}
		events, err := w.pending.ClaimStale(ctx, activity.ID, w.stale, w.lease, remaining)
		if err != nil {
			return completed, err
		}
		for _, event := range events {
			if age := pendingAge(event.ReservedAt); age > oldestAge {
				oldestAge = age
			}
			if err := w.publisher.Publish(ctx, event); err != nil {
				slog.WarnContext(ctx, "flash sale recovery publish deferred", "request_id", event.RequestID, "activity_id", event.ActivityID, "outcome", "dependency_unavailable")
				return completed, err
			}
			slog.InfoContext(ctx, "flash sale recovery published", "request_id", event.RequestID, "activity_id", event.ActivityID, "outcome", "republished")
			completed++
			remaining--
		}
	}
	return completed, nil
}

func NewReleaseWorker(store Store, compensator Compensator, batch int, lease time.Duration) *ReleaseWorker {
	return &ReleaseWorker{store: store, compensator: compensator, batch: batch, lease: lease, now: time.Now}
}

func (w *ReleaseWorker) RunOnce(ctx context.Context) (completed int, resultErr error) {
	started := time.Now()
	oldestAge := time.Duration(0)
	defer func() {
		platformmetrics.Default().ObserveFlashSale("release", workerOutcome(resultErr, completed), time.Since(started), completed, oldestAge)
	}()
	if w == nil || w.store == nil || w.compensator == nil || w.batch < 1 || w.batch > 1000 || w.lease <= 0 {
		return 0, ErrInvalidInput
	}
	jobs, err := w.store.ClaimReleaseJobs(ctx, w.batch, w.lease)
	if err != nil {
		return 0, err
	}
	for _, job := range jobs {
		if age := pendingAge(job.ReservedAt); age > oldestAge {
			oldestAge = age
		}
		_, releaseErr := w.compensator.Release(ctx, ReleaseCommand{
			RequestID: job.RequestID, ActivityID: job.ActivityID, UserID: job.UserID,
			IdempotencyDigest: job.IdempotencyDigest, ReservedAt: job.ReservedAt, Reason: job.Reason,
			RemoveBuyer: job.Reason == "technical_rollback",
		})
		if releaseErr == nil {
			if err := w.store.CompleteReleaseJob(ctx, job.ID); err != nil {
				return completed, err
			}
			slog.InfoContext(ctx, "flash sale stock release completed", "request_id", job.RequestID, "activity_id", job.ActivityID, "reason", job.Reason, "outcome", "released")
			completed++
			continue
		}
		platformmetrics.Default().ObserveFlashSale("release_retry", "retry", time.Since(started), 1, pendingAge(job.ReservedAt))
		delay := releaseRetryDelay(job.Attempts)
		if err := w.store.RetryReleaseJob(ctx, job.ID, w.now().UTC().Add(delay), "redis_unavailable"); err != nil {
			return completed, err
		}
		slog.WarnContext(ctx, "flash sale stock release deferred", "request_id", job.RequestID, "activity_id", job.ActivityID, "reason", job.Reason, "attempt", job.Attempts, "outcome", "dependency_unavailable")
	}
	return completed, nil
}

func workerOutcome(err error, processed int) string {
	if err != nil {
		return flashSaleErrorOutcome(err)
	}
	if processed == 0 {
		return "empty"
	}
	return "success"
}

func releaseRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Second * time.Duration(1<<(attempt-1))
}
