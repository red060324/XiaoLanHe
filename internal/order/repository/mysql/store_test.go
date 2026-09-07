package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/order/entity"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
)

func TestNewStoreImplementsOrderStore(t *testing.T) {
	store := NewStore(&sql.DB{})
	var contract order.Store = store
	if contract == nil || store.db == nil {
		t.Fatalf("NewStore() = %#v, want store backed by supplied database", store)
	}
}

func TestOrderSQLUsesMySQLDialect(t *testing.T) {
	queries := []string{orderSelect, strictPaymentReplaySelect, lockUserSQL, lockActiveEntitlementSQL, strictPaymentReplayWhere}
	for _, query := range queries {
		lower := strings.ToLower(query)
		for _, forbidden := range []string{"$1", "::jsonb", " returning ", "on conflict", "pg_advisory", "statement_timestamp"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("query contains PostgreSQL syntax %q: %s", forbidden, query)
			}
		}
	}
}

func TestOrderSelectUsesCardinalityConstrainedJoins(t *testing.T) {
	for name, query := range map[string]string{
		"order":          orderSelect,
		"payment replay": strictPaymentReplaySelect,
	} {
		if !strings.Contains(query, "join purchase_order_item i on i.order_id = o.id") {
			t.Fatalf("%s query does not join the schema-constrained single item: %s", name, query)
		}
		if !strings.Contains(query, "payment_record p on p.order_id = o.id and p.paid_payment = 1") {
			t.Fatalf("%s query does not use the unique paid-payment marker: %s", name, query)
		}
		if strings.Contains(query, "p.order_id = o.id and p.status = 'paid'") {
			t.Fatalf("%s query still relies on an unconstrained status join: %s", name, query)
		}
	}
}

func TestStrictPaymentReplayRequiresEntitlementAndExactPaymentIdentity(t *testing.T) {
	for _, fragment := range []string{
		"o.status = 'paid'",
		"p.status = 'paid'",
		"p.provider = ?",
		"p.provider_reference = ?",
		"p.idempotency_key = ?",
		"p.amount_minor = o.total_minor",
		"e.source_order_id = o.id",
	} {
		if !strings.Contains(strictPaymentReplayWhere, fragment) {
			t.Fatalf("strict payment replay query is missing %q", fragment)
		}
	}
	query := strictPaymentReplaySelect + " where " + strictPaymentReplayWhere + " for update"
	if !strings.HasSuffix(strings.TrimSpace(query), "for update") {
		t.Fatal("strict payment replay is not a locking read")
	}
	if !strings.Contains(strictPaymentReplaySelect, "join game_entitlement e") {
		t.Fatal("strict payment replay does not explicitly join the entitlement row")
	}
}

func TestDifferentEntitlementOrderIsNeverAReplay(t *testing.T) {
	currentOrderID := int64(42)
	for _, sourceOrderID := range []sql.NullInt64{
		{},
		{Int64: 41, Valid: true},
	} {
		if sameEntitlementOrder(sourceOrderID, currentOrderID) {
			t.Fatalf("sameEntitlementOrder(%+v, %d) = true", sourceOrderID, currentOrderID)
		}
	}
	if !sameEntitlementOrder(sql.NullInt64{Int64: currentOrderID, Valid: true}, currentOrderID) {
		t.Fatal("matching entitlement source order was rejected")
	}
}

func TestPayReplaysOnlyStrictSameOrderPaymentAndEntitlement(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 123456000, time.UTC)
	command := order.PayCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", UserID: 7,
		IdempotencyKey: "payment-key.01", ProviderReference: "sandbox:ord_0123456789abcdef0123456789abcdef",
	}
	state := &scriptState{steps: []scriptStep{
		{queryContains: lockUserSQL, columns: []string{"id"}, rows: [][]driver.Value{{command.UserID}}},
		{queryContains: "p.provider_reference = ?", columns: orderColumns, rows: [][]driver.Value{paidOrderValues(now, command)}},
	}}
	store := newScriptStore(t, state)

	result, err := store.Pay(context.Background(), command)
	if err != nil {
		t.Fatalf("Pay() error = %v", err)
	}
	if !result.Replayed || result.Order.ID != 42 || result.Order.Payment == nil ||
		result.Order.Payment.Provider != paymentProvider || result.Order.Payment.ProviderReference != command.ProviderReference {
		t.Fatalf("Pay() = %+v, want strict replay", result)
	}
	state.assertDone(t, 1, 1, 0)
	args := state.arguments(t, 1)
	if len(args) != 5 || args[2].Value != paymentProvider || args[3].Value != command.ProviderReference || args[4].Value != command.IdempotencyKey {
		t.Fatalf("strict replay arguments = %#v", args)
	}
}

func TestPayRejectsEntitlementFromDifferentOrderAndRollsBack(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 123456000, time.UTC)
	command := order.PayCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", UserID: 7,
		IdempotencyKey: "payment-key.01", ProviderReference: "sandbox:ord_0123456789abcdef0123456789abcdef",
	}
	state := &scriptState{steps: []scriptStep{
		{queryContains: lockUserSQL, columns: []string{"id"}, rows: [][]driver.Value{{command.UserID}}},
		{queryContains: "p.provider_reference = ?", columns: orderColumns},
		{queryContains: "where o.order_no = ?", columns: orderColumns, rows: [][]driver.Value{pendingOrderValues(now, command)}},
		{queryContains: lockActiveEntitlementSQL, columns: []string{"source_order_id"}, rows: [][]driver.Value{{int64(41)}}},
	}}
	store := newScriptStore(t, state)

	result, err := store.Pay(context.Background(), command)
	if !errors.Is(err, order.ErrAlreadyOwned) {
		t.Fatalf("Pay() error = %v, want %v", err, order.ErrAlreadyOwned)
	}
	if result != (order.PayResult{}) {
		t.Fatalf("Pay() = %+v, want zero result", result)
	}
	state.assertDone(t, 1, 0, 1)
}

func TestPayRejectsSameOrderEntitlementWithoutExactPaymentReplay(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 123456000, time.UTC)
	command := order.PayCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", UserID: 7,
		IdempotencyKey: "different-payment-key", ProviderReference: "sandbox:ord_0123456789abcdef0123456789abcdef",
	}
	state := &scriptState{steps: []scriptStep{
		{queryContains: lockUserSQL, columns: []string{"id"}, rows: [][]driver.Value{{command.UserID}}},
		{queryContains: "p.provider_reference = ?", columns: orderColumns},
		{queryContains: "where o.order_no = ?", columns: orderColumns, rows: [][]driver.Value{paidOrderWithoutJoinedPaymentValues(now, command)}},
		{queryContains: lockActiveEntitlementSQL, columns: []string{"source_order_id"}, rows: [][]driver.Value{{int64(42)}}},
	}}
	store := newScriptStore(t, state)

	_, err := store.Pay(context.Background(), command)
	if !errors.Is(err, order.ErrIdempotencyConflict) {
		t.Fatalf("Pay() error = %v, want %v", err, order.ErrIdempotencyConflict)
	}
	state.assertDone(t, 1, 0, 1)
}

func TestCreateUsesDatabaseTimeAfterUserLockForPriceLookup(t *testing.T) {
	callerNow := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	databaseTime := callerNow.Add(2 * time.Hour)
	command := order.CreateCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", UserID: 7, IdempotencyKey: "create-key.01", Now: callerNow,
	}
	command.Offer.EditionID = 3
	command.Offer.GameID = 2
	command.Offer.GameSlug = "game"
	command.Offer.GameName = "Game"
	command.Offer.EditionCode = "standard"
	command.Offer.EditionName = "Standard"
	command.Offer.Currency = "USD"
	command.Offer.Region = "GLOBAL"
	command.Offer.AmountMinor = 1000
	state := &scriptState{steps: []scriptStep{
		{queryContains: lockUserSQL, columns: []string{"id"}, rows: [][]driver.Value{{command.UserID}}},
		{queryContains: "o.idempotency_key = ?", columns: orderColumns},
		{queryContains: "select exists(", columns: []string{"owned"}, rows: [][]driver.Value{{false}}},
		{queryContains: "select utc_timestamp(6)", columns: []string{"now"}, rows: [][]driver.Value{{databaseTime}}},
		{queryContains: "from game_edition e", columns: []string{"edition_id", "game_id", "slug", "game_name", "code", "edition_name"}, rows: [][]driver.Value{{int64(3), int64(2), "game", "Game", "standard", "Standard"}}},
		{queryContains: "select amount_minor, active_from, active_until", columns: []string{"amount_minor", "active_from", "active_until"}},
	}}
	store := newScriptStore(t, state)

	_, err := store.Create(context.Background(), command)
	if !errors.Is(err, order.ErrPriceUnavailable) {
		t.Fatalf("Create() error = %v, want %v", err, order.ErrPriceUnavailable)
	}
	state.assertDone(t, 1, 0, 1)
	priceArgs := state.arguments(t, 5)
	if priceArgs[3].Value != databaseTime || priceArgs[4].Value != databaseTime {
		t.Fatalf("price lookup times = %v, %v, want database time %v", priceArgs[3].Value, priceArgs[4].Value, databaseTime)
	}
}

func TestReconcilePayPreservesAmbiguousCommitWhenDurableIdentityIsAbsent(t *testing.T) {
	command := order.PayCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", UserID: 7,
		IdempotencyKey: "payment-key.01", ProviderReference: "sandbox:ord_0123456789abcdef0123456789abcdef",
	}
	state := &scriptState{steps: []scriptStep{
		{queryContains: lockUserSQL, columns: []string{"id"}, rows: [][]driver.Value{{command.UserID}}},
		{queryContains: "p.provider_reference = ?", columns: orderColumns},
	}}
	store := newScriptStore(t, state)
	commitErr := &mysqltx.CommitOutcomeUnknownError{Err: errors.New("connection lost during commit")}

	_, err := store.reconcilePay(context.Background(), command, commitErr)
	if err != commitErr || !mysqltx.IsCommitOutcomeUnknown(err) {
		t.Fatalf("reconcilePay() error = %v, want original ambiguous commit error", err)
	}
	state.assertDone(t, 1, 0, 1)
}

func TestReconcileCreateRejectsSameIdempotencyKeyWithDifferentAmount(t *testing.T) {
	command := order.CreateCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", UserID: 7, IdempotencyKey: "create-key.01",
		TotalMinor: 800,
	}
	command.Offer.EditionID = 3
	command.Offer.Currency = "USD"
	command.Offer.Region = "GLOBAL"
	command.Offer.AmountMinor = 1000
	command.Quote.ClaimID = 5
	command.Quote.DiscountMinor = 200
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	values := []driver.Value{
		int64(42), command.OrderNo, command.UserID, "pending_payment", "USD",
		int64(1200), int64(200), int64(1000), command.Quote.ClaimID,
		int64(3), int64(2), "game", "Game", "standard", "Standard", int64(1200), "GLOBAL",
		nil, nil, nil, nil, nil, nil, "standard", "", nil, now, now,
	}
	state := &scriptState{steps: []scriptStep{
		{queryContains: lockUserSQL, columns: []string{"id"}, rows: [][]driver.Value{{command.UserID}}},
		{queryContains: "o.idempotency_key = ?", columns: orderColumns, rows: [][]driver.Value{values}},
	}}
	store := newScriptStore(t, state)
	commitErr := &mysqltx.CommitOutcomeUnknownError{Err: errors.New("connection lost during commit")}

	_, err := store.reconcileCreate(context.Background(), command, commitErr)
	if !errors.Is(err, order.ErrIdempotencyConflict) {
		t.Fatalf("reconcileCreate() error = %v, want %v", err, order.ErrIdempotencyConflict)
	}
	state.assertDone(t, 1, 0, 1)
}

func TestMatchesCommandRequiresStandardOrderIdentity(t *testing.T) {
	command := order.CreateCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", UserID: 7, TotalMinor: 800,
	}
	command.Offer.EditionID = 3
	command.Offer.GameID = 2
	command.Offer.GameSlug = "game"
	command.Offer.GameName = "Game"
	command.Offer.EditionCode = "standard"
	command.Offer.EditionName = "Standard"
	command.Offer.Currency = "USD"
	command.Offer.Region = "GLOBAL"
	command.Offer.AmountMinor = 1000
	command.Quote.ClaimID = 5
	command.Quote.DiscountMinor = 200
	existing := entity.Order{
		OrderNo: command.OrderNo, UserID: command.UserID, Status: entity.StatusPendingPayment, Currency: command.Offer.Currency,
		SubtotalMinor: command.Offer.AmountMinor, DiscountMinor: command.Quote.DiscountMinor, TotalMinor: command.TotalMinor,
		CouponClaimID: command.Quote.ClaimID, SourceType: "standard",
		Item: entity.Item{EditionID: command.Offer.EditionID, GameID: command.Offer.GameID, GameSlug: command.Offer.GameSlug,
			GameName: command.Offer.GameName, EditionCode: command.Offer.EditionCode, EditionName: command.Offer.EditionName,
			Region: command.Offer.Region, UnitPriceMinor: command.Offer.AmountMinor},
	}
	if !matchesCommand(existing, command) {
		t.Fatal("matching standard order command was rejected")
	}

	cases := map[string]func(*entity.Order){
		"subtotal":           func(value *entity.Order) { value.SubtotalMinor++ },
		"discount":           func(value *entity.Order) { value.DiscountMinor++ },
		"total":              func(value *entity.Order) { value.TotalMinor++ },
		"item price":         func(value *entity.Order) { value.Item.UnitPriceMinor++ },
		"source type":        func(value *entity.Order) { value.SourceType = "flash_sale" },
		"source reference":   func(value *entity.Order) { value.SourceReference = "fsr_1_0123456789abcdef0123456789abcdef" },
		"payment expiration": func(value *entity.Order) { value.PaymentExpiresAt = time.Now().UTC() },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := existing
			mutate(&changed)
			if matchesCommand(changed, command) {
				t.Fatalf("mismatched %s was accepted", name)
			}
		})
	}
}

func TestScanOrderHandlesNullablePaymentAndExpiry(t *testing.T) {
	createdAt := time.Date(2026, 9, 7, 12, 0, 0, 123456000, time.FixedZone("test", 8*60*60))
	row := &valueScanner{values: []any{
		int64(1), "ord_0123456789abcdef0123456789abcdef", int64(7), "pending_payment", "USD", int64(1000), int64(0), int64(1000), int64(0),
		int64(3), int64(2), "game", "Game", "standard", "Standard", int64(1000), "GLOBAL",
		nil, nil, nil, nil, nil, nil,
		"standard", "", nil, createdAt, createdAt,
	}}
	got, err := scanOrder(row)
	if err != nil {
		t.Fatalf("scanOrder() error = %v", err)
	}
	if got.Payment != nil || !got.PaymentExpiresAt.IsZero() {
		t.Fatalf("scanOrder() nullable fields = payment %#v, expiry %v", got.Payment, got.PaymentExpiresAt)
	}
	if got.CreatedAt.Location() != time.UTC || got.UpdatedAt.Location() != time.UTC {
		t.Fatalf("scanOrder() did not normalize timestamps to UTC: %#v", got)
	}
}

func TestScanOrderRejectsIncompletePaidPayment(t *testing.T) {
	createdAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	row := &valueScanner{values: []any{
		int64(1), "ord_0123456789abcdef0123456789abcdef", int64(7), "paid", "USD", int64(1000), int64(0), int64(1000), int64(0),
		int64(3), int64(2), "game", "Game", "standard", "Standard", int64(1000), "GLOBAL",
		int64(9), nil, "sandbox:order", "paid", int64(1000), createdAt,
		"standard", "", nil, createdAt, createdAt,
	}}
	if _, err := scanOrder(row); err == nil {
		t.Fatal("scanOrder() error = nil, want incomplete payment error")
	}
}

func TestMatchesFlashSaleCommandChecksEveryPersistedIdentityField(t *testing.T) {
	expiresAt := time.Date(2026, 9, 7, 13, 0, 0, 0, time.UTC)
	command := order.FlashSaleCreateCommand{
		OrderNo: "ord_0123456789abcdef0123456789abcdef", RequestID: "fsr_7_abababababababababababababababab",
		UserID: 7, SalePriceMinor: 900, PaymentExpiresAt: expiresAt,
	}
	command.Offer.EditionID = 3
	command.Offer.GameID = 2
	command.Offer.GameSlug = "game"
	command.Offer.GameName = "Game"
	command.Offer.EditionCode = "standard"
	command.Offer.EditionName = "Standard"
	command.Offer.Currency = "USD"
	command.Offer.Region = "GLOBAL"
	existing := entity.Order{
		OrderNo: command.OrderNo, UserID: 7, Status: entity.StatusPendingPayment, Currency: "USD", SubtotalMinor: 900, TotalMinor: 900,
		SourceType: "flash_sale", SourceReference: command.RequestID, PaymentExpiresAt: expiresAt,
		Item: entity.Item{EditionID: 3, GameID: 2, GameSlug: "game", GameName: "Game", EditionCode: "standard",
			EditionName: "Standard", Region: "GLOBAL", UnitPriceMinor: 900},
	}
	if !matchesFlashSaleCommand(existing, command) {
		t.Fatal("matching flash-sale command was rejected")
	}
	existing.Item.UnitPriceMinor++
	if matchesFlashSaleCommand(existing, command) {
		t.Fatal("mismatched flash-sale snapshot was accepted")
	}
}

func TestIsDuplicateOnlyMatchesMySQLError1062(t *testing.T) {
	if !isDuplicate(&drivermysql.MySQLError{Number: duplicateEntry}) {
		t.Fatal("duplicate error was not classified")
	}
	if isDuplicate(&drivermysql.MySQLError{Number: 1213}) || isDuplicate(errors.New("duplicate")) {
		t.Fatal("non-duplicate error was classified as duplicate")
	}
}

type valueScanner struct {
	values []any
	err    error
}

var orderColumns = []string{
	"id", "order_no", "user_id", "status", "currency", "subtotal_minor", "discount_minor", "total_minor", "coupon_claim_id",
	"edition_id", "game_id", "game_slug_snapshot", "game_name_snapshot", "edition_code_snapshot", "edition_name_snapshot", "unit_price_minor", "region_code",
	"payment_id", "provider", "provider_reference", "payment_status", "payment_amount", "payment_created_at",
	"source_type", "source_reference", "payment_expires_at", "created_at", "updated_at",
}

func pendingOrderValues(now time.Time, command order.PayCommand) []driver.Value {
	return []driver.Value{
		int64(42), command.OrderNo, command.UserID, "pending_payment", "USD", int64(1000), int64(0), int64(1000), int64(0),
		int64(3), int64(2), "game", "Game", "standard", "Standard", int64(1000), "GLOBAL",
		nil, nil, nil, nil, nil, nil, "standard", "", nil, now, now,
	}
}

func paidOrderWithoutJoinedPaymentValues(now time.Time, command order.PayCommand) []driver.Value {
	values := pendingOrderValues(now, command)
	values[3] = "paid"
	return values
}

func paidOrderValues(now time.Time, command order.PayCommand) []driver.Value {
	values := paidOrderWithoutJoinedPaymentValues(now, command)
	values[17] = int64(11)
	values[18] = paymentProvider
	values[19] = command.ProviderReference
	values[20] = "paid"
	values[21] = int64(1000)
	values[22] = now
	return values
}

func newScriptStore(t *testing.T, state *scriptState) *Store {
	t.Helper()
	db := sql.OpenDB(scriptConnector{state: state})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return NewStore(db)
}

type scriptStep struct {
	queryContains string
	args          []driver.NamedValue
	columns       []string
	rows          [][]driver.Value
	err           error
}

type scriptState struct {
	mu                     sync.Mutex
	steps                  []scriptStep
	next                   int
	begins, commits, rolls int
	isolation              []driver.IsolationLevel
}

func (s *scriptState) take(query string, args []driver.NamedValue) scriptStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.steps) {
		return scriptStep{err: errors.New("unexpected database call: " + query)}
	}
	step := s.steps[s.next]
	s.next++
	if !strings.Contains(strings.ToLower(query), strings.ToLower(strings.TrimSpace(step.queryContains))) {
		return scriptStep{err: errors.New("unexpected SQL: " + query)}
	}
	step.args = append([]driver.NamedValue(nil), args...)
	s.steps[s.next-1] = step
	return step
}

func (s *scriptState) arguments(t *testing.T, index int) []driver.NamedValue {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]driver.NamedValue(nil), s.steps[index].args...)
}

func (s *scriptState) assertDone(t *testing.T, begins, commits, rolls int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next != len(s.steps) || s.begins != begins || s.commits != commits || s.rolls != rolls {
		t.Fatalf("steps=%d/%d begin/commit/rollback=%d/%d/%d, want %d/%d/%d",
			s.next, len(s.steps), s.begins, s.commits, s.rolls, begins, commits, rolls)
	}
	for _, isolation := range s.isolation {
		if isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
			t.Fatalf("transaction isolation = %v, want ReadCommitted", isolation)
		}
	}
}

type scriptConnector struct{ state *scriptState }

func (c scriptConnector) Connect(context.Context) (driver.Conn, error) {
	return &scriptConn{state: c.state}, nil
}
func (c scriptConnector) Driver() driver.Driver { return scriptDriver{} }

type scriptDriver struct{}

func (scriptDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type scriptConn struct{ state *scriptState }

func (*scriptConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (*scriptConn) Close() error { return nil }
func (c *scriptConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *scriptConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	c.state.begins++
	c.state.isolation = append(c.state.isolation, options.Isolation)
	c.state.mu.Unlock()
	return &scriptTx{state: c.state}, nil
}
func (c *scriptConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	step := c.state.take(query, args)
	if step.err != nil {
		return nil, step.err
	}
	return &scriptRows{columns: step.columns, rows: step.rows}, nil
}

type scriptTx struct{ state *scriptState }

func (tx *scriptTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.commits++
	return nil
}
func (tx *scriptTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rolls++
	return nil
}

type scriptRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (r *scriptRows) Columns() []string { return r.columns }
func (*scriptRows) Close() error        { return nil }
func (r *scriptRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

func (s *valueScanner) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	if len(dest) != len(s.values) {
		return errors.New("destination count mismatch")
	}
	for i, value := range s.values {
		switch target := dest[i].(type) {
		case *int64:
			*target = value.(int64)
		case *string:
			*target = value.(string)
		case *entity.Status:
			*target = entity.Status(value.(string))
		case *time.Time:
			*target = value.(time.Time)
		case *sql.NullInt64:
			if value != nil {
				*target = sql.NullInt64{Int64: value.(int64), Valid: true}
			}
		case *sql.NullString:
			if value != nil {
				*target = sql.NullString{String: value.(string), Valid: true}
			}
		case *sql.NullTime:
			if value != nil {
				*target = sql.NullTime{Time: value.(time.Time), Valid: true}
			}
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}
