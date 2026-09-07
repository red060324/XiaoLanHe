package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
)

func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	return NewStore(db), mock
}

func TestFindPurchaseOfferBindsRegionTwiceAndPrefersIt(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(findPurchaseOfferSQL).
		WithArgs(int64(12), "USD", "CN", "CN").
		WillReturnRows(sqlmock.NewRows([]string{
			"game_id", "slug", "game_name", "edition_id", "code", "edition_name",
			"amount_minor", "currency", "region_code",
		}).AddRow(3, "demo", "Demo", 12, "standard", "Standard", 9900, "USD", "CN"))

	offer, err := store.FindPurchaseOffer(context.Background(), 12, catalog.Pricing{Region: "CN", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if offer.EditionID != 12 || offer.AmountMinor != 9900 || offer.Region != "CN" {
		t.Fatalf("offer=%+v", offer)
	}
}

func TestFindPurchaseOfferMapsMissingRow(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(findPurchaseOfferSQL).
		WithArgs(int64(13), "CNY", "CN", "CN").
		WillReturnRows(sqlmock.NewRows([]string{"game_id", "slug", "game_name", "edition_id", "code", "edition_name", "amount_minor", "currency", "region_code"}))

	_, err := store.FindPurchaseOffer(context.Background(), 13, catalog.Pricing{Region: "CN", Currency: "CNY"})
	if !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestListBindsRepeatedFiltersAndScansNullableReleaseDate(t *testing.T) {
	store, mock := newMockStore(t)
	local := time.Date(2026, 9, 7, 8, 30, 0, 123000000, time.FixedZone("test", 8*60*60))
	filter := catalog.ListFilter{ViewerID: 7, BeforeID: 40, Query: "%_game", Limit: 11}
	mock.ExpectQuery(listSQL).
		WithArgs(int64(7), int64(40), int64(40), "%_game", "%_game", "%_game", 11).
		WillReturnRows(sqlmock.NewRows([]string{"id", "slug", "name", "summary", "cover_url", "release_date", "owned"}).
			AddRow(39, "dated-game", "Dated", "summary", "cover", local, true).
			AddRow(38, "undated-game", "Undated", "summary", "cover", nil, false))

	games, err := store.List(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 || games[0].ReleaseDate == nil || games[0].ReleaseDate.Format("2006-01-02") != "2026-09-07" || games[0].ReleaseDate.Location() != time.UTC {
		t.Fatalf("games=%+v", games)
	}
	if games[1].ReleaseDate != nil || games[1].Owned {
		t.Fatalf("nullable game=%+v", games[1])
	}
}

func TestFindBySlugLoadsRegionFirstAndNullablePrices(t *testing.T) {
	store, mock := newMockStore(t)
	local := time.Date(2026, 9, 8, 0, 0, 0, 0, time.FixedZone("test", 8*60*60))
	mock.ExpectQuery(findBySlugSQL).
		WithArgs(int64(7), "demo-game").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "slug", "name", "summary", "description", "developer", "publisher", "release_date", "cover_url", "owned",
		}).AddRow(3, "demo-game", "Demo", "summary", "description", "dev", "publisher", local, "cover", true))
	mock.ExpectQuery(loadEditionsSQL).
		WithArgs(int64(7), "CN", "USD", "CN", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "owned", "amount_minor", "currency", "region_code"}).
			AddRow(10, "standard", "Standard", "base", true, 9900, "USD", "CN").
			AddRow(11, "deluxe", "Deluxe", "extras", false, nil, nil, nil))

	game, err := store.FindBySlug(context.Background(), "demo-game", catalog.Pricing{Region: "CN", Currency: "USD"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if game.ReleaseDate == nil || game.ReleaseDate.Location() != time.UTC || !game.Owned || len(game.Editions) != 2 {
		t.Fatalf("game=%+v", game)
	}
	if len(game.Editions[0].Prices) != 1 || game.Editions[0].Prices[0].Region != "CN" || len(game.Editions[1].Prices) != 0 {
		t.Fatalf("editions=%+v", game.Editions)
	}
}

func TestFindBySlugRejectsPartiallyNullPrice(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(findBySlugSQL).
		WithArgs(int64(0), "broken-game").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "slug", "name", "summary", "description", "developer", "publisher", "release_date", "cover_url", "owned",
		}).AddRow(3, "broken-game", "Broken", "summary", "description", "dev", "publisher", nil, "cover", false))
	mock.ExpectQuery(loadEditionsSQL).
		WithArgs(int64(0), "GLOBAL", "USD", "GLOBAL", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "name", "description", "owned", "amount_minor", "currency", "region_code"}).
			AddRow(10, "standard", "Standard", "base", false, 1999, nil, "GLOBAL"))

	_, err := store.FindBySlug(context.Background(), "broken-game", catalog.Pricing{Region: "GLOBAL", Currency: "USD"}, 0)
	if err == nil || !strings.Contains(err.Error(), "inconsistent nullability") {
		t.Fatalf("err=%v", err)
	}
}

func TestFindBySlugMapsMissingRow(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(findBySlugSQL).WithArgs(int64(0), "missing").
		WillReturnRows(sqlmock.NewRows([]string{"id", "slug", "name", "summary", "description", "developer", "publisher", "release_date", "cover_url", "owned"}))

	_, err := store.FindBySlug(context.Background(), "missing", catalog.Pricing{Region: "GLOBAL", Currency: "USD"}, 0)
	if !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveCreateLocksAggregateAndAdvancesSameMicrosecondPrices(t *testing.T) {
	store, mock := newMockStore(t)
	localDate := time.Date(2026, 9, 8, 0, 0, 0, 0, time.FixedZone("test", 8*60*60))
	activeFrom := time.Date(2026, 9, 7, 4, 5, 6, 123456000, time.UTC)
	olderActiveFrom := activeFrom.Add(-time.Hour)
	replacementAt := activeFrom.Add(time.Microsecond)
	draft := entity.Draft{
		Slug: "demo-game", Name: "Demo", Summary: "summary", Description: "description",
		Developer: "dev", Publisher: "publisher", ReleaseDate: &localDate, CoverURL: "cover",
		Editions: []entity.EditionDraft{
			{Code: "deluxe", Name: "Deluxe", Description: "extras"},
			{Code: "standard", Name: "Standard", Description: "base", Prices: []entity.Price{
				{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999},
				{Region: "CN", Currency: "CNY", AmountMinor: 9900},
			}},
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec(insertGameSQL).
		WithArgs("demo-game", "Demo", "summary", "description", "dev", "publisher", "2026-09-08", "cover").
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
	expectEditionUpsert(mock, 41, draft.Editions[0], 52)
	expectEditionUpsert(mock, 41, draft.Editions[1], 51)
	mock.ExpectQuery(lockActivePriceStartsSQL).WithArgs(int64(51)).WillReturnRows(
		sqlmock.NewRows([]string{"active_from"}).AddRow(olderActiveFrom),
	)
	mock.ExpectQuery(lockActivePriceStartsSQL).WithArgs(int64(52)).WillReturnRows(
		sqlmock.NewRows([]string{"active_from"}).AddRow(activeFrom),
	)
	mock.ExpectQuery(priceReplacementClockSQL).WithArgs(activeFrom).WillReturnRows(
		sqlmock.NewRows([]string{"replacement_at"}).AddRow(replacementAt),
	)
	expectPriceReplacement(mock, 51, draft.Editions[1].Prices, replacementAt)
	expectPriceReplacement(mock, 52, draft.Editions[0].Prices, replacementAt)
	mock.ExpectCommit()
	expectSavedGameRead(mock, 41, time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), []editionRow{
		{id: 51, code: "standard", name: "Standard", description: "base", amount: int64Pointer(1999), currency: stringPointer("USD"), region: stringPointer("GLOBAL")},
		{id: 52, code: "deluxe", name: "Deluxe", description: "extras"},
	})

	game, err := store.Save(context.Background(), 0, draft)
	if err != nil {
		t.Fatal(err)
	}
	if game.ID != 41 || game.ReleaseDate == nil || game.ReleaseDate.Location() != time.UTC || len(game.Editions) != 2 {
		t.Fatalf("game=%+v", game)
	}
}

func TestSaveUpdateLocksAggregateBeforeNoOpUpdate(t *testing.T) {
	store, mock := newMockStore(t)
	draft := entity.Draft{Slug: "demo-game", Name: "Demo"}
	mock.ExpectBegin()
	mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectExec(updateGameSQL).
		WithArgs("demo-game", "Demo", "", "", "", "", nil, "", int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	expectSavedGameRead(mock, 41, nil, nil)

	game, err := store.Save(context.Background(), 41, draft)
	if err != nil || game.ID != 41 {
		t.Fatalf("game=%+v err=%v", game, err)
	}
}

func TestSaveUpdateLocksAggregateBeforeChangedUpdate(t *testing.T) {
	store, mock := newMockStore(t)
	draft := entity.Draft{Slug: "demo-game", Name: "Changed"}
	mock.ExpectBegin()
	mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectExec(updateGameSQL).
		WithArgs("demo-game", "Changed", "", "", "", "", nil, "", int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	expectSavedGameRead(mock, 41, nil, nil)

	game, err := store.Save(context.Background(), 41, draft)
	if err != nil || game.ID != 41 {
		t.Fatalf("game=%+v err=%v", game, err)
	}
}

func TestSaveUpdateMissingRollsBack(t *testing.T) {
	store, mock := newMockStore(t)
	draft := entity.Draft{Slug: "missing-game", Name: "Missing"}
	mock.ExpectBegin()
	mock.ExpectQuery(lockGameSQL).WithArgs(int64(404)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	_, err := store.Save(context.Background(), 404, draft)
	if !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveMapsDuplicateAndPreservesOtherErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     error
		attempts int
	}{
		{name: "duplicate", err: &drivermysql.MySQLError{Number: 1062, Message: "duplicate slug"}, want: catalog.ErrConflict, attempts: 1},
		{name: "wrapped duplicate", err: fmt.Errorf("insert: %w", &drivermysql.MySQLError{Number: 1062}), want: catalog.ErrConflict, attempts: 1},
		{name: "other", err: &drivermysql.MySQLError{Number: 1048, Message: "invalid value"}, attempts: 1},
		{name: "deadlock", err: &drivermysql.MySQLError{Number: 1213, Message: "deadlock"}, attempts: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			for range test.attempts {
				mock.ExpectBegin()
				mock.ExpectExec(insertGameSQL).WillReturnError(test.err)
				mock.ExpectRollback()
			}

			_, err := store.Save(context.Background(), 0, entity.Draft{})
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("err=%v want=%v", err, test.want)
				}
			} else if !errors.Is(err, test.err) {
				t.Fatalf("err=%v want original %v", err, test.err)
			}
		})
	}
}

func TestSaveRollsBackOnLastInsertIDError(t *testing.T) {
	store, mock := newMockStore(t)
	want := errors.New("last insert id")
	mock.ExpectBegin()
	mock.ExpectExec(insertGameSQL).WillReturnResult(errorResult{lastInsertIDErr: want})
	mock.ExpectRollback()

	_, err := store.Save(context.Background(), 0, entity.Draft{})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveRollsBackAndMapsDuplicatePrice(t *testing.T) {
	store, mock := newMockStore(t)
	draft := entity.Draft{Slug: "demo-game", Name: "Demo", Editions: []entity.EditionDraft{{
		Code: "standard", Name: "Standard", Prices: []entity.Price{{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999}},
	}}}
	replacementAt := time.Date(2026, 9, 7, 4, 5, 6, 123456000, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec(insertGameSQL).WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(upsertEditionSQL).WithArgs(int64(41), "standard", "Standard", "").WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectQuery(findEditionIDSQL).WithArgs(int64(41), "standard").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(51))
	mock.ExpectQuery(lockActivePriceStartsSQL).WithArgs(int64(51)).
		WillReturnRows(sqlmock.NewRows([]string{"active_from"}))
	mock.ExpectQuery(priceReplacementClockSQL).WithArgs(nil).
		WillReturnRows(sqlmock.NewRows([]string{"replacement_at"}).AddRow(replacementAt))
	mock.ExpectExec(closeEditionPricesSQL).WithArgs(replacementAt, replacementAt, int64(51)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(insertPriceSQL).WithArgs(int64(51), "GLOBAL", "USD", int64(1999), replacementAt).
		WillReturnError(&drivermysql.MySQLError{Number: 1062, Message: "duplicate active price"})
	mock.ExpectRollback()

	_, err := store.Save(context.Background(), 0, draft)
	if !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveUsesDatabaseTimeWhenNoOpenPriceExists(t *testing.T) {
	store, mock := newMockStore(t)
	databaseNow := time.Date(2026, 9, 7, 4, 5, 6, 654321000, time.UTC)
	draft := entity.Draft{Slug: "demo-game", Name: "Demo", Editions: []entity.EditionDraft{{
		Code: "standard", Name: "Standard", Prices: []entity.Price{{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999}},
	}}}

	mock.ExpectBegin()
	mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectExec(updateGameSQL).
		WithArgs("demo-game", "Demo", "", "", "", "", nil, "", int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectEditionUpsert(mock, 41, draft.Editions[0], 51)
	mock.ExpectQuery(lockActivePriceStartsSQL).WithArgs(int64(51)).
		WillReturnRows(sqlmock.NewRows([]string{"active_from"}))
	mock.ExpectQuery(priceReplacementClockSQL).WithArgs(nil).
		WillReturnRows(sqlmock.NewRows([]string{"replacement_at"}).AddRow(databaseNow))
	expectPriceReplacement(mock, 51, draft.Editions[0].Prices, databaseNow)
	mock.ExpectCommit()
	expectSavedGameRead(mock, 41, nil, []editionRow{{
		id: 51, code: "standard", name: "Standard", amount: int64Pointer(1999),
		currency: stringPointer("USD"), region: stringPointer("GLOBAL"),
	}})

	if _, err := store.Save(context.Background(), 41, draft); err != nil {
		t.Fatal(err)
	}
}

func TestSaveReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name        string
		gameRows    *sqlmock.Rows
		editionRows *sqlmock.Rows
		wantOK      bool
		wantReason  string
	}{
		{
			name:        "committed exact aggregate",
			gameRows:    catalogDraftRows().AddRow("demo-game", "Demo", "", "", "", "", nil, "", "active"),
			editionRows: catalogDraftEditionRows().AddRow(int64(51), "standard", "Standard", "", int64(1999), "USD", "GLOBAL", replacementTime()),
			wantOK:      true,
		},
		{name: "not committed", gameRows: catalogDraftRows(), wantReason: "load catalog save reconciliation state"},
		{
			name:        "mismatch",
			gameRows:    catalogDraftRows().AddRow("demo-game", "Other", "", "", "", "", nil, "", "active"),
			editionRows: catalogDraftEditionRows().AddRow(int64(51), "standard", "Standard", "", int64(1999), "USD", "GLOBAL", replacementTime()),
			wantReason:  "durable state mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			draft := entity.Draft{Slug: "demo-game", Name: "Demo", Editions: []entity.EditionDraft{{Code: "standard", Name: "Standard", Prices: []entity.Price{{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999}}}}}
			replacementAt := replacementTime()
			commitErr := errors.New("connection lost during commit")

			mock.ExpectBegin()
			mock.ExpectExec(insertGameSQL).WithArgs("demo-game", "Demo", "", "", "", "", nil, "").WillReturnResult(sqlmock.NewResult(41, 1))
			mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
			mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
			expectEditionUpsert(mock, 41, draft.Editions[0], 51)
			mock.ExpectQuery(lockActivePriceStartsSQL).WithArgs(int64(51)).WillReturnRows(sqlmock.NewRows([]string{"active_from"}))
			mock.ExpectQuery(priceReplacementClockSQL).WithArgs(nil).WillReturnRows(sqlmock.NewRows([]string{"replacement_at"}).AddRow(replacementAt))
			expectPriceReplacement(mock, 51, draft.Editions[0].Prices, replacementAt)
			mock.ExpectCommit().WillReturnError(commitErr)

			mock.ExpectBegin()
			mock.ExpectQuery(lockGameBySlugSQL).WithArgs("demo-game").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
			mock.ExpectQuery(loadCatalogDraftSQL).WithArgs(int64(41)).WillReturnRows(test.gameRows)
			if test.editionRows != nil {
				mock.ExpectQuery(loadCatalogDraftEditionsSQL).WithArgs(int64(41)).WillReturnRows(test.editionRows)
			}
			if test.wantOK {
				expectSavedGameRead(mock, 41, nil, []editionRow{{id: 51, code: "standard", name: "Standard", amount: int64Pointer(1999), currency: stringPointer("USD"), region: stringPointer("GLOBAL")}})
			}
			mock.ExpectRollback()

			game, err := store.Save(context.Background(), 0, draft)
			if test.wantOK {
				if err != nil || game.ID != 41 {
					t.Fatalf("Save() = (%+v, %v), want reconciled game", game, err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("Save() error = %v, want unknown outcome with %q", err, test.wantReason)
			}
		})
	}
}

func TestSaveCreateAmbiguousCommitWithAbsentSlugRemainsUnknown(t *testing.T) {
	store, mock := newMockStore(t)
	draft := catalogReconciliationDraft()
	commitErr := errors.New("connection lost during commit")
	expectAmbiguousCatalogSaveAttempt(mock, 0, 41, draft, replacementTime(), commitErr)
	mock.ExpectBegin()
	mock.ExpectQuery(lockGameBySlugSQL).WithArgs(draft.Slug).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	_, err := store.Save(context.Background(), 0, draft)
	if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) {
		t.Fatalf("Save() error = %v, want original unknown outcome for absent slug", err)
	}
}

func TestSaveUpdateReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		storedName string
		wantOK     bool
	}{
		{name: "committed exact aggregate", storedName: "Demo", wantOK: true},
		{name: "not committed state", storedName: "Before"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			draft := catalogReconciliationDraft()
			commitErr := errors.New("connection lost during commit")
			expectAmbiguousCatalogSaveAttempt(mock, 41, 41, draft, replacementTime(), commitErr)

			mock.ExpectBegin()
			mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
			mock.ExpectQuery(loadCatalogDraftSQL).WithArgs(int64(41)).WillReturnRows(
				catalogDraftRows().AddRow("demo-game", test.storedName, "summary", "description", "dev", "publisher", nil, "cover", "active"),
			)
			mock.ExpectQuery(loadCatalogDraftEditionsSQL).WithArgs(int64(41)).WillReturnRows(
				catalogDraftEditionRows().AddRow(int64(51), "standard", "Standard", "base", int64(1999), "USD", "GLOBAL", replacementTime()),
			)
			if test.wantOK {
				expectSavedGameRead(mock, 41, nil, []editionRow{{id: 51, code: "standard", name: "Standard", description: "base", amount: int64Pointer(1999), currency: stringPointer("USD"), region: stringPointer("GLOBAL")}})
			}
			mock.ExpectRollback()

			game, err := store.Save(context.Background(), 41, draft)
			if test.wantOK {
				if err != nil || game.ID != 41 {
					t.Fatalf("Save() = (%+v, %v), want reconciled update", game, err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), "durable state mismatch") {
				t.Fatalf("Save() error = %v, want unknown outcome for uncommitted update", err)
			}
		})
	}
}

func TestSaveCreateAmbiguousCommitRejectsDurableIdentityMismatch(t *testing.T) {
	store, mock := newMockStore(t)
	draft := catalogReconciliationDraft()
	commitErr := errors.New("connection lost during commit")
	expectAmbiguousCatalogSaveAttempt(mock, 0, 41, draft, replacementTime(), commitErr)
	mock.ExpectBegin()
	mock.ExpectQuery(lockGameBySlugSQL).WithArgs(draft.Slug).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
	mock.ExpectRollback()

	_, err := store.Save(context.Background(), 0, draft)
	if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), "durable game identity mismatch") {
		t.Fatalf("Save() error = %v, want unknown outcome for durable identity mismatch", err)
	}
}

func TestSaveAmbiguousCommitRejectsDifferentPriceGeneration(t *testing.T) {
	store, mock := newMockStore(t)
	draft := catalogReconciliationDraft()
	commitErr := errors.New("connection lost during commit")
	replacementAt := replacementTime()
	expectAmbiguousCatalogSaveAttempt(mock, 0, 41, draft, replacementAt, commitErr)
	mock.ExpectBegin()
	mock.ExpectQuery(lockGameBySlugSQL).WithArgs(draft.Slug).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectQuery(loadCatalogDraftSQL).WithArgs(int64(41)).WillReturnRows(
		catalogDraftRows().AddRow("demo-game", "Demo", "summary", "description", "dev", "publisher", nil, "cover", "active"),
	)
	mock.ExpectQuery(loadCatalogDraftEditionsSQL).WithArgs(int64(41)).WillReturnRows(
		catalogDraftEditionRows().AddRow(int64(51), "standard", "Standard", "base", int64(1999), "USD", "GLOBAL", replacementAt.Add(-time.Microsecond)),
	)
	mock.ExpectRollback()

	_, err := store.Save(context.Background(), 0, draft)
	if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), "active price generation mismatch") {
		t.Fatalf("Save() error = %v, want unknown outcome for price generation mismatch", err)
	}
}

func catalogReconciliationDraft() entity.Draft {
	return entity.Draft{
		Slug: "demo-game", Name: "Demo", Summary: "summary", Description: "description",
		Developer: "dev", Publisher: "publisher", CoverURL: "cover",
		Editions: []entity.EditionDraft{{
			Code: "standard", Name: "Standard", Description: "base",
			Prices: []entity.Price{{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999}},
		}},
	}
}

func expectAmbiguousCatalogSaveAttempt(mock sqlmock.Sqlmock, requestedID, storedID int64, draft entity.Draft, replacementAt time.Time, commitErr error) {
	mock.ExpectBegin()
	if requestedID == 0 {
		mock.ExpectExec(insertGameSQL).
			WithArgs(draft.Slug, draft.Name, draft.Summary, draft.Description, draft.Developer, draft.Publisher, nil, draft.CoverURL).
			WillReturnResult(sqlmock.NewResult(storedID, 1))
		mock.ExpectQuery(lockGameSQL).WithArgs(storedID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(storedID))
	} else {
		mock.ExpectQuery(lockGameSQL).WithArgs(requestedID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(requestedID))
		mock.ExpectExec(updateGameSQL).
			WithArgs(draft.Slug, draft.Name, draft.Summary, draft.Description, draft.Developer, draft.Publisher, nil, draft.CoverURL, requestedID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(storedID).WillReturnResult(sqlmock.NewResult(0, 1))
	expectEditionUpsert(mock, storedID, draft.Editions[0], 51)
	mock.ExpectQuery(lockActivePriceStartsSQL).WithArgs(int64(51)).WillReturnRows(sqlmock.NewRows([]string{"active_from"}))
	mock.ExpectQuery(priceReplacementClockSQL).WithArgs(nil).WillReturnRows(sqlmock.NewRows([]string{"replacement_at"}).AddRow(replacementAt))
	expectPriceReplacement(mock, 51, draft.Editions[0].Prices, replacementAt)
	mock.ExpectCommit().WillReturnError(commitErr)
}

func catalogDraftRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"slug", "name", "summary", "description", "developer", "publisher", "release_date", "cover_url", "status"})
}

func catalogDraftEditionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "code", "name", "description", "amount_minor", "currency", "region_code", "active_from"})
}

func replacementTime() time.Time {
	return time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
}

func TestSaveRollsBackWhenReplacementTimestampIsNotRepresentable(t *testing.T) {
	store, mock := newMockStore(t)
	activeFrom := time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
	draft := entity.Draft{Slug: "demo-game", Name: "Demo", Editions: []entity.EditionDraft{{
		Code: "standard", Name: "Standard",
	}}}

	mock.ExpectBegin()
	mock.ExpectQuery(lockGameSQL).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectExec(updateGameSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(deactivateAllEditionsSQL).WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectEditionUpsert(mock, 41, draft.Editions[0], 51)
	mock.ExpectQuery(lockActivePriceStartsSQL).WithArgs(int64(51)).
		WillReturnRows(sqlmock.NewRows([]string{"active_from"}).AddRow(activeFrom))
	mock.ExpectQuery(priceReplacementClockSQL).WithArgs(activeFrom).
		WillReturnRows(sqlmock.NewRows([]string{"replacement_at"}).AddRow(nil))
	mock.ExpectRollback()

	_, err := store.Save(context.Background(), 41, draft)
	if err == nil || !strings.Contains(err.Error(), "not representable") {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveRequestsReadCommitted(t *testing.T) {
	want := errors.New("stop after begin")
	connector := &isolationConnector{beginErr: want}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })

	_, err := NewStore(db).Save(context.Background(), 0, entity.Draft{})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	if connector.options.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) || connector.options.ReadOnly {
		t.Fatalf("transaction options=%+v", connector.options)
	}
}

func TestQueriesContainNoPostgreSQLOnlySyntax(t *testing.T) {
	queries := []string{
		existsSQL, findPurchaseOfferSQL, listSQL, findBySlugSQL, insertGameSQL, updateGameSQL,
		lockGameSQL, upsertEditionSQL, findEditionIDSQL, lockActivePriceStartsSQL, priceReplacementClockSQL, closeEditionPricesSQL,
		insertPriceSQL, deactivateAllEditionsSQL, findByIDSQL, loadEditionsSQL,
	}
	for _, query := range queries {
		lower := strings.ToLower(query)
		for _, forbidden := range []string{"$1", "::", " returning ", "on conflict", "lateral", "=any("} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("query contains %q: %s", forbidden, query)
			}
		}
	}
	if !strings.Contains(loadEditionsSQL, "row_number() over") || !strings.Contains(findPurchaseOfferSQL, "case when p.region_code=?") {
		t.Fatal("price queries do not preserve region-first selection")
	}
	if !strings.Contains(upsertEditionSQL, "values (?,?,?,?,'active') as new") || !strings.Contains(upsertEditionSQL, "new.name") {
		t.Fatal("edition upsert does not use MySQL row alias syntax")
	}
	if !strings.Contains(lockActivePriceStartsSQL, "active_until is null") ||
		!strings.Contains(lockActivePriceStartsSQL, "order by region_code,currency,active_from,id for update") {
		t.Fatal("active-price read does not lock only open rows in deterministic order")
	}
	if strings.Count(strings.ToLower(priceReplacementClockSQL), "utc_timestamp(6)") != 1 ||
		!strings.Contains(strings.ToLower(priceReplacementClockSQL), "timestampadd(microsecond,1,max_active_from)") {
		t.Fatal("replacement clock must evaluate UTC_TIMESTAMP(6) once and advance a same-microsecond price")
	}
}

func expectEditionUpsert(mock sqlmock.Sqlmock, gameID int64, edition entity.EditionDraft, editionID int64) {
	mock.ExpectExec(upsertEditionSQL).
		WithArgs(gameID, edition.Code, edition.Name, edition.Description).
		WillReturnResult(sqlmock.NewResult(editionID, 1))
	mock.ExpectQuery(findEditionIDSQL).WithArgs(gameID, edition.Code).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(editionID))
}

func expectPriceReplacement(mock sqlmock.Sqlmock, editionID int64, prices []entity.Price, replacementAt time.Time) {
	mock.ExpectExec(closeEditionPricesSQL).WithArgs(replacementAt, replacementAt, editionID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, price := range prices {
		mock.ExpectExec(insertPriceSQL).WithArgs(editionID, price.Region, price.Currency, price.AmountMinor, replacementAt).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}
}

type editionRow struct {
	id                      int64
	code, name, description string
	owned                   bool
	amount                  *int64
	currency, region        *string
}

func expectSavedGameRead(mock sqlmock.Sqlmock, id int64, releaseDate any, editions []editionRow) {
	mock.ExpectQuery(findByIDSQL).WithArgs(id).WillReturnRows(
		sqlmock.NewRows([]string{"id", "slug", "name", "summary", "description", "developer", "publisher", "release_date", "cover_url"}).
			AddRow(id, "demo-game", "Demo", "summary", "description", "dev", "publisher", releaseDate, "cover"),
	)
	rows := sqlmock.NewRows([]string{"id", "code", "name", "description", "owned", "amount_minor", "currency", "region_code"})
	for _, edition := range editions {
		var amount, currency, region any
		if edition.amount != nil {
			amount = *edition.amount
		}
		if edition.currency != nil {
			currency = *edition.currency
		}
		if edition.region != nil {
			region = *edition.region
		}
		rows.AddRow(edition.id, edition.code, edition.name, edition.description, edition.owned, amount, currency, region)
	}
	mock.ExpectQuery(loadEditionsSQL).WithArgs(int64(0), "GLOBAL", "USD", "GLOBAL", id).WillReturnRows(rows)
}

func int64Pointer(value int64) *int64    { return &value }
func stringPointer(value string) *string { return &value }

type errorResult struct {
	lastInsertIDErr error
	rowsAffectedErr error
}

func (r errorResult) LastInsertId() (int64, error) { return 0, r.lastInsertIDErr }
func (r errorResult) RowsAffected() (int64, error) { return 0, r.rowsAffectedErr }

type isolationConnector struct {
	options  driver.TxOptions
	beginErr error
}

func (c *isolationConnector) Connect(context.Context) (driver.Conn, error) {
	return &isolationConn{connector: c}, nil
}
func (c *isolationConnector) Driver() driver.Driver { return isolationDriver{} }

type isolationDriver struct{}

func (isolationDriver) Open(string) (driver.Conn, error) { return nil, errors.New("not supported") }

type isolationConn struct{ connector *isolationConnector }

func (*isolationConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (*isolationConn) Close() error                        { return nil }
func (*isolationConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }
func (c *isolationConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.connector.options = options
	return nil, c.connector.beginErr
}
