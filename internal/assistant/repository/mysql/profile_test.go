package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

func TestLoadAssistantProfileMissingRowOrPath(t *testing.T) {
	tests := []struct {
		name string
		rows *sqlmock.Rows
	}{
		{name: "row absent", rows: sqlmock.NewRows([]string{"default_region", "assistant", "found", "updated_at"})},
		{name: "assistant path absent", rows: sqlmock.NewRows([]string{"default_region", "assistant", "found", "updated_at"}).AddRow("US", []byte(`{}`), false, time.Now())},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(regexp.QuoteMeta(loadAssistantProfileSQL)).WithArgs(int64(7)).WillReturnRows(test.rows)

			profile, found, err := NewProfileStore(db).LoadAssistantProfile(context.Background(), 7)
			if err != nil || found {
				t.Fatalf("LoadAssistantProfile() = (%#v, %v, %v), want empty, false, nil", profile, found, err)
			}
			if profile.FavoriteGenres == nil || profile.PreferredPlatforms == nil || profile.PreferredLanguages == nil {
				t.Fatalf("empty profile contains nil slices: %#v", profile)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadAssistantProfileExplicitJSONNullPreservesFoundAndUTC(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	local := time.Date(2026, 9, 7, 12, 30, 1, 123000000, time.FixedZone("test", 8*60*60))
	mock.ExpectQuery(regexp.QuoteMeta(loadAssistantProfileSQL)).WithArgs(int64(8)).WillReturnRows(
		sqlmock.NewRows([]string{"default_region", "assistant", "found", "updated_at"}).AddRow("", []byte(`null`), true, local),
	)

	profile, found, err := NewProfileStore(db).LoadAssistantProfile(context.Background(), 8)
	if err != nil || !found {
		t.Fatalf("LoadAssistantProfile() = (%#v, %v, %v), want found", profile, found, err)
	}
	if profile.UpdatedAt.Location() != time.UTC || !profile.UpdatedAt.Equal(local) {
		t.Fatalf("UpdatedAt = %v (%v), want UTC instant %v", profile.UpdatedAt, profile.UpdatedAt.Location(), local)
	}
	if profile.FavoriteGenres == nil || profile.MaxPriceMinor != nil {
		t.Fatalf("explicit JSON null profile = %#v", profile)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceAssistantProfileUsesTransactionAndReturnsStoredUTCValue(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	price := int64(9900)
	input := entity.Profile{
		FavoriteGenres: []string{"rpg"}, PreferredPlatforms: []string{"pc"},
		PreferredLanguages: []string{"zh-CN"}, DefaultRegion: "CN",
		MaxPriceMinor: &price, Currency: "CNY",
	}
	storedAt := time.Date(2026, 9, 7, 4, 30, 1, 456000000, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)))
	mock.ExpectExec(regexp.QuoteMeta(replaceAssistantProfileSQL)).
		WithArgs(int64(11), "CN", assistantJSONMatcher{want: assistantPreferences{
			FavoriteGenres: []string{"rpg"}, PreferredPlatforms: []string{"pc"},
			PreferredLanguages: []string{"zh-CN"}, MaxPriceMinor: &price, Currency: "CNY",
		}}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(readStoredAssistantProfileSQL)).WithArgs(int64(11)).WillReturnRows(
		sqlmock.NewRows([]string{"default_region", "assistant", "updated_at"}).AddRow(
			"CN", []byte(`{"favoriteGenres":["rpg"],"preferredPlatforms":["pc"],"preferredLanguages":["zh-CN"],"maxPriceMinor":9900,"currency":"CNY"}`), storedAt,
		),
	)
	mock.ExpectCommit()

	stored, err := NewProfileStore(db).ReplaceAssistantProfile(context.Background(), 11, input)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.UpdatedAt.Equal(storedAt) || stored.UpdatedAt.Location() != time.UTC || stored.MaxPriceMinor == nil || *stored.MaxPriceMinor != price {
		t.Fatalf("stored profile = %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceAssistantProfileRollsBackWhenStoredReadFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(12)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(12)))
	mock.ExpectExec(regexp.QuoteMeta(replaceAssistantProfileSQL)).WithArgs(int64(12), "", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta(readStoredAssistantProfileSQL)).WithArgs(int64(12)).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	_, err = NewProfileStore(db).ReplaceAssistantProfile(context.Background(), 12, entity.EmptyProfile())
	if !strings.Contains(errorString(err), "read replaced assistant profile") {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceAssistantProfilePreservesDeterministicWriteError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := errors.New("replace write failed")
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(16)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(16)))
	mock.ExpectExec(regexp.QuoteMeta(replaceAssistantProfileSQL)).WithArgs(int64(16), "", sqlmock.AnyArg()).WillReturnError(want)
	mock.ExpectRollback()

	_, err = NewProfileStore(db).ReplaceAssistantProfile(context.Background(), 16, entity.EmptyProfile())
	if !errors.Is(err, want) || mysqltx.IsCommitOutcomeUnknown(err) {
		t.Fatalf("ReplaceAssistantProfile() error = %v, want deterministic write error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceAssistantProfileReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		rows       *sqlmock.Rows
		wantOK     bool
		wantReason string
	}{
		{
			name:   "committed exact state",
			rows:   assistantProfileRows().AddRow("CN", []byte(`{"favoriteGenres":["rpg"],"preferredPlatforms":[],"preferredLanguages":[]}`), true, time.Now()),
			wantOK: true,
		},
		{name: "not committed", rows: assistantProfileRows(), wantReason: "durable state mismatch"},
		{
			name:       "mismatch",
			rows:       assistantProfileRows().AddRow("US", []byte(`{"favoriteGenres":["other"]}`), true, time.Now()),
			wantReason: "durable state mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			commitErr := errors.New("connection lost during commit")
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(21)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(21))
			mock.ExpectExec(regexp.QuoteMeta(replaceAssistantProfileSQL)).WithArgs(int64(21), "CN", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta(readStoredAssistantProfileSQL)).WithArgs(int64(21)).WillReturnRows(
				sqlmock.NewRows([]string{"default_region", "assistant", "updated_at"}).AddRow("CN", []byte(`{"favoriteGenres":["rpg"],"preferredPlatforms":[],"preferredLanguages":[]}`), time.Now()),
			)
			mock.ExpectCommit().WillReturnError(commitErr)
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(21)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(21))
			mock.ExpectQuery(regexp.QuoteMeta(loadAssistantProfileSQL)).WithArgs(int64(21)).WillReturnRows(test.rows)
			mock.ExpectRollback()

			stored, err := NewProfileStore(db).ReplaceAssistantProfile(context.Background(), 21, entity.Profile{DefaultRegion: "CN", FavoriteGenres: []string{"rpg"}, PreferredPlatforms: []string{}, PreferredLanguages: []string{}})
			if test.wantOK {
				if err != nil || stored.DefaultRegion != "CN" || !slices.Equal(stored.FavoriteGenres, []string{"rpg"}) {
					t.Fatalf("ReplaceAssistantProfile() = (%#v, %v), want reconciled success", stored, err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("ReplaceAssistantProfile() error = %v, want unknown outcome with %q", err, test.wantReason)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assistantProfileRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"default_region", "assistant", "found", "updated_at"})
}

func TestClearAssistantProfileTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(13)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(13)))
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileSQL)).WithArgs(int64(13)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(19)))
	mock.ExpectExec(regexp.QuoteMeta(clearAssistantProfileSQL)).WithArgs(int64(19)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(removeEmptyPlayerProfileSQL)).WithArgs(int64(19)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if err := NewProfileStore(db).ClearAssistantProfile(context.Background(), 13); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClearAssistantProfileMissingRowCommitsWithoutWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(14)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(14)))
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileSQL)).WithArgs(int64(14)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	if err := NewProfileStore(db).ClearAssistantProfile(context.Background(), 14); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClearAssistantProfilePreservesDeterministicWriteErrors(t *testing.T) {
	for _, test := range []struct {
		name              string
		failClear         bool
		want              error
		expectRemoveEmpty bool
	}{
		{name: "clear update", failClear: true, want: errors.New("clear write failed")},
		{name: "empty row delete", want: errors.New("delete write failed"), expectRemoveEmpty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(17)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
			mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileSQL)).WithArgs(int64(17)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(27)))
			if test.failClear {
				mock.ExpectExec(regexp.QuoteMeta(clearAssistantProfileSQL)).WithArgs(int64(27)).WillReturnError(test.want)
			} else {
				mock.ExpectExec(regexp.QuoteMeta(clearAssistantProfileSQL)).WithArgs(int64(27)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if test.expectRemoveEmpty {
				mock.ExpectExec(regexp.QuoteMeta(removeEmptyPlayerProfileSQL)).WithArgs(int64(27)).WillReturnError(test.want)
			}
			mock.ExpectRollback()

			err = NewProfileStore(db).ClearAssistantProfile(context.Background(), 17)
			if !errors.Is(err, test.want) || mysqltx.IsCommitOutcomeUnknown(err) {
				t.Fatalf("ClearAssistantProfile() error = %v, want deterministic write error", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClearAssistantProfileReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		rows       *sqlmock.Rows
		wantOK     bool
		wantReason string
	}{
		{name: "committed path removed", rows: assistantProfileRows(), wantOK: true},
		{name: "committed path absent in retained row", rows: assistantProfileRows().AddRow("", []byte(`{}`), false, time.Now()), wantOK: true},
		{name: "not committed", rows: assistantProfileRows().AddRow("CN", []byte(`{"favoriteGenres":["rpg"]}`), true, time.Now()), wantReason: "durable state mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			commitErr := errors.New("connection lost during commit")
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(22)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(22))
			mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileSQL)).WithArgs(int64(22)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(31))
			mock.ExpectExec(regexp.QuoteMeta(clearAssistantProfileSQL)).WithArgs(int64(31)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta(removeEmptyPlayerProfileSQL)).WithArgs(int64(31)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit().WillReturnError(commitErr)
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(22)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(22))
			mock.ExpectQuery(regexp.QuoteMeta(loadAssistantProfileSQL)).WithArgs(int64(22)).WillReturnRows(test.rows)
			mock.ExpectRollback()

			err = NewProfileStore(db).ClearAssistantProfile(context.Background(), 22)
			if test.wantOK {
				if err != nil {
					t.Fatalf("ClearAssistantProfile() error = %v, want reconciled success", err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("ClearAssistantProfile() error = %v, want unknown outcome with %q", err, test.wantReason)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplaceAssistantProfileMissingUserRollsBackBeforeProfileWrite(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockAssistantProfileUserSQL)).WithArgs(int64(15)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	_, err = NewProfileStore(db).ReplaceAssistantProfile(context.Background(), 15, entity.EmptyProfile())
	if !strings.Contains(errorString(err), "lock assistant profile user") || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error = %v, want wrapped sql.ErrNoRows", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeAssistantPreferencesPreservesMissingAndNullSemantics(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`null`), []byte(`{}`)} {
		var profile entity.Profile
		if err := decodeAssistantPreferences(raw, &profile); err != nil {
			t.Fatalf("decodeAssistantPreferences(%s) error = %v", raw, err)
		}
		if profile.FavoriteGenres == nil || profile.PreferredPlatforms == nil || profile.PreferredLanguages == nil {
			t.Fatalf("decodeAssistantPreferences(%s) returned nil slices: %#v", raw, profile)
		}
		if profile.MaxPriceMinor != nil || profile.Currency != "" {
			t.Fatalf("decodeAssistantPreferences(%s) = %#v, want empty profile", raw, profile)
		}
	}
}

func TestDecodeAssistantPreferencesRoundTrip(t *testing.T) {
	var profile entity.Profile
	if err := decodeAssistantPreferences([]byte(`{"favoriteGenres":["rpg"],"preferredPlatforms":["pc"],"preferredLanguages":["zh-CN"],"maxPriceMinor":9900,"currency":"CNY"}`), &profile); err != nil {
		t.Fatal(err)
	}
	if len(profile.FavoriteGenres) != 1 || profile.FavoriteGenres[0] != "rpg" || profile.MaxPriceMinor == nil || *profile.MaxPriceMinor != 9900 || profile.Currency != "CNY" {
		t.Fatalf("decoded profile = %#v", profile)
	}
}

func TestProfileSQLUsesMySQLJSONAndPlaceholders(t *testing.T) {
	queries := []string{loadAssistantProfileSQL, replaceAssistantProfileSQL, readStoredAssistantProfileSQL, lockAssistantProfileUserSQL, lockAssistantProfileSQL, clearAssistantProfileSQL, removeEmptyPlayerProfileSQL}
	for _, query := range queries {
		if strings.Contains(query, "$1") || strings.Contains(query, "::jsonb") {
			t.Fatalf("query contains PostgreSQL syntax: %s", query)
		}
	}
	if !strings.Contains(loadAssistantProfileSQL, "json_contains_path") {
		t.Fatal("load query does not distinguish a missing assistant path")
	}
	if !strings.Contains(replaceAssistantProfileSQL, "json_set") {
		t.Fatal("replace query does not preserve unrelated preferences")
	}
}

func TestProfileTransactionsUseReadCommitted(t *testing.T) {
	options := profileTxOptions()
	if options.Isolation != sql.LevelReadCommitted || options.ReadOnly {
		t.Fatalf("profile transaction options = %#v, want writable ReadCommitted", options)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type assistantJSONMatcher struct{ want assistantPreferences }

func (m assistantJSONMatcher) Match(value driver.Value) bool {
	var raw []byte
	switch value := value.(type) {
	case []byte:
		raw = value
	case string:
		raw = []byte(value)
	default:
		return false
	}
	var got assistantPreferences
	if err := json.Unmarshal(raw, &got); err != nil {
		return false
	}
	want, err := json.Marshal(m.want)
	if err != nil {
		return false
	}
	actual, err := json.Marshal(got)
	return err == nil && string(actual) == string(want)
}
