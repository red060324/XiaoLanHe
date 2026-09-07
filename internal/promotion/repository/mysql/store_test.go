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

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
)

func TestNewStoreImplementsPromotionStore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectClose()
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	store := NewStore(db)
	var contract promotion.Store = store
	if contract == nil || store.db != db {
		t.Fatalf("NewStore() = %#v, want store backed by supplied database", store)
	}
}

func TestClaimLocksUserBeforeCouponAndCommitsInsert(t *testing.T) {
	store, mock := newMockStore(t)
	command := promotion.ClaimCommand{
		UserID: 7, Code: "WELCOME20", IdempotencyKey: "claim-key.01",
	}
	effectiveNow := time.Date(2026, 9, 7, 8, 9, 10, 123456000, time.UTC)

	mock.ExpectBegin()
	// Ordered expectations prove the stable user row is locked before either
	// idempotency lookup or the locking campaign/coupon query.
	mock.ExpectQuery(`
		select id from user_account where id=? for update`).
		WithArgs(command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(findClaimByIdempotencySQL).
		WithArgs(command.UserID, command.IdempotencyKey).
		WillReturnRows(claimRows())
	mock.ExpectQuery(`
		select d.id,d.code,d.name,d.discount_type,coalesce(d.fixed_minor,0),coalesce(d.percentage_bps,0),
			d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
			coalesce(d.game_id,0),coalesce(d.edition_id,0),c.status,c.starts_at,c.ends_at,
			(select count(*) from coupon_claim cl where cl.coupon_id=d.id and cl.user_id=? and cl.status in ('claimed','redeemed'))
		from coupon_definition d join coupon_campaign c on c.id=d.campaign_id
		where d.code=?
		for update`).
		WithArgs(command.UserID, command.Code).
		WillReturnRows(couponRows().AddRow(
			12, command.Code, "Welcome", "percentage", 0, 2000, "USD", 1000,
			10, 2, 1, 4, 6, "active", effectiveNow.Add(-time.Hour), effectiveNow.Add(time.Hour), 0,
		))
	mock.ExpectQuery(databaseNowSQL).
		WillReturnRows(sqlmock.NewRows([]string{"utc_timestamp(6)"}).AddRow(effectiveNow))
	mock.ExpectExec(insertClaimSQL).
		WithArgs(int64(12), command.UserID, command.IdempotencyKey, effectiveNow).
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectExec(incrementClaimedStockSQL).
		WithArgs(effectiveNow, int64(12)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := store.Claim(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Claim.ID != 41 || result.Claim.CouponID != 12 ||
		result.Claim.CouponCode != command.Code || result.Claim.UserID != command.UserID ||
		result.Claim.IdempotencyKey != command.IdempotencyKey || result.Claim.Status != "claimed" ||
		!result.Claim.ClaimedAt.Equal(effectiveNow) {
		t.Fatalf("Claim() = %+v", result)
	}
}

func TestClaimReplaysMatchingIdempotencyKeyAfterUserLock(t *testing.T) {
	store, mock := newMockStore(t)
	command := promotion.ClaimCommand{UserID: 7, Code: "WELCOME20", IdempotencyKey: "claim-key.01"}
	claimedAt := time.Date(2026, 9, 7, 8, 9, 10, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`
		select id from user_account where id=? for update`).
		WithArgs(command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(findClaimByIdempotencySQL).
		WithArgs(command.UserID, command.IdempotencyKey).
		WillReturnRows(claimRows().AddRow(41, 12, command.Code, command.UserID, "claimed", command.IdempotencyKey, claimedAt))
	mock.ExpectCommit()

	result, err := store.Claim(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.Claim.ID != 41 || result.Claim.CouponCode != command.Code {
		t.Fatalf("Claim() = %+v, want replay", result)
	}
}

func TestClaimRejectsIdempotencyKeyUsedForDifferentCoupon(t *testing.T) {
	store, mock := newMockStore(t)
	command := promotion.ClaimCommand{UserID: 7, Code: "WELCOME20", IdempotencyKey: "claim-key.01"}

	mock.ExpectBegin()
	mock.ExpectQuery(`
		select id from user_account where id=? for update`).
		WithArgs(command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(findClaimByIdempotencySQL).
		WithArgs(command.UserID, command.IdempotencyKey).
		WillReturnRows(claimRows().AddRow(41, 12, "OTHER20", command.UserID, "claimed", command.IdempotencyKey, time.Now()))
	mock.ExpectRollback()

	result, err := store.Claim(context.Background(), command)
	if !errors.Is(err, promotion.ErrIdempotencyConflict) {
		t.Fatalf("Claim() error = %v, want %v", err, promotion.ErrIdempotencyConflict)
	}
	if result != (promotion.ClaimResult{}) {
		t.Fatalf("Claim() result = %+v, want zero", result)
	}
}

func TestClaimReconcilesAmbiguousCommitInFreshTransaction(t *testing.T) {
	store, mock := newMockStore(t)
	command := promotion.ClaimCommand{UserID: 7, Code: "WELCOME20", IdempotencyKey: "claim-key.01"}
	effectiveNow := time.Date(2026, 9, 7, 8, 9, 10, 123456000, time.UTC)
	commitErr := errors.New("connection lost during commit")

	mock.ExpectBegin()
	mock.ExpectQuery(lockClaimUserSQL).WithArgs(command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(findClaimByIdempotencySQL).WithArgs(command.UserID, command.IdempotencyKey).
		WillReturnRows(claimRows())
	mock.ExpectQuery(lockClaimCouponSQL).WithArgs(command.UserID, command.Code).
		WillReturnRows(couponRows().AddRow(
			12, command.Code, "Welcome", "percentage", 0, 2000, "USD", 1000,
			10, 2, 1, 4, 6, "active", effectiveNow.Add(-time.Hour), effectiveNow.Add(time.Hour), 0,
		))
	mock.ExpectQuery(databaseNowSQL).
		WillReturnRows(sqlmock.NewRows([]string{"utc_timestamp(6)"}).AddRow(effectiveNow))
	mock.ExpectExec(insertClaimSQL).WithArgs(int64(12), command.UserID, command.IdempotencyKey, effectiveNow).
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectExec(incrementClaimedStockSQL).WithArgs(effectiveNow, int64(12)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(commitErr)

	// The ambiguous write is not replayed. A new transaction locks the same
	// stable user row and proves the durable idempotency record instead.
	mock.ExpectBegin()
	mock.ExpectQuery(lockClaimUserSQL).WithArgs(command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(findClaimByIdempotencySQL).WithArgs(command.UserID, command.IdempotencyKey).
		WillReturnRows(claimRows().AddRow(41, 12, command.Code, command.UserID, "claimed", command.IdempotencyKey, effectiveNow))
	mock.ExpectCommit()

	result, err := store.Claim(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.Claim.ID != 41 || result.Claim.CouponCode != command.Code {
		t.Fatalf("Claim() = %+v, want reconciled replay", result)
	}
}

func TestClaimReconcilesDurableReplayAfterRequestContextEnds(t *testing.T) {
	tests := []struct {
		name       string
		newContext func(t *testing.T) (context.Context, func())
		wantErr    error
	}{
		{
			name: "canceled",
			newContext: func(t *testing.T) (context.Context, func()) {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			newContext: func(t *testing.T) (context.Context, func()) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				t.Cleanup(cancel)
				return ctx, func() { <-ctx.Done() }
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := promotion.ClaimCommand{UserID: 7, Code: "WELCOME20", IdempotencyKey: "claim-key.01"}
			effectiveNow := time.Date(2026, 9, 7, 8, 9, 10, 123456000, time.UTC)
			commitErr := errors.New("connection lost during commit")
			ctx, endRequest := tt.newContext(t)
			state := &claimReconciliationState{
				command:      command,
				effectiveNow: effectiveNow,
				commitErr:    commitErr,
				endRequest:   endRequest,
			}
			store := newClaimReconciliationStore(t, state)

			result, err := store.Claim(ctx, command)
			if err != nil {
				t.Fatalf("Claim() error = %v, want durable replay", err)
			}
			if !errors.Is(ctx.Err(), tt.wantErr) {
				t.Fatalf("request context error = %v, want %v", ctx.Err(), tt.wantErr)
			}
			if !result.Replayed || result.Claim.ID != 41 || result.Claim.CouponCode != command.Code {
				t.Fatalf("Claim() = %+v, want reconciled durable replay", result)
			}
			state.assertReconciliationContexts(t)
		})
	}
}

func TestPromotionReconciliationContextDetachesAndBoundsRequest(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	reconcileCtx, cancelReconcile := promotionReconciliationContext(requestCtx)
	defer cancelReconcile()
	if err := reconcileCtx.Err(); err != nil {
		t.Fatalf("reconciliation context error = %v, want active context", err)
	}
	deadline, ok := reconcileCtx.Deadline()
	if !ok {
		t.Fatal("reconciliation context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > promotionReconciliationTimeout {
		t.Fatalf("reconciliation deadline remaining = %s, want (0,%s]", remaining, promotionReconciliationTimeout)
	}
}

func TestClaimPreservesAmbiguousCommitWhenReconciliationFindsNoClaim(t *testing.T) {
	store, mock := newMockStore(t)
	command := promotion.ClaimCommand{UserID: 7, Code: "WELCOME20", IdempotencyKey: "claim-key.01"}
	effectiveNow := time.Date(2026, 9, 7, 8, 9, 10, 123456000, time.UTC)
	commitErr := errors.New("connection lost during commit")

	mock.ExpectBegin()
	mock.ExpectQuery(lockClaimUserSQL).WithArgs(command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(findClaimByIdempotencySQL).WithArgs(command.UserID, command.IdempotencyKey).
		WillReturnRows(claimRows())
	mock.ExpectQuery(lockClaimCouponSQL).WithArgs(command.UserID, command.Code).
		WillReturnRows(couponRows().AddRow(
			12, command.Code, "Welcome", "percentage", 0, 2000, "USD", 1000,
			10, 2, 1, 4, 6, "active", effectiveNow.Add(-time.Hour), effectiveNow.Add(time.Hour), 0,
		))
	mock.ExpectQuery(databaseNowSQL).
		WillReturnRows(sqlmock.NewRows([]string{"utc_timestamp(6)"}).AddRow(effectiveNow))
	mock.ExpectExec(insertClaimSQL).WithArgs(int64(12), command.UserID, command.IdempotencyKey, effectiveNow).
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectExec(incrementClaimedStockSQL).WithArgs(effectiveNow, int64(12)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(commitErr)

	mock.ExpectBegin()
	mock.ExpectQuery(lockClaimUserSQL).WithArgs(command.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(command.UserID))
	mock.ExpectQuery(findClaimByIdempotencySQL).WithArgs(command.UserID, command.IdempotencyKey).
		WillReturnRows(claimRows())
	mock.ExpectRollback()

	result, err := store.Claim(context.Background(), command)
	if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) {
		t.Fatalf("Claim() error = %v, want original ambiguous commit", err)
	}
	if result != (promotion.ClaimResult{}) {
		t.Fatalf("Claim() result = %+v, want zero", result)
	}
}

func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return NewStore(db), mock
}

func claimRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "coupon_id", "code", "user_id", "status", "idempotency_key", "claimed_at",
	})
}

func couponRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "code", "name", "discount_type", "fixed_minor", "percentage_bps",
		"currency", "minimum_minor", "total_stock", "claimed_stock", "per_user_limit",
		"game_id", "edition_id", "status", "starts_at", "ends_at", "viewer_claim_count",
	})
}

type claimReconciliationState struct {
	mu sync.Mutex

	command      promotion.ClaimCommand
	effectiveNow time.Time
	commitErr    error
	endRequest   func()

	begins                int
	commits               int
	initialQueries        int
	initialExecs          int
	reconciliationQueries int
	reconciliationCtxErr  error
}

func newClaimReconciliationStore(t *testing.T, state *claimReconciliationState) *Store {
	t.Helper()
	db := sql.OpenDB(claimReconciliationConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close reconciliation database: %v", err)
		}
	})
	return NewStore(db)
}

func (s *claimReconciliationState) assertReconciliationContexts(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reconciliationCtxErr != nil {
		t.Fatal(s.reconciliationCtxErr)
	}
	if s.begins != 2 || s.commits != 2 || s.initialQueries != 4 || s.initialExecs != 2 || s.reconciliationQueries != 2 {
		t.Fatalf("begin/commit/initial-query/initial-exec/reconciliation-query = %d/%d/%d/%d/%d, want 2/2/4/2/2",
			s.begins, s.commits, s.initialQueries, s.initialExecs, s.reconciliationQueries)
	}
}

func (s *claimReconciliationState) checkReconciliationContext(ctx context.Context) {
	if s.reconciliationCtxErr != nil {
		return
	}
	if err := ctx.Err(); err != nil {
		s.reconciliationCtxErr = fmt.Errorf("reconciliation context is canceled: %w", err)
		return
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		s.reconciliationCtxErr = errors.New("reconciliation context has no deadline")
		return
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > promotionReconciliationTimeout {
		s.reconciliationCtxErr = fmt.Errorf("reconciliation deadline remaining = %s, want (0,%s]", remaining, promotionReconciliationTimeout)
	}
}

type claimReconciliationConnector struct{ state *claimReconciliationState }

func (c claimReconciliationConnector) Connect(context.Context) (driver.Conn, error) {
	return &claimReconciliationConn{state: c.state}, nil
}

func (c claimReconciliationConnector) Driver() driver.Driver {
	return claimReconciliationDriver{state: c.state}
}

type claimReconciliationDriver struct{ state *claimReconciliationState }

func (d claimReconciliationDriver) Open(string) (driver.Conn, error) {
	return &claimReconciliationConn{state: d.state}, nil
}

type claimReconciliationConn struct{ state *claimReconciliationState }

func (*claimReconciliationConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (*claimReconciliationConn) Close() error { return nil }

func (c *claimReconciliationConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *claimReconciliationConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.begins++
	if c.state.begins == 2 {
		c.state.checkReconciliationContext(ctx)
	}
	return &claimReconciliationTx{state: c.state}, nil
}

func (c *claimReconciliationConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	reconciling := c.state.begins == 2
	if reconciling {
		c.state.reconciliationQueries++
		c.state.checkReconciliationContext(ctx)
	} else {
		c.state.initialQueries++
	}

	switch strings.TrimSpace(query) {
	case strings.TrimSpace(lockClaimUserSQL):
		return &claimReconciliationRows{columns: []string{"id"}, values: [][]driver.Value{{c.state.command.UserID}}}, nil
	case strings.TrimSpace(findClaimByIdempotencySQL):
		rows := [][]driver.Value(nil)
		if reconciling {
			rows = [][]driver.Value{{int64(41), int64(12), c.state.command.Code, c.state.command.UserID, "claimed", c.state.command.IdempotencyKey, c.state.effectiveNow}}
		}
		return &claimReconciliationRows{columns: []string{"id", "coupon_id", "code", "user_id", "status", "idempotency_key", "claimed_at"}, values: rows}, nil
	case strings.TrimSpace(lockClaimCouponSQL):
		return &claimReconciliationRows{columns: []string{"id", "code", "name", "discount_type", "fixed_minor", "percentage_bps", "currency", "minimum_minor", "total_stock", "claimed_stock", "per_user_limit", "game_id", "edition_id", "status", "starts_at", "ends_at", "viewer_claim_count"}, values: [][]driver.Value{{
			int64(12), c.state.command.Code, "Welcome", "percentage", int64(0), int64(2000), "USD", int64(1000),
			int64(10), int64(2), int64(1), int64(4), int64(6), "active", c.state.effectiveNow.Add(-time.Hour), c.state.effectiveNow.Add(time.Hour), int64(0),
		}}}, nil
	case strings.TrimSpace(databaseNowSQL):
		return &claimReconciliationRows{columns: []string{"utc_timestamp(6)"}, values: [][]driver.Value{{c.state.effectiveNow}}}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (c *claimReconciliationConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.initialExecs++
	switch strings.TrimSpace(query) {
	case strings.TrimSpace(insertClaimSQL):
		return claimReconciliationResult{id: 41, affected: 1}, nil
	case strings.TrimSpace(incrementClaimedStockSQL):
		return claimReconciliationResult{affected: 1}, nil
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
}

type claimReconciliationTx struct{ state *claimReconciliationState }

func (tx *claimReconciliationTx) Commit() error {
	tx.state.mu.Lock()
	tx.state.commits++
	commitNumber := tx.state.commits
	commitErr := tx.state.commitErr
	endRequest := tx.state.endRequest
	tx.state.mu.Unlock()
	if commitNumber == 1 {
		endRequest()
		return commitErr
	}
	return nil
}

func (*claimReconciliationTx) Rollback() error { return nil }

type claimReconciliationRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *claimReconciliationRows) Columns() []string { return r.columns }
func (*claimReconciliationRows) Close() error        { return nil }
func (r *claimReconciliationRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

type claimReconciliationResult struct{ id, affected int64 }

func (r claimReconciliationResult) LastInsertId() (int64, error) { return r.id, nil }
func (r claimReconciliationResult) RowsAffected() (int64, error) { return r.affected, nil }
