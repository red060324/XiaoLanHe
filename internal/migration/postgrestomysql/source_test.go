package postgrestomysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestInspectSourceBindsFullCanonicalInventory(t *testing.T) {
	table := tinyTable()
	first := &sourceSnapshot{tx: &cutoverFakeSourceTx{queryRows: map[string][][]any{
		table.name: {{int64(1), "alpha"}, {int64(2), "beta"}},
	}}}
	_, inventory, rows, bytes, err := inspectSource(context.Background(), first, []tableSpec{table}, 2, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 || bytes <= 0 || inventory[table.name].Rows != 2 || first.identity.SnapshotID == "" {
		t.Fatalf("rows=%d bytes=%d inventory=%+v identity=%+v", rows, bytes, inventory, first.identity)
	}

	replay := &sourceSnapshot{tx: &cutoverFakeSourceTx{queryRows: map[string][][]any{
		table.name: {{int64(1), "alpha"}, {int64(2), "beta"}},
	}}}
	_, replayInventory, _, _, err := inspectSource(context.Background(), replay, []tableSpec{table}, 2, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if replay.identity.SnapshotID != first.identity.SnapshotID || !equalInventory(inventory, replayInventory) {
		t.Fatalf("stable source changed: first=%s replay=%s", first.identity.SnapshotID, replay.identity.SnapshotID)
	}

	drift := &sourceSnapshot{tx: &cutoverFakeSourceTx{queryRows: map[string][][]any{
		table.name: {{int64(1), "alpha"}, {int64(2), "changed"}},
	}}}
	_, driftInventory, _, _, err := inspectSource(context.Background(), drift, []tableSpec{table}, 2, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if drift.identity.SnapshotID == first.identity.SnapshotID || equalInventory(inventory, driftInventory) {
		t.Fatal("canonical source-row drift was not reflected in the resume binding")
	}
}

func TestInspectSourceFailsClosedAtGlobalRowAndByteCaps(t *testing.T) {
	table := tinyTable()
	rows := map[string][][]any{table.name: {{int64(1), "alpha"}, {int64(2), "beta"}}}
	for _, test := range []struct {
		name     string
		maxRows  int64
		maxBytes int64
	}{
		{name: "row cap", maxRows: 1, maxBytes: 1_000_000},
		{name: "byte cap", maxRows: 10, maxBytes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &sourceSnapshot{tx: &cutoverFakeSourceTx{queryRows: rows}}
			if _, _, _, _, err := inspectSource(context.Background(), source, []tableSpec{table}, test.maxRows, test.maxBytes); err == nil || !strings.Contains(err.Error(), "exceeds in-memory limit") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestReadAllRejectsNonIncreasingSourceKeys(t *testing.T) {
	table := tinyTable()
	source := &sourceSnapshot{tx: &cutoverFakeSourceTx{queryRows: map[string][][]any{
		table.name: {{int64(2), "beta"}, {int64(1), "alpha"}},
	}}}
	if _, _, err := source.readAll(context.Background(), table, 10, 1_000_000); err == nil || !strings.Contains(err.Error(), "not strictly increasing") {
		t.Fatalf("err=%v", err)
	}
}

type cutoverFakeSourceTx struct {
	queryRows  map[string][][]any
	rowValues  map[string][]any
	defaultRow []any
	err        error
}

func (*cutoverFakeSourceTx) Rollback(context.Context) error { return nil }
func (*cutoverFakeSourceTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (t *cutoverFakeSourceTx) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	if t.err != nil {
		return nil, t.err
	}
	for table, rows := range t.queryRows {
		if strings.Contains(query, " FROM "+table+" ORDER BY ") {
			return &cutoverFakePGXRows{rows: rows}, nil
		}
	}
	return nil, errors.New("unexpected source query: " + query)
}

func (t *cutoverFakeSourceTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	for fragment, values := range t.rowValues {
		if strings.Contains(query, fragment) {
			return cutoverFakePGXRow{values: values}
		}
	}
	if t.defaultRow != nil {
		return cutoverFakePGXRow{values: t.defaultRow}
	}
	return cutoverFakePGXRow{err: errors.New("unexpected source row query: " + query)}
}

type cutoverFakePGXRow struct {
	values []any
	err    error
}

func (r cutoverFakePGXRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(destinations) != len(r.values) {
		return errors.New("scan width mismatch")
	}
	for i, value := range r.values {
		if err := assignFakeSourceValue(destinations[i], value); err != nil {
			return err
		}
	}
	return nil
}

type cutoverFakePGXRows struct {
	rows  [][]any
	index int
	err   error
}

func (*cutoverFakePGXRows) Close()                                       {}
func (r *cutoverFakePGXRows) Err() error                                 { return r.err }
func (*cutoverFakePGXRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*cutoverFakePGXRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *cutoverFakePGXRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	r.index++
	return true
}
func (r *cutoverFakePGXRows) Scan(destinations ...any) error {
	if r.index == 0 || r.index > len(r.rows) {
		return errors.New("scan without current row")
	}
	values := r.rows[r.index-1]
	if len(destinations) != len(values) {
		return errors.New("scan width mismatch")
	}
	for i, value := range values {
		if err := assignFakeSourceValue(destinations[i], value); err != nil {
			return err
		}
	}
	return nil
}
func (r *cutoverFakePGXRows) Values() ([]any, error) {
	if r.index == 0 || r.index > len(r.rows) {
		return nil, errors.New("values without current row")
	}
	return append([]any(nil), r.rows[r.index-1]...), nil
}
func (*cutoverFakePGXRows) RawValues() [][]byte { return nil }
func (*cutoverFakePGXRows) Conn() *pgx.Conn     { return nil }

func assignFakeSourceValue(destination, value any) error {
	switch pointer := destination.(type) {
	case *int64:
		converted, ok := value.(int64)
		if !ok {
			return errors.New("value is not int64")
		}
		*pointer = converted
	case *string:
		converted, ok := value.(string)
		if !ok {
			return errors.New("value is not string")
		}
		*pointer = converted
	default:
		return errors.New("unsupported scan destination")
	}
	return nil
}

var _ pgx.Rows = (*cutoverFakePGXRows)(nil)
