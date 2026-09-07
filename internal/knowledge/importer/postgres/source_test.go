package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestBindSourceBuildsStableDescriptor(t *testing.T) {
	columns := requiredSchemaColumns()
	tx := &fakeTx{
		rowResults: []row{
			&fakeRow{values: []any{"16384", "legacy", "160004", "10.0.0.8", int32(5432)}},
			&fakeRow{values: []any{"00000003-0000007B-1"}},
		},
		queryResults: []rowSet{newFakeRows(columnsToValues(columns))},
	}
	source, err := bindSource(context.Background(), &fakePool{}, tx)
	if err != nil {
		t.Fatalf("bindSource() error = %v", err)
	}

	descriptor := source.Descriptor()
	wantIdentity := canonicalDigest(databaseIdentity{
		DatabaseOID: "16384", DatabaseName: "legacy", ServerVersion: "160004",
		ServerAddress: "10.0.0.8", ServerPort: 5432,
	})
	if descriptor.IdentitySHA256 != wantIdentity || descriptor.SchemaSHA256 != canonicalDigest(columns) {
		t.Fatalf("Descriptor() = %#v, want identity %q and schema digest", descriptor, wantIdentity)
	}
	if descriptor.SnapshotID != "00000003-0000007B-1" || descriptor.Isolation != "repeatable_read" || !descriptor.ReadOnly {
		t.Fatalf("Descriptor() snapshot properties = %#v", descriptor)
	}
	if source.documents != `"legacy_schema"."knowledge_document"` || source.chunks != `"legacy_schema"."knowledge_chunk"` {
		t.Fatalf("bound tables = %q, %q", source.documents, source.chunks)
	}
	if strings.Contains(identitySQL, "pg_control_system") {
		t.Fatal("identity query must not require pg_control_system privileges")
	}
}

func TestListLegacyKnowledgeReadsFullManifestRow(t *testing.T) {
	published := time.Date(2024, 5, 2, 3, 4, 5, 0, time.FixedZone("offset", 8*60*60))
	created := published.Add(time.Hour)
	updated := created.Add(time.Minute)
	tx := &fakeTx{queryResults: []rowSet{newFakeRows([][]any{{
		int64(9), "guide", "Build", "https://example.test/build", "game", "cn", "1.2", "body",
		`{"rank":1}`, &published, created, updated, int64(7),
	}})}}
	source := &Source{tx: tx, documents: `"legacy"."knowledge_document"`, chunks: `"legacy"."knowledge_chunk"`}

	documents, err := source.ListLegacyKnowledge(context.Background(), 8, 25)
	if err != nil {
		t.Fatalf("ListLegacyKnowledge() error = %v", err)
	}
	if len(documents) != 1 {
		t.Fatalf("document count = %d, want 1", len(documents))
	}
	document := documents[0]
	if document.ID != 9 || document.Draft.SourceType != "guide" || document.Draft.Title != "Build" ||
		document.Draft.SourceURL != "https://example.test/build" || document.Draft.GameCode != "game" ||
		document.Draft.RegionCode != "cn" || document.Draft.PatchVersion != "1.2" || document.Draft.ContentText != "body" ||
		string(document.Metadata) != `{"rank":1}` || document.PublishedAt == nil || !document.PublishedAt.Equal(published) ||
		!document.CreatedAt.Equal(created) || !document.UpdatedAt.Equal(updated) || document.LegacyChunkCount != 7 {
		t.Fatalf("document = %#v", document)
	}
	if len(tx.queries) != 1 || !strings.Contains(tx.queries[0].sql, "document.metadata::text") ||
		!strings.Contains(tx.queries[0].sql, "count(*)::bigint") || !strings.Contains(tx.queries[0].sql, "order by document.id asc") {
		t.Fatalf("query did not preserve manifest fields/order: %q", tx.queries[0].sql)
	}
	if !reflect.DeepEqual(tx.queries[0].args, []any{int64(8), 25}) {
		t.Fatalf("query args = %#v", tx.queries[0].args)
	}
}

func TestListLegacyKnowledgeRejectsNonIncreasingIDs(t *testing.T) {
	now := time.Now().UTC()
	row := func(id int64) []any {
		return []any{id, "guide", "title", "", "", "", "", "body", `{}`, (*time.Time)(nil), now, now, int64(0)}
	}
	tx := &fakeTx{queryResults: []rowSet{newFakeRows([][]any{row(5), row(5)})}}
	source := &Source{tx: tx, documents: `"public"."knowledge_document"`, chunks: `"public"."knowledge_chunk"`}

	if _, err := source.ListLegacyKnowledge(context.Background(), 4, 2); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("ListLegacyKnowledge() error = %v, want invalid ordering", err)
	}
}

func requiredSchemaColumns() []schemaColumn {
	var columns []schemaColumn
	add := func(table string, names ...string) {
		for index, name := range names {
			columns = append(columns, schemaColumn{
				LogicalTable: table, Schema: "legacy_schema", Table: table, RelationOID: map[string]string{"knowledge_chunk": "41", "knowledge_document": "42"}[table],
				Position: int16(index + 1), Name: name, DataType: "text", NotNull: name == "id", Default: "",
			})
		}
	}
	add("knowledge_chunk", "id", "document_id")
	add("knowledge_document", "id", "source_type", "title", "source_url", "game_code", "region_code", "patch_version", "metadata", "content_text", "published_at", "created_at", "updated_at")
	return columns
}

func columnsToValues(columns []schemaColumn) [][]any {
	values := make([][]any, 0, len(columns))
	for _, column := range columns {
		values = append(values, []any{column.LogicalTable, column.Schema, column.Table, column.RelationOID, column.Position, column.Name, column.DataType, column.NotNull, column.Default})
	}
	return values
}

type fakePool struct{ closed bool }

func (p *fakePool) BeginTx(context.Context, pgx.TxOptions) (snapshotTx, error) {
	return nil, errors.New("unexpected BeginTx")
}
func (p *fakePool) Close() { p.closed = true }

type recordedQuery struct {
	sql  string
	args []any
}

type fakeTx struct {
	rowResults   []row
	queryResults []rowSet
	queries      []recordedQuery
	rolledBack   bool
}

func (tx *fakeTx) Query(_ context.Context, sql string, args ...any) (rowSet, error) {
	tx.queries = append(tx.queries, recordedQuery{sql: sql, args: append([]any(nil), args...)})
	if len(tx.queryResults) == 0 {
		return nil, errors.New("unexpected query")
	}
	result := tx.queryResults[0]
	tx.queryResults = tx.queryResults[1:]
	return result, nil
}

func (tx *fakeTx) QueryRow(context.Context, string, ...any) row {
	if len(tx.rowResults) == 0 {
		return &fakeRow{err: errors.New("unexpected query row")}
	}
	result := tx.rowResults[0]
	tx.rowResults = tx.rowResults[1:]
	return result
}

func (tx *fakeTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type fakeRow struct {
	values []any
	err    error
}

func (r *fakeRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	return assign(destinations, r.values)
}

type fakeRows struct {
	values [][]any
	index  int
	closed bool
	err    error
}

func newFakeRows(values [][]any) *fakeRows { return &fakeRows{values: values, index: -1} }
func (r *fakeRows) Close()                 { r.closed = true }
func (r *fakeRows) Next() bool {
	r.index++
	return r.index < len(r.values)
}
func (r *fakeRows) Scan(destinations ...any) error {
	if r.index < 0 || r.index >= len(r.values) {
		return errors.New("scan outside row")
	}
	return assign(destinations, r.values[r.index])
}
func (r *fakeRows) Err() error { return r.err }

func assign(destinations, values []any) error {
	if len(destinations) != len(values) {
		return errors.New("destination count mismatch")
	}
	for index, destination := range destinations {
		target := reflect.ValueOf(destination)
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("destination is not a pointer")
		}
		value := reflect.ValueOf(values[index])
		if !value.IsValid() {
			target.Elem().SetZero()
			continue
		}
		if !value.Type().AssignableTo(target.Elem().Type()) {
			return errors.New("value type mismatch")
		}
		target.Elem().Set(value)
	}
	return nil
}
