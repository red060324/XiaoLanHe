package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

func TestRegisterUsesLastInsertIDAndBinaryToken(t *testing.T) {
	expiresAt := time.Date(2026, 9, 7, 9, 8, 7, 654321000, time.FixedZone("test", 8*60*60))
	state := &testState{steps: []testStep{
		{queryContains: "insert into user_account", result: testResult{id: 41, affected: 1}},
		{queryContains: "insert into user_session", result: testResult{id: 9, affected: 1}},
	}}
	store := newTestStore(t, state)

	user, err := store.Register(context.Background(), "player_one", "Player One", "password-hash", strings.Repeat("ab", 32), expiresAt)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if user.ID != 41 || user.Username != "player_one" || user.DisplayName != "Player One" || user.Role != auth.RoleUser || user.Status != "active" {
		t.Fatalf("Register() user = %+v", user)
	}
	state.assertDone(t, 1, 1, 0)
	args := state.arguments(t, 1)
	assertDigest(t, args[1].Value, strings.Repeat("ab", 32))
	if got := args[2].Value; got != expiresAt.UTC() {
		t.Fatalf("expiresAt = %v, want %v", got, expiresAt.UTC())
	}
}

func TestRegisterReconcilesAmbiguousCommit(t *testing.T) {
	expiresAt := time.Date(2026, 9, 8, 12, 0, 0, 123456000, time.UTC)
	tokenHash := strings.Repeat("ab", 32)
	tokenDigest, _ := decodeTokenHash(tokenHash)
	commitErr := errors.New("connection lost during commit")
	state := &testState{commitErrors: []error{commitErr}, steps: []testStep{
		{queryContains: "insert into user_account", result: testResult{id: 41, affected: 1}},
		{queryContains: "insert into user_session", result: testResult{id: 51, affected: 1}},
		{queryContains: "from user_account", columns: registrationColumns(), rows: [][]driver.Value{{
			int64(41), "player_one", "Player One", "password-hash", "user", "active",
			int64(51), int64(41), tokenDigest, expiresAt,
		}}},
	}}
	store := newTestStore(t, state)

	user, err := store.Register(context.Background(), "player_one", "Player One", "password-hash", tokenHash, expiresAt)
	if err != nil || user.ID != 41 {
		t.Fatalf("Register() user=%+v error=%v, want reconciled success", user, err)
	}
	state.assertDone(t, 1, 1, 0)
}

func TestRegisterReconcilesAfterCallerCancellationWithDeadline(t *testing.T) {
	expiresAt := time.Date(2026, 9, 8, 12, 0, 0, 123456000, time.UTC)
	tokenHash := strings.Repeat("ab", 32)
	tokenDigest, _ := decodeTokenHash(tokenHash)
	commitErr := errors.New("connection lost during commit")
	ctx, cancel := context.WithCancel(context.Background())
	state := &testState{commitErrors: []error{commitErr}, onCommit: cancel, steps: []testStep{
		{queryContains: "insert into user_account", result: testResult{id: 41, affected: 1}},
		{queryContains: "insert into user_session", result: testResult{id: 51, affected: 1}},
		{queryContains: "from user_account", checkContext: freshReconciliationContext, columns: registrationColumns(), rows: [][]driver.Value{{
			int64(41), "player_one", "Player One", "password-hash", "user", "active",
			int64(51), int64(41), tokenDigest, expiresAt,
		}}},
	}}
	store := newTestStore(t, state)

	user, err := store.Register(ctx, "player_one", "Player One", "password-hash", tokenHash, expiresAt)
	if err != nil || user.ID != 41 {
		t.Fatalf("Register() user=%+v error=%v, want reconciled success", user, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("caller context error = %v, want canceled", ctx.Err())
	}
	state.assertDone(t, 1, 1, 0)
}

func TestRegisterPreservesUnknownOutcomeWhenReconciliationCannotConfirm(t *testing.T) {
	expiresAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	tokenHash := strings.Repeat("ab", 32)
	tokenDigest, _ := decodeTokenHash(tokenHash)
	commitErr := errors.New("connection lost during commit")
	tests := []struct {
		name string
		rows [][]driver.Value
		want error
	}{
		{name: "not committed"},
		{name: "mismatch", want: errDurableStateMismatch, rows: [][]driver.Value{{
			int64(41), "player_one", "Player One", "different-password", "user", "active",
			int64(51), int64(41), tokenDigest, expiresAt,
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &testState{commitErrors: []error{commitErr}, steps: []testStep{
				{queryContains: "insert into user_account", result: testResult{id: 41, affected: 1}},
				{queryContains: "insert into user_session", result: testResult{id: 51, affected: 1}},
				{queryContains: "from user_account", columns: registrationColumns(), rows: tt.rows},
			}}
			store := newTestStore(t, state)

			_, err := store.Register(context.Background(), "player_one", "Player One", "password-hash", tokenHash, expiresAt)
			if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) {
				t.Fatalf("Register() error=%v, want original unknown outcome", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Register() error=%v, want %v", err, tt.want)
			}
			state.assertDone(t, 1, 1, 0)
		})
	}
}

func TestRegisterRejectsUnexpectedRowsAffected(t *testing.T) {
	state := &testState{steps: []testStep{{queryContains: "insert into user_account", result: testResult{id: 41, affected: 0}}}}
	store := newTestStore(t, state)

	_, err := store.Register(context.Background(), "player_one", "Player One", "password-hash", strings.Repeat("ab", 32), time.Now())
	if err == nil || !strings.Contains(err.Error(), "affected 0 rows") {
		t.Fatalf("Register() error=%v, want affected-row failure", err)
	}
	state.assertDone(t, 1, 0, 1)
}

func TestRegisterMapsDuplicateAndRollsBack(t *testing.T) {
	state := &testState{steps: []testStep{{
		queryContains: "insert into user_account",
		err:           &drivermysql.MySQLError{Number: 1062, Message: "duplicate"},
	}}}
	store := newTestStore(t, state)

	_, err := store.Register(context.Background(), "player_one", "Player One", "password-hash", strings.Repeat("ab", 32), time.Now())
	if !errors.Is(err, account.ErrConflict) {
		t.Fatalf("Register() error = %v, want conflict", err)
	}
	state.assertDone(t, 1, 0, 1)
}

func TestRegisterRollsBackOnSessionOrLastInsertIDFailure(t *testing.T) {
	tests := []struct {
		name  string
		steps []testStep
		err   error
	}{
		{name: "session", err: errors.New("session insert failed"), steps: []testStep{
			{queryContains: "insert into user_account", result: testResult{id: 7, affected: 1}},
			{queryContains: "insert into user_session", err: errors.New("session insert failed")},
		}},
		{name: "last insert id", err: errors.New("last insert id failed"), steps: []testStep{
			{queryContains: "insert into user_account", result: testResult{affected: 1, idErr: errors.New("last insert id failed")}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &testState{steps: tt.steps}
			store := newTestStore(t, state)
			_, err := store.Register(context.Background(), "player", "Player", "password-hash", strings.Repeat("cd", 32), time.Now())
			if err == nil || err.Error() != tt.err.Error() {
				t.Fatalf("Register() error = %v, want %v", err, tt.err)
			}
			state.assertDone(t, 1, 0, 1)
		})
	}
}

func TestMalformedTokenIsRejectedBeforeDatabaseAccess(t *testing.T) {
	state := &testState{}
	store := newTestStore(t, state)
	for _, tokenHash := range []string{"short", strings.Repeat("zz", 32)} {
		if _, err := store.Register(context.Background(), "player", "Player", "hash", tokenHash, time.Now()); err == nil {
			t.Fatalf("Register(%q) error = nil", tokenHash)
		}
		if err := store.RevokeSession(context.Background(), tokenHash); err == nil {
			t.Fatalf("RevokeSession(%q) error = nil", tokenHash)
		}
	}
	state.assertDone(t, 0, 0, 0)
}

func TestFindCredentialMapsNoRowsAndEmptyPassword(t *testing.T) {
	state := &testState{steps: []testStep{
		{queryContains: "from user_account where user_name=?", columns: []string{"id", "user_name", "display_name", "role", "status", "password_hash"}},
		{queryContains: "from user_account where user_name=?", columns: []string{"id", "user_name", "display_name", "role", "status", "password_hash"}, rows: [][]driver.Value{{int64(4), "legacy", "Legacy", "user", "active", ""}}},
	}}
	store := newTestStore(t, state)
	for _, username := range []string{"missing", "legacy"} {
		_, _, err := store.FindCredential(context.Background(), username)
		if !errors.Is(err, account.ErrInvalidCredentials) {
			t.Fatalf("FindCredential(%q) error = %v", username, err)
		}
	}
	state.assertDone(t, 0, 0, 0)
}

func TestReplaceSessionCommitsWithBinaryTokens(t *testing.T) {
	expiresAt := time.Date(2026, 9, 8, 12, 0, 0, 123456000, time.FixedZone("test", -5*60*60))
	currentHash, newHash := strings.Repeat("01", 32), strings.Repeat("fe", 32)
	state := &testState{steps: []testStep{
		{queryContains: "update user_session", result: testResult{affected: 1}},
		{queryContains: "insert into user_session", result: testResult{id: 3, affected: 1}},
	}}
	store := newTestStore(t, state)

	if err := store.ReplaceSession(context.Background(), 12, currentHash, newHash, expiresAt); err != nil {
		t.Fatalf("ReplaceSession() error = %v", err)
	}
	state.assertDone(t, 1, 1, 0)
	assertDigest(t, state.arguments(t, 0)[0].Value, currentHash)
	insertArgs := state.arguments(t, 1)
	assertDigest(t, insertArgs[1].Value, newHash)
	if insertArgs[2].Value != expiresAt.UTC() {
		t.Fatalf("expiresAt = %v, want %v", insertArgs[2].Value, expiresAt.UTC())
	}
}

func TestReplaceSessionReconcilesAmbiguousCommitAndRejectsMismatch(t *testing.T) {
	expiresAt := time.Date(2026, 9, 8, 12, 0, 0, 123456000, time.UTC)
	currentHash, newHash := strings.Repeat("01", 32), strings.Repeat("fe", 32)
	newDigest, _ := decodeTokenHash(newHash)
	commitErr := errors.New("connection lost during commit")
	tests := []struct {
		name          string
		reconcileRows [][]driver.Value
		wantErr       error
	}{
		{name: "committed", reconcileRows: [][]driver.Value{{int64(51), int64(12), newDigest, expiresAt, true, true}}},
		{name: "not committed", wantErr: commitErr},
		{name: "mismatch", wantErr: errDurableStateMismatch, reconcileRows: [][]driver.Value{{int64(51), int64(99), newDigest, expiresAt, true, true}}},
		{name: "replacement revoked", wantErr: errDurableStateMismatch, reconcileRows: [][]driver.Value{{int64(51), int64(12), newDigest, expiresAt, false, true}}},
		{name: "prior token still active", wantErr: errDurableStateMismatch, reconcileRows: [][]driver.Value{{int64(51), int64(12), newDigest, expiresAt, true, false}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &testState{commitErrors: []error{commitErr}, steps: []testStep{
				{queryContains: "update user_session", result: testResult{affected: 1}},
				{queryContains: "insert into user_session", result: testResult{id: 51, affected: 1}},
				{queryContains: "from user_session where id=?", columns: []string{"id", "user_id", "token_hash", "expires_at", "replacement_active", "prior_inactive"}, rows: tt.reconcileRows},
			}}
			store := newTestStore(t, state)

			err := store.ReplaceSession(context.Background(), 12, currentHash, newHash, expiresAt)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("ReplaceSession() error=%v, want success", err)
			}
			if tt.wantErr != nil && (!mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, tt.wantErr)) {
				t.Fatalf("ReplaceSession() error=%v, want unknown including %v", err, tt.wantErr)
			}
			state.assertDone(t, 1, 1, 0)
			assertDigest(t, state.arguments(t, 2)[1].Value, currentHash)
			if got := state.arguments(t, 2)[2].Value; got != int64(51) {
				t.Fatalf("reconciliation session id = %v, want 51", got)
			}
		})
	}
}

func TestReplaceSessionAcceptsZeroChangedRowsForAlreadyRevokedCurrentToken(t *testing.T) {
	state := &testState{steps: []testStep{
		{queryContains: "update user_session", result: testResult{affected: 0}},
		{queryContains: "insert into user_session", result: testResult{id: 51, affected: 1}},
	}}
	store := newTestStore(t, state)

	err := store.ReplaceSession(context.Background(), 12, strings.Repeat("01", 32), strings.Repeat("fe", 32), time.Now())
	if err != nil {
		t.Fatalf("ReplaceSession() error=%v, want zero-row revoke accepted", err)
	}
	state.assertDone(t, 1, 1, 0)
}

func TestReplaceSessionRollsBackOnInsertFailure(t *testing.T) {
	wantErr := errors.New("insert failed")
	state := &testState{steps: []testStep{{queryContains: "insert into user_session", err: wantErr}}}
	store := newTestStore(t, state)
	err := store.ReplaceSession(context.Background(), 12, "", strings.Repeat("12", 32), time.Now())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReplaceSession() error = %v, want %v", err, wantErr)
	}
	state.assertDone(t, 1, 0, 1)
}

func TestFindSessionMapsNoRowsAndUsesBinaryToken(t *testing.T) {
	now := time.Date(2026, 9, 7, 13, 0, 0, 0, time.FixedZone("test", 8*60*60))
	tokenHash := strings.Repeat("34", 32)
	state := &testState{steps: []testStep{{queryContains: "from user_session", columns: []string{"id", "user_name", "display_name", "role"}}}}
	store := newTestStore(t, state)

	_, err := store.FindSession(context.Background(), tokenHash, now)
	if !errors.Is(err, account.ErrUnauthenticated) {
		t.Fatalf("FindSession() error = %v, want unauthenticated", err)
	}
	args := state.arguments(t, 0)
	assertDigest(t, args[0].Value, tokenHash)
	if args[1].Value != now.UTC() {
		t.Fatalf("now = %v, want %v", args[1].Value, now.UTC())
	}
}

func TestRevokeSessionReconcilesAmbiguousCommit(t *testing.T) {
	tokenHash := strings.Repeat("34", 32)
	commitErr := errors.New("connection lost during commit")
	tests := []struct {
		name        string
		rows        [][]driver.Value
		wantUnknown bool
	}{
		{name: "committed", rows: [][]driver.Value{{time.Now().UTC()}}},
		{name: "deleted is inactive"},
		{name: "not committed", rows: [][]driver.Value{{nil}}, wantUnknown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &testState{commitErrors: []error{commitErr}, steps: []testStep{
				{queryContains: "update user_session", result: testResult{affected: 1}},
				{queryContains: "select revoked_at", columns: []string{"revoked_at"}, rows: tt.rows},
			}}
			store := newTestStore(t, state)

			err := store.RevokeSession(context.Background(), tokenHash)
			if tt.wantUnknown && !mysqltx.IsCommitOutcomeUnknown(err) {
				t.Fatalf("RevokeSession() error=%v, want unknown outcome", err)
			}
			if !tt.wantUnknown && err != nil {
				t.Fatalf("RevokeSession() error=%v, want reconciled success", err)
			}
			state.assertDone(t, 1, 1, 0)
		})
	}
}

func TestRevokeSessionAcceptsZeroChangedRows(t *testing.T) {
	state := &testState{steps: []testStep{{queryContains: "update user_session", result: testResult{affected: 0}}}}
	store := newTestStore(t, state)

	if err := store.RevokeSession(context.Background(), strings.Repeat("34", 32)); err != nil {
		t.Fatalf("RevokeSession() error=%v, want idempotent success", err)
	}
	state.assertDone(t, 1, 1, 0)
}

func registrationColumns() []string {
	return []string{"id", "user_name", "display_name", "password_hash", "role", "status", "session_id", "session_user_id", "token_hash", "expires_at"}
}

func newTestStore(t *testing.T, state *testState) *Store {
	t.Helper()
	db := sql.OpenDB(testConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func assertDigest(t *testing.T, value driver.Value, hexValue string) {
	t.Helper()
	digest, ok := value.([]byte)
	if !ok || len(digest) != 32 {
		t.Fatalf("digest = %T(%v), want 32-byte []byte", value, value)
	}
	want, err := decodeTokenHash(hexValue)
	if err != nil || string(digest) != string(want) {
		t.Fatalf("digest = %x, want %x", digest, want)
	}
}

type testStep struct {
	queryContains string
	args          []driver.NamedValue
	result        driver.Result
	err           error
	columns       []string
	rows          [][]driver.Value
	checkContext  func(context.Context) error
}

type testState struct {
	mu                     sync.Mutex
	steps                  []testStep
	next                   int
	begins, commits, rolls int
	isolation              []driver.IsolationLevel
	commitErrors           []error
	onCommit               func()
}

func (s *testState) take(query string, args []driver.NamedValue) testStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.steps) {
		return testStep{err: errors.New("unexpected database call")}
	}
	step := s.steps[s.next]
	s.next++
	if !strings.Contains(strings.ToLower(query), strings.ToLower(step.queryContains)) {
		return testStep{err: errors.New("unexpected SQL: " + query)}
	}
	step.args = append([]driver.NamedValue(nil), args...)
	s.steps[s.next-1] = step
	return step
}

func (s *testState) arguments(t *testing.T, index int) []driver.NamedValue {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]driver.NamedValue(nil), s.steps[index].args...)
}

func (s *testState) assertDone(t *testing.T, begins, commits, rolls int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next != len(s.steps) || s.begins != begins || s.commits != commits || s.rolls != rolls {
		t.Fatalf("steps=%d/%d begin/commit/rollback=%d/%d/%d, want %d/%d/%d", s.next, len(s.steps), s.begins, s.commits, s.rolls, begins, commits, rolls)
	}
	for _, isolation := range s.isolation {
		if isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
			t.Fatalf("transaction isolation = %v, want ReadCommitted", isolation)
		}
	}
}

type testConnector struct{ state *testState }

func (c testConnector) Connect(context.Context) (driver.Conn, error) {
	return &testConn{state: c.state}, nil
}
func (c testConnector) Driver() driver.Driver { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type testConn struct{ state *testState }

func (*testConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (*testConn) Close() error                        { return nil }
func (c *testConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *testConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	c.state.begins++
	c.state.isolation = append(c.state.isolation, options.Isolation)
	c.state.mu.Unlock()
	return &testTx{state: c.state}, nil
}
func (c *testConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	step := c.state.take(query, args)
	if step.result == nil {
		step.result = testResult{affected: 1}
	}
	return step.result, step.err
}
func (c *testConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	step := c.state.take(query, args)
	if step.checkContext != nil {
		if err := step.checkContext(ctx); err != nil {
			return nil, err
		}
	}
	return &testRows{columns: step.columns, rows: step.rows}, step.err
}

type testTx struct{ state *testState }

func (tx *testTx) Commit() error {
	tx.state.mu.Lock()
	tx.state.commits++
	var err error
	if len(tx.state.commitErrors) > 0 {
		err = tx.state.commitErrors[0]
		tx.state.commitErrors = tx.state.commitErrors[1:]
	}
	onCommit := tx.state.onCommit
	tx.state.onCommit = nil
	tx.state.mu.Unlock()
	if onCommit != nil {
		onCommit()
	}
	return err
}
func (tx *testTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rolls++
	return nil
}

type testRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *testRows) Columns() []string { return r.columns }
func (*testRows) Close() error        { return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

type testResult struct {
	id, affected int64
	idErr        error
}

func (r testResult) LastInsertId() (int64, error) { return r.id, r.idErr }
func (r testResult) RowsAffected() (int64, error) { return r.affected, nil }

func freshReconciliationContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("reconciliation context is canceled: %w", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("reconciliation context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > reconciliationTimeout {
		return fmt.Errorf("reconciliation deadline remaining = %s, want (0,%s]", remaining, reconciliationTimeout)
	}
	return nil
}
