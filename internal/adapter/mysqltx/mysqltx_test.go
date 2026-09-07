package mysqltx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

func TestRunRetriesRetryableErrorsWithFreshReadCommittedTransactions(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	var transactions []*sql.Tx

	value, err := RunWithOptions(context.Background(), db, Options{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
	}, func(tx *sql.Tx) (string, error) {
		transactions = append(transactions, tx)
		if len(transactions) == 1 {
			return "", fmt.Errorf("locked: %w", &mysql.MySQLError{Number: deadlockFound})
		}
		return "done", nil
	})
	if err != nil {
		t.Fatalf("RunWithOptions() error = %v", err)
	}
	if value != "done" {
		t.Fatalf("RunWithOptions() value = %q, want done", value)
	}
	if len(transactions) != 2 || transactions[0] == transactions[1] {
		t.Fatalf("transactions = %v, want two distinct transactions", transactions)
	}
	begins, commits, rollbacks, isolations := state.snapshot()
	if begins != 2 || commits != 1 || rollbacks != 1 {
		t.Fatalf("begin/commit/rollback = %d/%d/%d, want 2/1/1", begins, commits, rollbacks)
	}
	for _, isolation := range isolations {
		if isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
			t.Fatalf("isolation = %v, want ReadCommitted", isolation)
		}
	}
}

func TestRunBoundsLockWaitTimeoutRetries(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	attempts := 0

	_, err := RunWithOptions(context.Background(), db, Options{
		MaxAttempts: 2,
		Backoff:     func(int) time.Duration { return 0 },
	}, func(*sql.Tx) (struct{}, error) {
		attempts++
		return struct{}{}, &mysql.MySQLError{Number: lockWaitTimeout}
	})
	if !IsRetryable(err) {
		t.Fatalf("RunWithOptions() error = %v, want retryable lock timeout", err)
	}
	begins, commits, rollbacks, _ := state.snapshot()
	if attempts != 2 || begins != 2 || commits != 0 || rollbacks != 2 {
		t.Fatalf("attempt/begin/commit/rollback = %d/%d/%d/%d, want 2/2/0/2", attempts, begins, commits, rollbacks)
	}
}

func TestRunDoesNotRetryUnknownError(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	wantErr := errors.New("validation failed")
	attempts := 0

	_, err := RunWithOptions(context.Background(), db, Options{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
	}, func(*sql.Tx) (struct{}, error) {
		attempts++
		return struct{}{}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunWithOptions() error = %v, want %v", err, wantErr)
	}
	_, commits, rollbacks, _ := state.snapshot()
	if attempts != 1 || commits != 0 || rollbacks != 1 {
		t.Fatalf("attempt/commit/rollback = %d/%d/%d, want 1/0/1", attempts, commits, rollbacks)
	}
}

func TestRunDoesNotReplayAmbiguousCommit(t *testing.T) {
	commitErr := &mysql.MySQLError{Number: deadlockFound, Message: "commit failed"}
	state := &driverState{commitErr: commitErr}
	db := openTestDB(t, state)
	attempts := 0

	_, err := RunWithOptions(context.Background(), db, Options{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
	}, func(*sql.Tx) (struct{}, error) {
		attempts++
		return struct{}{}, nil
	})
	if !IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) {
		t.Fatalf("RunWithOptions() error = %v, want commit outcome unknown", err)
	}
	begins, commits, rollbacks, _ := state.snapshot()
	if attempts != 1 || begins != 1 || commits != 1 || rollbacks != 0 {
		t.Fatalf("attempt/begin/commit/rollback = %d/%d/%d/%d, want 1/1/1/0", attempts, begins, commits, rollbacks)
	}
}

func TestRunStopsWhenContextIsCancelledBetweenAttempts(t *testing.T) {
	state := &driverState{}
	db := openTestDB(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	_, err := RunWithOptions(ctx, db, Options{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return time.Hour },
	}, func(*sql.Tx) (struct{}, error) {
		attempts++
		cancel()
		return struct{}{}, &mysql.MySQLError{Number: deadlockFound}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunWithOptions() error = %v, want context canceled", err)
	}
	begins, commits, rollbacks, _ := state.snapshot()
	if attempts != 1 || begins != 1 || commits != 0 || rollbacks != 1 {
		t.Fatalf("attempt/begin/commit/rollback = %d/%d/%d/%d, want 1/1/0/1", attempts, begins, commits, rollbacks)
	}
}

func TestRunRecordsTransactionAndActualRetry(t *testing.T) {
	registry := useTestTelemetry(t)
	state := &driverState{}
	db := openTestDB(t, state)
	attempts := 0

	_, err := RunWithOptions(context.Background(), db, Options{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
	}, func(*sql.Tx) (struct{}, error) {
		attempts++
		if attempts == 1 {
			return struct{}{}, &mysql.MySQLError{Number: deadlockFound, Message: "CANARY_SQL_DSN_SECRET"}
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("RunWithOptions() error = %v", err)
	}
	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_mysql_operations_total{operation="transaction",outcome="success"} 1`,
		`xiaolanhe_mysql_operation_duration_seconds_count{operation="transaction",outcome="success"} 1`,
		`xiaolanhe_mysql_transaction_retry_events_total{reason="deadlock",outcome="scheduled"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "CANARY_SQL_DSN_SECRET") {
		t.Fatalf("database error text leaked to metrics:\n%s", output)
	}
}

func TestRunRecordsExhaustedRetry(t *testing.T) {
	registry := useTestTelemetry(t)
	db := openTestDB(t, &driverState{})

	_, err := RunWithOptions(context.Background(), db, Options{
		MaxAttempts: 1,
		Backoff:     func(int) time.Duration { return 0 },
	}, func(*sql.Tx) (struct{}, error) {
		return struct{}{}, &mysql.MySQLError{Number: lockWaitTimeout}
	})
	if !IsRetryable(err) {
		t.Fatalf("RunWithOptions() error = %v, want retryable error", err)
	}
	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_mysql_operations_total{operation="transaction",outcome="error"} 1`,
		`xiaolanhe_mysql_transaction_retry_events_total{reason="lock_wait_timeout",outcome="exhausted"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
	if strings.Contains(output, `outcome="scheduled"`) {
		t.Fatalf("final failed attempt was incorrectly recorded as scheduled:\n%s", output)
	}
}

func TestRunRecordsCancelledBackoff(t *testing.T) {
	registry := useTestTelemetry(t)
	db := openTestDB(t, &driverState{})
	ctx, cancel := context.WithCancel(context.Background())

	_, err := RunWithOptions(ctx, db, Options{
		MaxAttempts: 2,
		Backoff:     func(int) time.Duration { return time.Hour },
	}, func(*sql.Tx) (struct{}, error) {
		cancel()
		return struct{}{}, &mysql.MySQLError{Number: deadlockFound}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunWithOptions() error = %v, want context canceled", err)
	}
	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_mysql_operations_total{operation="transaction",outcome="cancelled"} 1`,
		`xiaolanhe_mysql_transaction_retry_events_total{reason="deadlock",outcome="cancelled"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
}

func TestRunRecordsCommitUnknownWithoutRetry(t *testing.T) {
	registry := useTestTelemetry(t)
	db := openTestDB(t, &driverState{commitErr: &mysql.MySQLError{Number: deadlockFound}})
	attempts := 0

	_, err := RunWithOptions(context.Background(), db, Options{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
	}, func(*sql.Tx) (struct{}, error) {
		attempts++
		return struct{}{}, nil
	})
	if !IsCommitOutcomeUnknown(err) || attempts != 1 {
		t.Fatalf("RunWithOptions() error/attempts = %v/%d, want commit unknown/1", err, attempts)
	}
	output := string(registry.Prometheus())
	if !strings.Contains(output, `xiaolanhe_mysql_operations_total{operation="transaction",outcome="commit_unknown"} 1`) {
		t.Fatalf("commit-unknown transaction metric missing:\n%s", output)
	}
	if strings.Contains(output, "xiaolanhe_mysql_transaction_retry_events_total{") {
		t.Fatalf("ambiguous commit was incorrectly recorded as a retry:\n%s", output)
	}
}

func TestRunRecordsPanickingTransactionAndPreservesPanic(t *testing.T) {
	registry := useTestTelemetry(t)
	db := openTestDB(t, &driverState{})

	defer func() {
		if recovered := recover(); recovered != "boom" {
			t.Fatalf("panic = %v, want boom", recovered)
		}
		output := string(registry.Prometheus())
		if !strings.Contains(output, `xiaolanhe_mysql_operations_total{operation="transaction",outcome="error"} 1`) {
			t.Fatalf("panicking transaction metric missing:\n%s", output)
		}
	}()
	_, _ = RunWithOptions(context.Background(), db, Options{}, func(*sql.Tx) (struct{}, error) {
		panic("boom")
	})
}

func useTestTelemetry(t *testing.T) *platformmetrics.Registry {
	t.Helper()
	registry := platformmetrics.NewRegistry()
	previous := telemetry
	telemetry = registry
	t.Cleanup(func() { telemetry = previous })
	return registry
}

func openTestDB(t *testing.T, state *driverState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(testConnector{state: state})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close test database: %v", err)
		}
	})
	return db
}

type testConnector struct{ state *driverState }

func (c testConnector) Connect(context.Context) (driver.Conn, error) {
	return &testConn{state: c.state}, nil
}

func (c testConnector) Driver() driver.Driver { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type testConn struct{ state *driverState }

func (*testConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (*testConn) Close() error                        { return nil }
func (c *testConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *testConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	c.state.begins++
	c.state.isolations = append(c.state.isolations, options.Isolation)
	c.state.mu.Unlock()
	return &testTx{state: c.state}, nil
}

type testTx struct{ state *driverState }

func (tx *testTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.commits++
	return tx.state.commitErr
}

func (tx *testTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rollbacks++
	return nil
}

type driverState struct {
	mu         sync.Mutex
	begins     int
	commits    int
	rollbacks  int
	isolations []driver.IsolationLevel
	commitErr  error
}

func (s *driverState) snapshot() (begins, commits, rollbacks int, isolations []driver.IsolationLevel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.begins, s.commits, s.rollbacks, append([]driver.IsolationLevel(nil), s.isolations...)
}
