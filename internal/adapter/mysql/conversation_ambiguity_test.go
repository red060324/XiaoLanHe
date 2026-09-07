package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestFindOrCreateSessionReconcilesAmbiguousCommit(t *testing.T) {
	commitErr := errors.New("connection lost during commit")
	tests := []struct {
		name      string
		owner     any
		wantID    int64
		wantCause error
		empty     bool
	}{
		{name: "committed matching owner", owner: int64(7), wantID: 41},
		{name: "not committed", empty: true, wantCause: commitErr},
		{name: "owner mismatch", owner: int64(8), wantCause: errConversationDurableStateMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newConversationMockStore(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \? FOR UPDATE`).
				WithArgs("session-key").
				WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(41), int64(7)))
			mock.ExpectExec("UPDATE conversation_session").
				WithArgs(int64(7), int64(41)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit().WillReturnError(commitErr)
			reconcileRows := sqlmock.NewRows([]string{"id", "user_id"})
			if !tt.empty {
				reconcileRows.AddRow(int64(41), tt.owner)
			}
			mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \?`).
				WithArgs("session-key").
				WillReturnRows(reconcileRows)

			id, err := store.FindOrCreateSession(context.Background(), "session-key", 7)
			if tt.wantCause == nil {
				if err != nil || id != tt.wantID {
					t.Fatalf("FindOrCreateSession()=(%d,%v), want (%d,nil)", id, err, tt.wantID)
				}
				return
			}
			if id != 0 || !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, tt.wantCause) {
				t.Fatalf("FindOrCreateSession()=(%d,%v), want zero and unknown including %v", id, err, tt.wantCause)
			}
		})
	}
}

func TestFindOrCreateSessionReconcilesAmbiguousInsertCommit(t *testing.T) {
	store, mock := newConversationMockStore(t)
	commitErr := errors.New("connection lost during commit")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \? FOR UPDATE`).
		WithArgs("new-session").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO conversation_session").
		WithArgs("new-session", int64(7)).
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectCommit().WillReturnError(commitErr)
	mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \?`).
		WithArgs("new-session").
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(41), int64(7)))

	id, err := store.FindOrCreateSession(context.Background(), "new-session", 7)
	if err != nil || id != 41 {
		t.Fatalf("FindOrCreateSession()=(%d,%v), want reconciled insert", id, err)
	}
}

func TestFindOrCreateSessionReconcilesAmbiguousCommitAfterRequestCancellation(t *testing.T) {
	store, mock := newConversationMockStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commitErr := &cancelAfterCommitError{cancel: cancel}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \? FOR UPDATE`).
		WithArgs("session-key").
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(41), int64(7)))
	mock.ExpectExec("UPDATE conversation_session").
		WithArgs(int64(7), int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(commitErr)
	mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \?`).
		WithArgs("session-key").
		WillDelayFor(time.Millisecond).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(41), int64(7)))

	id, err := store.FindOrCreateSession(ctx, "session-key", 7)
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("request context error = %v, want canceled after commit", ctx.Err())
	}
	if err != nil || id != 41 {
		t.Fatalf("FindOrCreateSession()=(%d,%v), want exact durable session after cancellation", id, err)
	}
}

func TestFindOrCreateSessionAcceptsZeroChangedRows(t *testing.T) {
	store, mock := newConversationMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \? FOR UPDATE`).
		WithArgs("session-key").
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(41), int64(7)))
	mock.ExpectExec("UPDATE conversation_session").
		WithArgs(int64(7), int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	id, err := store.FindOrCreateSession(context.Background(), "session-key", 7)
	if err != nil || id != 41 {
		t.Fatalf("FindOrCreateSession()=(%d,%v), want (41,nil)", id, err)
	}
}

func TestFindOrCreateSessionDuplicateRaceRetriesAndPreservesOwnership(t *testing.T) {
	store, mock := newConversationMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \? FOR UPDATE`).
		WithArgs("raced").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO conversation_session").
		WithArgs("raced", int64(7)).
		WillReturnError(&drivermysql.MySQLError{Number: 1062, Message: "duplicate"})
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, user_id FROM conversation_session WHERE session_key = \? FOR UPDATE`).
		WithArgs("raced").
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(41), int64(8)))
	mock.ExpectRollback()

	id, err := store.FindOrCreateSession(context.Background(), "raced", 7)
	if id != 0 || !errors.Is(err, usecase.ErrConversationForbidden) {
		t.Fatalf("FindOrCreateSession()=(%d,%v), want forbidden", id, err)
	}
}

func TestSaveMessageReconcilesAmbiguousCommitWithoutReplaying(t *testing.T) {
	const key = "11111111-1111-4111-8111-111111111111"
	commitErr := errors.New("connection lost during commit")
	for _, test := range []struct {
		name       string
		rows       *sqlmock.Rows
		wantOK     bool
		wantReason string
	}{
		{name: "committed exact payload", rows: messageRows().AddRow("assistant", "answer", "model"), wantOK: true},
		{name: "not committed", rows: messageRows(), wantReason: "commit outcome is unknown"},
		{name: "different role", rows: messageRows().AddRow("user", "answer", "model"), wantReason: "different payload"},
		{name: "different content", rows: messageRows().AddRow("assistant", "other", "model"), wantReason: "different payload"},
		{name: "different model", rows: messageRows().AddRow("assistant", "answer", "other"), wantReason: "different payload"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newConversationMockStore(t)
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO conversation_message").
				WithArgs(int64(41), key, "assistant", "answer", "model").
				WillReturnResult(sqlmock.NewResult(51, 1))
			mock.ExpectCommit().WillReturnError(commitErr)
			mock.ExpectQuery(`SELECT role, content, model_name FROM conversation_message WHERE session_id = \? AND message_key = \?`).
				WithArgs(int64(41), key).WillReturnRows(test.rows)

			err := store.SaveMessageWithKey(context.Background(), 41, key, "assistant", "answer", "model")
			if test.wantOK {
				if err != nil {
					t.Fatalf("SaveMessageWithKey() error=%v, want reconciled success", err)
				}
				return
			}
			if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("SaveMessageWithKey() error=%v, want unknown outcome with %q", err, test.wantReason)
			}
		})
	}
}

func TestSaveMessageReconcilesDuplicateKeyByExactPayload(t *testing.T) {
	const key = "22222222-2222-4222-8222-222222222222"
	duplicate := &drivermysql.MySQLError{Number: 1062, Message: "duplicate message key"}
	for _, test := range []struct {
		name    string
		role    string
		content string
		model   any
		wantOK  bool
	}{
		{name: "exact duplicate", role: "assistant", content: "answer", model: "model", wantOK: true},
		{name: "role conflict", role: "user", content: "answer", model: "model"},
		{name: "content conflict", role: "assistant", content: "different", model: "model"},
		{name: "model conflict", role: "assistant", content: "answer", model: "different"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newConversationMockStore(t)
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO conversation_message").
				WithArgs(int64(41), key, "assistant", "answer", "model").WillReturnError(duplicate)
			mock.ExpectRollback()
			mock.ExpectQuery(`SELECT role, content, model_name FROM conversation_message WHERE session_id = \? AND message_key = \?`).
				WithArgs(int64(41), key).WillReturnRows(messageRows().AddRow(test.role, test.content, test.model))

			err := store.SaveMessageWithKey(context.Background(), 41, key, "assistant", "answer", "model")
			if test.wantOK {
				if err != nil {
					t.Fatalf("SaveMessageWithKey() error=%v, want idempotent success", err)
				}
				return
			}
			if !errors.Is(err, errConversationMessageConflict) || mysqltx.IsCommitOutcomeUnknown(err) {
				t.Fatalf("SaveMessageWithKey() error=%v, want definite payload conflict", err)
			}
		})
	}
}

func TestSaveMessageRejectsZeroAffectedRows(t *testing.T) {
	const key = "33333333-3333-4333-8333-333333333333"
	store, mock := newConversationMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO conversation_message").
		WithArgs(int64(41), key, "user", "hello", "").
		WillReturnResult(sqlmock.NewResult(51, 0))
	mock.ExpectRollback()

	err := store.SaveMessageWithKey(context.Background(), 41, key, "user", "hello", "")
	if err == nil || !strings.Contains(err.Error(), "affected 0 rows") || mysqltx.IsCommitOutcomeUnknown(err) {
		t.Fatalf("SaveMessageWithKey() error=%v, want definite affected-row failure", err)
	}
}

func TestSaveMessageCompatibilityEntryGeneratesDurableKeyOnce(t *testing.T) {
	store, mock := newConversationMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO conversation_message").
		WithArgs(int64(41), uuidV4Argument{}, "user", "hello", "").
		WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectCommit()

	if err := store.SaveMessage(context.Background(), 41, "user", "hello", ""); err != nil {
		t.Fatalf("SaveMessage() error=%v", err)
	}
}

func TestSaveMessageRejectsInvalidDurableKey(t *testing.T) {
	store, _ := newConversationMockStore(t)
	if err := store.SaveMessageWithKey(context.Background(), 41, "", "user", "hello", ""); err == nil || !strings.Contains(err.Error(), "lowercase UUIDv4") {
		t.Fatalf("SaveMessageWithKey() error=%v, want invalid key", err)
	}
}

func TestConversationReconciliationContextIsDetachedAndBounded(t *testing.T) {
	tests := []struct {
		name   string
		parent func() context.Context
	}{
		{
			name: "canceled",
			parent: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "deadline expired",
			parent: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				cancel()
				return ctx
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.parent()
			if parent.Err() == nil {
				t.Fatal("parent context is not done")
			}

			ctx, cancel := conversationReconciliationContext(parent)
			defer cancel()
			if err := ctx.Err(); err != nil {
				t.Fatalf("reconciliation context inherited parent error: %v", err)
			}
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > conversationReconciliationLimit {
				t.Fatalf("reconciliation deadline=%v ok=%v remaining=%v", deadline, ok, remaining)
			}
		})
	}
}

type cancelAfterCommitError struct {
	cancel context.CancelFunc
}

func (e *cancelAfterCommitError) Error() string {
	return "connection lost during commit"
}

func (e *cancelAfterCommitError) Is(error) bool {
	e.cancel()
	return false
}

func messageRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"role", "content", "model_name"})
}

type uuidV4Argument struct{}

func (uuidV4Argument) Match(value driver.Value) bool {
	key, ok := value.(string)
	return ok && conversationMessageKeyPattern.MatchString(key)
}

func newConversationMockStore(t *testing.T) (*ConversationStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	return NewConversationStore(db), mock
}
