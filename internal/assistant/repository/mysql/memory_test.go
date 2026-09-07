package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPrepareSummaryBuildsCandidateAndRetainsRecentWindow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(loadSummaryWatermarkSQL)).WithArgs(int64(21)).WillReturnRows(
		sqlmock.NewRows([]string{"summary_text", "summary_through_message_id"}).AddRow("prior", int64(4)),
	)
	mock.ExpectQuery(regexp.QuoteMeta(loadUnsummarizedMessagesSQL)).WithArgs(int64(21), int64(4)).WillReturnRows(
		sqlmock.NewRows([]string{"id", "role", "content"}).
			AddRow(int64(5), "user", "你好世界").
			AddRow(int64(6), "assistant", "answer").
			AddRow(int64(7), "user", "recent"),
	)

	candidate, needed, err := NewMemoryStore(db).PrepareSummary(context.Background(), 21, 1, 3)
	if err != nil || !needed {
		t.Fatalf("PrepareSummary() = (%#v, %v, %v), want needed", candidate, needed, err)
	}
	if candidate.PriorSummary != "prior" || candidate.PriorWatermark != 4 || candidate.ThroughMessageID != 6 || len(candidate.Messages) != 2 {
		t.Fatalf("candidate = %#v", candidate)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSummaryCASRowsAffected(t *testing.T) {
	for _, test := range []struct {
		name     string
		affected int64
		want     bool
	}{
		{name: "updated", affected: 1, want: true},
		{name: "stale or invalid watermark", affected: 0, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectExec(regexp.QuoteMeta(updateSummarySQL)).WithArgs(
				"summary", int64(9), "summary-v2", int64(22), int64(4), int64(9), int64(4), int64(9),
			).WillReturnResult(sqlmock.NewResult(0, test.affected))

			updated, err := NewMemoryStore(db).UpdateSummary(context.Background(), 22, 4, 9, "summary", "summary-v2")
			if err != nil || updated != test.want {
				t.Fatalf("UpdateSummary() = (%v, %v), want (%v, nil)", updated, err, test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateSummaryRowsAffectedError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wantErr := errors.New("affected unavailable")
	mock.ExpectExec(regexp.QuoteMeta(updateSummarySQL)).WithArgs(
		"summary", int64(9), "summary-v2", int64(22), int64(4), int64(9), int64(4), int64(9),
	).WillReturnResult(errorResult{err: wantErr})

	updated, err := NewMemoryStore(db).UpdateSummary(context.Background(), 22, 4, 9, "summary", "summary-v2")
	if updated || !errors.Is(err, wantErr) {
		t.Fatalf("UpdateSummary() = (%v, %v), want false and affected error", updated, err)
	}
}

type errorResult struct{ err error }

func (r errorResult) LastInsertId() (int64, error) { return 0, r.err }
func (r errorResult) RowsAffected() (int64, error) { return 0, r.err }

var _ driver.Result = errorResult{}

func TestMemorySQLUsesMySQLPlaceholdersAndCAS(t *testing.T) {
	queries := []string{loadSummaryWatermarkSQL, loadUnsummarizedMessagesSQL, updateSummarySQL}
	for _, query := range queries {
		if strings.Contains(query, "$1") {
			t.Fatalf("query contains PostgreSQL placeholder: %s", query)
		}
	}
	for _, fragment := range []string{
		"coalesce(cs.summary_through_message_id, 0) = ?",
		"and ? > ?",
		"cm.id = ? and cm.session_id = cs.id",
	} {
		if !strings.Contains(updateSummarySQL, fragment) {
			t.Fatalf("summary update is missing CAS fragment %q", fragment)
		}
	}
}
