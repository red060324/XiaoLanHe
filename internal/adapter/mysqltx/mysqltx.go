// Package mysqltx runs retry-safe MySQL business transactions.
package mysqltx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/go-sql-driver/mysql"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

const (
	defaultMaxAttempts = 3
	maxRetryDelay      = 25 * time.Millisecond

	lockWaitTimeout = 1205
	deadlockFound   = 1213
)

var telemetry = platformmetrics.Default()

// Options controls bounded retries. A zero value selects the production
// defaults. Backoff is primarily exposed so deterministic tests do not sleep.
type Options struct {
	MaxAttempts int
	Backoff     func(attempt int) time.Duration
}

// CommitOutcomeUnknownError marks a commit failure that must be reconciled by
// the caller using its durable idempotency key. Run never replays a transaction
// after Commit returns an error.
type CommitOutcomeUnknownError struct {
	Err error
}

func (e *CommitOutcomeUnknownError) Error() string {
	return fmt.Sprintf("mysql transaction commit outcome is unknown: %v", e.Err)
}

func (e *CommitOutcomeUnknownError) Unwrap() error { return e.Err }

// IsCommitOutcomeUnknown reports whether err requires durable reconciliation.
func IsCommitOutcomeUnknown(err error) bool {
	var target *CommitOutcomeUnknownError
	return errors.As(err, &target)
}

// IsRetryable reports whether MySQL aborted the current transaction because of
// a deadlock or a lock wait timeout. Other failures are never retried here.
func IsRetryable(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) &&
		(mysqlErr.Number == lockWaitTimeout || mysqlErr.Number == deadlockFound)
}

// Run executes fn in an explicit Read Committed transaction. fn must contain
// database work only: callers must not perform external side effects because fn
// can be invoked again after MySQL 1205 or 1213.
func Run[T any](ctx context.Context, db *sql.DB, fn func(*sql.Tx) (T, error)) (T, error) {
	return RunWithOptions(ctx, db, Options{}, fn)
}

// RunWithOptions is Run with an injectable retry policy. Every failed attempt is
// explicitly rolled back and discarded before a retry starts a new transaction.
func RunWithOptions[T any](ctx context.Context, db *sql.DB, options Options, fn func(*sql.Tx) (T, error)) (result T, resultErr error) {
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			telemetry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{
				Operation: "transaction", Outcome: "error", Duration: time.Since(started),
			})
			panic(recovered)
		}
		telemetry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{
			Operation: "transaction", Outcome: transactionOutcome(resultErr), Duration: time.Since(started),
		})
	}()

	var zero T
	if db == nil {
		return zero, errors.New("mysql transaction database is nil")
	}
	if fn == nil {
		return zero, errors.New("mysql transaction closure is nil")
	}

	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	backoff := options.Backoff
	if backoff == nil {
		backoff = retryBackoff
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return zero, err
		}

		value, err := invoke(tx, fn)
		if err == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				return zero, &CommitOutcomeUnknownError{Err: commitErr}
			}
			return value, nil
		}

		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return zero, errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		if !IsRetryable(err) {
			return zero, err
		}
		reason := retryReason(err)
		if attempt == maxAttempts {
			telemetry.ObserveMySQLRetry(platformmetrics.MySQLRetryObservation{Reason: reason, Outcome: "exhausted"})
			return zero, err
		}
		if waitErr := wait(ctx, backoff(attempt)); waitErr != nil {
			telemetry.ObserveMySQLRetry(platformmetrics.MySQLRetryObservation{Reason: reason, Outcome: "cancelled"})
			return zero, waitErr
		}
		telemetry.ObserveMySQLRetry(platformmetrics.MySQLRetryObservation{Reason: reason, Outcome: "scheduled"})
	}
	return zero, errors.New("mysql transaction retry loop exhausted")
}

func transactionOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case IsCommitOutcomeUnknown(err):
		return "commit_unknown"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "error"
	}
}

func retryReason(err error) string {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return "unknown"
	}
	switch mysqlErr.Number {
	case deadlockFound:
		return "deadlock"
	case lockWaitTimeout:
		return "lock_wait_timeout"
	default:
		return "unknown"
	}
}

func invoke[T any](tx *sql.Tx, fn func(*sql.Tx) (T, error)) (value T, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
	}()
	return fn(tx)
}

func retryBackoff(attempt int) time.Duration {
	capDelay := time.Duration(1<<min(attempt-1, 4)) * 2 * time.Millisecond
	if capDelay > maxRetryDelay {
		capDelay = maxRetryDelay
	}
	return time.Duration(rand.Int64N(int64(capDelay) + 1))
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
