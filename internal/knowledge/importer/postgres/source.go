// Package postgres implements the operator-only reader for the legacy
// PostgreSQL knowledge tables. The application runtime must not import it.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/knowledge/importer"
)

const (
	maxPageSize = 1000

	identitySQL = `
select database.oid::text, database.datname,
       pg_catalog.current_setting('server_version_num'),
       coalesce(pg_catalog.inet_server_addr()::text, 'local'),
       coalesce(pg_catalog.inet_server_port(), 0)
from pg_catalog.pg_database as database
where database.datname = pg_catalog.current_database()`

	schemaSQL = `
select requested.logical_name, namespace.nspname, relation.relname, relation.oid::text,
       attribute.attnum, attribute.attname,
       pg_catalog.format_type(attribute.atttypid, attribute.atttypmod),
       attribute.attnotnull,
       coalesce(pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid), '')
from (values
        ('knowledge_document', pg_catalog.to_regclass('knowledge_document')),
        ('knowledge_chunk', pg_catalog.to_regclass('knowledge_chunk'))
     ) as requested(logical_name, relation_oid)
join pg_catalog.pg_class as relation on relation.oid = requested.relation_oid
join pg_catalog.pg_namespace as namespace on namespace.oid = relation.relnamespace
join pg_catalog.pg_attribute as attribute on attribute.attrelid = relation.oid
left join pg_catalog.pg_attrdef as default_value
  on default_value.adrelid = relation.oid and default_value.adnum = attribute.attnum
where attribute.attnum > 0 and not attribute.attisdropped
order by requested.logical_name, attribute.attnum`

	snapshotSQL = `select pg_catalog.pg_export_snapshot()`
)

var (
	ErrClosed      = errors.New("legacy PostgreSQL source is closed")
	ErrInvalidDSN  = errors.New("legacy PostgreSQL source DSN is required")
	ErrInvalidPage = errors.New("invalid legacy PostgreSQL source page")
)

// Source owns one long-lived read-only repeatable-read transaction. All pages
// therefore observe exactly the snapshot described by Descriptor.
type Source struct {
	mu sync.Mutex

	pool       poolHandle
	tx         snapshotTx
	descriptor importer.SourceDescriptor
	documents  string
	chunks     string
	closed     bool
}

// Open connects to the legacy database, starts the stable source snapshot, and
// binds it to a credential-free database identity and an observed schema hash.
func Open(ctx context.Context, dsn string) (*Source, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrInvalidDSN
	}
	pool, err := openPool(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open legacy PostgreSQL pool: %w", err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("begin legacy PostgreSQL snapshot: %w", err)
	}
	source, err := bindSource(ctx, pool, tx)
	if err != nil {
		rollbackErr := tx.Rollback(context.Background())
		pool.Close()
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback legacy PostgreSQL snapshot: %w", rollbackErr))
		}
		return nil, err
	}
	return source, nil
}

func bindSource(ctx context.Context, pool poolHandle, tx snapshotTx) (*Source, error) {
	var identity databaseIdentity
	if err := tx.QueryRow(ctx, identitySQL).Scan(
		&identity.DatabaseOID, &identity.DatabaseName, &identity.ServerVersion,
		&identity.ServerAddress, &identity.ServerPort,
	); err != nil {
		return nil, fmt.Errorf("identify legacy PostgreSQL database: %w", err)
	}
	if identity.DatabaseOID == "" || identity.DatabaseName == "" || identity.ServerVersion == "" || identity.ServerAddress == "" {
		return nil, errors.New("identify legacy PostgreSQL database: incomplete identity")
	}

	schema, tables, err := inspectSchema(ctx, tx)
	if err != nil {
		return nil, err
	}
	var snapshotID string
	if err := tx.QueryRow(ctx, snapshotSQL).Scan(&snapshotID); err != nil {
		return nil, fmt.Errorf("export legacy PostgreSQL snapshot: %w", err)
	}
	if strings.TrimSpace(snapshotID) == "" {
		return nil, errors.New("export legacy PostgreSQL snapshot: empty snapshot ID")
	}

	return &Source{
		pool: pool, tx: tx, documents: tables["knowledge_document"], chunks: tables["knowledge_chunk"],
		descriptor: importer.SourceDescriptor{
			IdentitySHA256: canonicalDigest(identity),
			SnapshotID:     snapshotID,
			SchemaSHA256:   canonicalDigest(schema),
			Isolation:      "repeatable_read",
			ReadOnly:       true,
		},
	}, nil
}

// Descriptor returns the proof for the transaction snapshot owned by Source.
func (s *Source) Descriptor() importer.SourceDescriptor {
	if s == nil {
		return importer.SourceDescriptor{}
	}
	return s.descriptor
}

// ListLegacyKnowledge returns an ID-keyset page from the source snapshot.
func (s *Source) ListLegacyKnowledge(ctx context.Context, afterID int64, limit int) ([]importer.LegacyDocument, error) {
	if s == nil {
		return nil, ErrClosed
	}
	if afterID < 0 || limit < 1 || limit > maxPageSize {
		return nil, ErrInvalidPage
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}

	query := fmt.Sprintf(`
select document.id, document.source_type, document.title,
       coalesce(document.source_url, ''), coalesce(document.game_code, ''),
       coalesce(document.region_code, ''), coalesce(document.patch_version, ''),
       coalesce(document.content_text, ''), document.metadata::text,
       document.published_at, document.created_at, document.updated_at,
       (select count(*)::bigint from %s as chunk where chunk.document_id = document.id)
from %s as document
where document.id > $1
order by document.id asc
limit $2`, s.chunks, s.documents)
	rows, err := s.tx.Query(ctx, query, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list legacy knowledge documents: %w", err)
	}
	defer rows.Close()

	documents := make([]importer.LegacyDocument, 0, limit)
	lastID := afterID
	for rows.Next() {
		var document importer.LegacyDocument
		var metadata string
		if err := rows.Scan(
			&document.ID, &document.Draft.SourceType, &document.Draft.Title,
			&document.Draft.SourceURL, &document.Draft.GameCode, &document.Draft.RegionCode,
			&document.Draft.PatchVersion, &document.Draft.ContentText, &metadata,
			&document.PublishedAt, &document.CreatedAt, &document.UpdatedAt, &document.LegacyChunkCount,
		); err != nil {
			return nil, fmt.Errorf("scan legacy knowledge document: %w", err)
		}
		document.Metadata = json.RawMessage(append([]byte(nil), metadata...))
		if document.ID <= lastID || document.CreatedAt.IsZero() || document.UpdatedAt.IsZero() ||
			document.LegacyChunkCount < 0 || len(document.Metadata) == 0 || !json.Valid(document.Metadata) {
			return nil, fmt.Errorf("legacy knowledge document %d is invalid", document.ID)
		}
		documents = append(documents, document)
		lastID = document.ID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list legacy knowledge documents: %w", err)
	}
	return documents, nil
}

// Close releases the exported snapshot by rolling back its read-only
// transaction, then closes the private pool. Close is idempotent.
func (s *Source) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.tx.Rollback(ctx)
	s.pool.Close()
	if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("rollback legacy PostgreSQL snapshot: %w", err)
	}
	return nil
}

type databaseIdentity struct {
	DatabaseOID   string `json:"databaseOid"`
	DatabaseName  string `json:"databaseName"`
	ServerVersion string `json:"serverVersion"`
	ServerAddress string `json:"serverAddress"`
	ServerPort    int32  `json:"serverPort"`
}

type schemaColumn struct {
	LogicalTable string `json:"logicalTable"`
	Schema       string `json:"schema"`
	Table        string `json:"table"`
	RelationOID  string `json:"relationOid"`
	Position     int16  `json:"position"`
	Name         string `json:"name"`
	DataType     string `json:"dataType"`
	NotNull      bool   `json:"notNull"`
	Default      string `json:"default"`
}

func inspectSchema(ctx context.Context, tx snapshotTx) ([]schemaColumn, map[string]string, error) {
	rows, err := tx.Query(ctx, schemaSQL)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect legacy PostgreSQL schema: %w", err)
	}
	defer rows.Close()

	var columns []schemaColumn
	tables := make(map[string]string, 2)
	present := make(map[string]map[string]bool, 2)
	for rows.Next() {
		var column schemaColumn
		if err := rows.Scan(
			&column.LogicalTable, &column.Schema, &column.Table, &column.RelationOID, &column.Position,
			&column.Name, &column.DataType, &column.NotNull, &column.Default,
		); err != nil {
			return nil, nil, fmt.Errorf("scan legacy PostgreSQL schema: %w", err)
		}
		columns = append(columns, column)
		tables[column.LogicalTable] = pgx.Identifier{column.Schema, column.Table}.Sanitize()
		if present[column.LogicalTable] == nil {
			present[column.LogicalTable] = make(map[string]bool)
		}
		present[column.LogicalTable][column.Name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("inspect legacy PostgreSQL schema: %w", err)
	}

	required := map[string][]string{
		"knowledge_document": {"id", "source_type", "title", "source_url", "game_code", "region_code", "patch_version", "metadata", "content_text", "published_at", "created_at", "updated_at"},
		"knowledge_chunk":    {"id", "document_id"},
	}
	for table, names := range required {
		if tables[table] == "" {
			return nil, nil, fmt.Errorf("inspect legacy PostgreSQL schema: %s is missing", table)
		}
		for _, name := range names {
			if !present[table][name] {
				return nil, nil, fmt.Errorf("inspect legacy PostgreSQL schema: %s.%s is missing", table, name)
			}
		}
	}
	return columns, tables, nil
}

func canonicalDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
}

type row interface {
	Scan(...any) error
}

type rowSet interface {
	Close()
	Next() bool
	Scan(...any) error
	Err() error
}

type snapshotTx interface {
	Query(context.Context, string, ...any) (rowSet, error)
	QueryRow(context.Context, string, ...any) row
	Rollback(context.Context) error
}

type poolHandle interface {
	BeginTx(context.Context, pgx.TxOptions) (snapshotTx, error)
	Close()
}

type realPool struct{ value *pgxpool.Pool }

func (p *realPool) BeginTx(ctx context.Context, options pgx.TxOptions) (snapshotTx, error) {
	tx, err := p.value.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return realTx{Tx: tx}, nil
}

func (p *realPool) Close() { p.value.Close() }

type realTx struct{ pgx.Tx }

func (tx realTx) Query(ctx context.Context, sql string, arguments ...any) (rowSet, error) {
	return tx.Tx.Query(ctx, sql, arguments...)
}

func (tx realTx) QueryRow(ctx context.Context, sql string, arguments ...any) row {
	return tx.Tx.QueryRow(ctx, sql, arguments...)
}

var openPool = func(ctx context.Context, dsn string) (poolHandle, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 1
	config.MinConns = 0
	config.ConnConfig.RuntimeParams["application_name"] = "xiaolanhe-legacy-knowledge-import"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	return &realPool{value: pool}, nil
}

var _ importer.Source = (*Source)(nil)
var _ importer.SourceDescriber = (*Source)(nil)
