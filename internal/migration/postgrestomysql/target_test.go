package postgrestomysql

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
)

func TestWriteBatchCommitsThenCheckpointRecordsExactDigests(t *testing.T) {
	state := &cutoverFakeState{rows: map[string]map[string]Row{}}
	db := sql.OpenDB(cutoverFakeConnector{state: state})
	defer db.Close()
	target := &targetDatabase{db: db}
	table := tinyTable()
	rows := []Row{{{Kind: kindInt, Value: "1"}, {Kind: kindString, Value: "alpha"}}, {{Kind: kindInt, Value: "2"}, {Kind: kindString, Value: "beta"}}}
	if err := target.writeBatch(context.Background(), table, rows); err != nil {
		t.Fatal(err)
	}
	if !state.committed || len(state.rows[table.name]) != 2 {
		t.Fatalf("committed=%t rows=%v", state.committed, state.rows)
	}
	if err := target.verifyBatch(context.Background(), table, rows); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := makeTableCheckpoint("sha256:identity", table, rows, rows, 0, 2)
	if err != nil || checkpoint.SourceRows != 2 || checkpoint.BatchSourceSHA256 != canonicalDigest(rows) || checkpoint.TargetCommittedSHA256 != canonicalDigest(rows) {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
}

func TestWriteBatchResumeAcceptsExactRowsAndRejectsDrift(t *testing.T) {
	state := &cutoverFakeState{rows: map[string]map[string]Row{"widget": {"1": {{Kind: kindInt, Value: "1"}, {Kind: kindString, Value: "alpha"}}}}}
	db := sql.OpenDB(cutoverFakeConnector{state: state})
	defer db.Close()
	target := &targetDatabase{db: db}
	table := tinyTable()
	exact := []Row{{{Kind: kindInt, Value: "1"}, {Kind: kindString, Value: "alpha"}}, {{Kind: kindInt, Value: "2"}, {Kind: kindString, Value: "beta"}}}
	if err := target.writeBatch(context.Background(), table, exact); err != nil {
		t.Fatal(err)
	}
	if len(state.rows["widget"]) != 2 {
		t.Fatalf("rows=%v", state.rows)
	}
	drift := []Row{{{Kind: kindInt, Value: "1"}, {Kind: kindString, Value: "changed"}}}
	if err := target.writeBatch(context.Background(), table, drift); !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("drift err=%v", err)
	}
}

func TestDeferredUpdateAndAutoIncrement(t *testing.T) {
	state := &cutoverFakeState{rows: map[string]map[string]Row{}, autoIncrement: map[string]int64{}}
	db := sql.OpenDB(cutoverFakeConnector{state: state})
	defer db.Close()
	table := tableSpec{name: "widget", columns: []columnSpec{col("id", kindInt), deferred("parent_id", kindInt)}, keyColumns: []int{0}, autoIncrement: true}
	source := []Row{{{Kind: kindInt, Value: "7"}, {Kind: kindInt, Value: "3"}}}
	state.rows["widget"] = map[string]Row{"7": table.initialRows(source)[0]}
	target := &targetDatabase{db: db}
	if err := target.writeDeferredBatch(context.Background(), table, source); err != nil {
		t.Fatal(err)
	}
	if got := state.rows["widget"]["7"][1]; got.Null || got.Value != "3" {
		t.Fatalf("deferred value=%+v", got)
	}
	if err := target.advanceAutoIncrement(context.Background(), table, source); err != nil {
		t.Fatal(err)
	}
	if state.autoIncrement["widget"] != 8 {
		t.Fatalf("AUTO_INCREMENT=%d", state.autoIncrement["widget"])
	}
}

func TestCouponClaimDeferredUpdateRestoresStatusAndOrderAtomically(t *testing.T) {
	var claim tableSpec
	for _, spec := range tables() {
		if spec.name == "coupon_claim" {
			claim = spec
			break
		}
	}
	row := make(Row, len(claim.columns))
	for i, column := range claim.columns {
		row[i] = Cell{Kind: column.kind, Null: true}
	}
	row[0] = Cell{Kind: kindInt, Value: "7"}
	row[3] = Cell{Kind: kindString, Value: "redeemed"}
	row[6] = Cell{Kind: kindInt, Value: "41"}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `coupon_claim` SET `status`=\\?,`redeemed_order_id`=\\? WHERE `id`=\\?").
		WithArgs("redeemed", int64(41), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := (&targetDatabase{db: db}).writeDeferredBatch(context.Background(), claim, []Row{row}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCouponClaimResumeAcceptsOnlyStagedOrAtomicallyRestoredState(t *testing.T) {
	table := tableSpec{
		name: "coupon_claim",
		columns: []columnSpec{
			col("id", kindInt),
			deferredWithInitial("status", kindString, "claimed"),
			deferred("redeemed_order_id", kindInt),
		},
		keyColumns: []int{0},
	}
	final := []Row{{
		{Kind: kindInt, Value: "7"},
		{Kind: kindString, Value: "redeemed"},
		{Kind: kindInt, Value: "41"},
	}}
	staged := table.initialRows(final)
	tests := []struct {
		name            string
		status          string
		redeemedOrderID any
		finalizedPrefix int
		wantErr         bool
	}{
		{name: "staged before checkpoint", status: "claimed", finalizedPrefix: 0},
		{name: "final before checkpoint", status: "redeemed", redeemedOrderID: int64(41), finalizedPrefix: 0},
		{name: "final after checkpoint", status: "redeemed", redeemedOrderID: int64(41), finalizedPrefix: 1},
		{name: "staged after checkpoint", status: "claimed", finalizedPrefix: 1, wantErr: true},
		{name: "status restored without order", status: "redeemed", finalizedPrefix: 0, wantErr: true},
		{name: "order restored without status", status: "claimed", redeemedOrderID: int64(41), finalizedPrefix: 0, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("SELECT `id`,`status`,`redeemed_order_id` FROM `coupon_claim` WHERE `id`=\\?").
				WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"id", "status", "redeemed_order_id"}).AddRow(int64(7), test.status, test.redeemedOrderID))

			err = verifyTargetResumableRows(context.Background(), db, table, staged, final, test.finalizedPrefix)
			if test.wantErr && !errors.Is(err, ErrCheckpointMismatch) {
				t.Fatalf("error=%v, want checkpoint mismatch", err)
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRequireEmptyTargetFailsClosed(t *testing.T) {
	state := &cutoverFakeState{count: map[string]int64{"widget": 1}}
	db := sql.OpenDB(cutoverFakeConnector{state: state})
	defer db.Close()
	runner := &Runner{target: &targetDatabase{db: db}, specs: []tableSpec{tinyTable()}}
	if err := runner.requireEmptyTarget(context.Background()); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err=%v", err)
	}
}

func tinyTable() tableSpec {
	return tableSpec{name: "widget", sourceFrom: "widget", columns: []columnSpec{col("id", kindInt), col("name", kindString)}, keyColumns: []int{0}, autoIncrement: true}
}

type cutoverFakeState struct {
	mu            sync.Mutex
	rows          map[string]map[string]Row
	count         map[string]int64
	autoIncrement map[string]int64
	committed     bool
}

type cutoverFakeConnector struct{ state *cutoverFakeState }

func (c cutoverFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &cutoverFakeConn{state: c.state}, nil
}
func (cutoverFakeConnector) Driver() driver.Driver { return cutoverFakeDriver{} }

type cutoverFakeDriver struct{}

func (cutoverFakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type cutoverFakeConn struct {
	state   *cutoverFakeState
	pending []func()
}

func (*cutoverFakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*cutoverFakeConn) Close() error                        { return nil }
func (c *cutoverFakeConn) Begin() (driver.Tx, error)         { return &cutoverFakeTx{conn: c}, nil }
func (c *cutoverFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c *cutoverFakeConn) ExecContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if strings.HasPrefix(query, "INSERT INTO `widget`") {
		row := Row{{Kind: kindInt, Value: fmt.Sprint(arguments[0].Value)}, {Kind: kindString, Value: fmt.Sprint(arguments[1].Value)}}
		key := row[0].Value
		c.pending = append(c.pending, func() {
			if c.state.rows["widget"] == nil {
				c.state.rows["widget"] = map[string]Row{}
			}
			c.state.rows["widget"][key] = row
		})
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "UPDATE `widget` SET `parent_id`") {
		value, key := fmt.Sprint(arguments[0].Value), fmt.Sprint(arguments[1].Value)
		c.pending = append(c.pending, func() {
			row := c.state.rows["widget"][key]
			row[1] = Cell{Kind: kindInt, Value: value}
			c.state.rows["widget"][key] = row
		})
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "ALTER TABLE `widget` AUTO_INCREMENT = ") {
		var next int64
		_, _ = fmt.Sscanf(query, "ALTER TABLE `widget` AUTO_INCREMENT = %d", &next)
		if c.state.autoIncrement == nil {
			c.state.autoIncrement = map[string]int64{}
		}
		c.state.autoIncrement["widget"] = next
		return driver.RowsAffected(0), nil
	}
	return nil, fmt.Errorf("unexpected exec: %s", query)
}

func (c *cutoverFakeConn) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if strings.HasPrefix(query, "SELECT `id`,`name` FROM `widget` WHERE") {
		row, ok := c.state.rows["widget"][fmt.Sprint(arguments[0].Value)]
		if !ok {
			return &cutoverRows{columns: []string{"id", "name"}}, nil
		}
		return &cutoverRows{columns: []string{"id", "name"}, rows: [][]driver.Value{{mustDriver(row[0]), mustDriver(row[1])}}}, nil
	}
	if strings.HasPrefix(query, "SELECT `id`,`parent_id` FROM `widget` WHERE") {
		row := c.state.rows["widget"][fmt.Sprint(arguments[0].Value)]
		return &cutoverRows{columns: []string{"id", "parent_id"}, rows: [][]driver.Value{{mustDriver(row[0]), mustDriver(row[1])}}}, nil
	}
	if query == "SELECT COUNT(*) FROM `widget`" {
		count := c.state.count["widget"]
		if c.state.count == nil {
			count = int64(len(c.state.rows["widget"]))
		}
		return &cutoverRows{columns: []string{"count"}, rows: [][]driver.Value{{count}}}, nil
	}
	if strings.HasPrefix(query, "SELECT AUTO_INCREMENT FROM information_schema.tables") {
		return &cutoverRows{columns: []string{"auto_increment"}, rows: [][]driver.Value{{c.state.autoIncrement["widget"]}}}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

func mustDriver(cell Cell) driver.Value { value, _ := cell.driverValue(); return value }

type cutoverFakeTx struct{ conn *cutoverFakeConn }

func (t *cutoverFakeTx) Commit() error {
	for _, apply := range t.conn.pending {
		apply()
	}
	t.conn.pending = nil
	t.conn.state.committed = true
	return nil
}
func (t *cutoverFakeTx) Rollback() error { t.conn.pending = nil; return nil }

type cutoverRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *cutoverRows) Columns() []string { return r.columns }
func (*cutoverRows) Close() error        { return nil }
func (r *cutoverRows) Next(destination []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(destination, r.rows[r.index])
	r.index++
	return nil
}

var _ driver.ConnBeginTx = (*cutoverFakeConn)(nil)
var _ driver.ExecerContext = (*cutoverFakeConn)(nil)
var _ driver.QueryerContext = (*cutoverFakeConn)(nil)
var _ = time.Time{}
