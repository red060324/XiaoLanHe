package postgrestomysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sourceSnapshot struct {
	conn     *pgxpool.Conn
	tx       sourceTransaction
	schema   string
	identity SourceIdentity
}

// sourceTransaction deliberately exposes only the read and rollback operations
// used by the cutover. Keeping this boundary small makes the safety-critical
// snapshot reader deterministic to test without changing the production pgx
// transaction semantics.
type sourceTransaction interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Rollback(context.Context) error
}

const sourceIdentitySQL = `
	SELECT current_database(),d.oid::text,current_schema(),current_setting('server_version_num'),
	       COALESCE(inet_server_addr()::text,'local'),COALESCE(inet_server_port()::text,'local'),
	       COALESCE((SELECT string_agg(c.oid::text,',' ORDER BY c.relname)
	                   FROM pg_catalog.pg_class c
	                   JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
	                  WHERE n.nspname=current_schema() AND c.relname=ANY($1::text[])
	                    AND c.relkind IN ('r','p')),''),
	       txid_current_snapshot()::text,pg_export_snapshot(),
	       CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn() ELSE pg_current_wal_lsn() END::text
	FROM pg_database d WHERE d.datname=current_database()`

func beginSourceSnapshot(ctx context.Context, pool *pgxpool.Pool) (*sourceSnapshot, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire PostgreSQL source connection: %w", err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		conn.Release()
		return nil, fmt.Errorf("begin PostgreSQL read-only snapshot: %w", err)
	}
	source := &sourceSnapshot{conn: conn, tx: tx}
	var databaseName, databaseOID, sourceSchema, serverVersion, serverAddress, serverPort, relationOIDs string
	if err := tx.QueryRow(ctx, sourceIdentitySQL, physicalSourceTableNames(tables())).Scan(
		&databaseName, &databaseOID, &sourceSchema, &serverVersion, &serverAddress, &serverPort, &relationOIDs,
		&source.identity.TransactionSnapshot, &source.identity.ExportedSnapshot, &source.identity.WALLSN,
	); err != nil {
		source.Close(ctx)
		return nil, fmt.Errorf("read PostgreSQL snapshot identity: %w", err)
	}
	if strings.TrimSpace(sourceSchema) == "" || strings.TrimSpace(relationOIDs) == "" {
		source.Close(ctx)
		return nil, errors.New("PostgreSQL source schema or expected relations are unavailable")
	}
	source.schema = sourceSchema
	if _, err := tx.Exec(ctx, "SET LOCAL search_path = "+pgx.Identifier{source.schema}.Sanitize()+", pg_catalog"); err != nil {
		source.Close(ctx)
		return nil, fmt.Errorf("pin PostgreSQL source schema: %w", err)
	}
	// The source cluster ID is intentionally not inferred from an endpoint.
	// Execute mode replaces this empty value with operatorClusterId from the
	// externally signed write-freeze attestation. Read-only inspection reports
	// only the stable database identity and schema/data digests.
	_ = serverVersion
	_ = serverAddress
	_ = serverPort
	source.identity.DatabaseID = databaseName + "/oid:" + databaseOID + "/schema:" + source.schema
	schema, err := source.schemaDigest(ctx)
	if err != nil {
		source.Close(ctx)
		return nil, err
	}
	source.identity.SchemaSHA256 = schema
	return source, nil
}

func (s *sourceSnapshot) Close(ctx context.Context) {
	if s == nil {
		return
	}
	if s.tx != nil {
		_ = s.tx.Rollback(ctx)
	}
	if s.conn != nil {
		s.conn.Release()
	}
}

func (s *sourceSnapshot) schemaDigest(ctx context.Context) (string, error) {
	rows, err := s.tx.Query(ctx, `
		SELECT object_type,table_name,object_name,definition FROM (
		  SELECT 'column' AS object_type,cl.relname AS table_name,
		         LPAD(a.attnum::text,6,'0')||'.'||a.attname AS object_name,
		         pg_catalog.format_type(a.atttypid,a.atttypmod)||chr(31)||a.attnotnull::text||chr(31)||
		         COALESCE(pg_catalog.pg_get_expr(ad.adbin,ad.adrelid),'')||chr(31)||
		         COALESCE(coll.collname,'')||chr(31)||a.attidentity::text||chr(31)||a.attgenerated::text AS definition
		    FROM pg_catalog.pg_class cl
		    JOIN pg_catalog.pg_namespace n ON n.oid=cl.relnamespace
		    JOIN pg_catalog.pg_attribute a ON a.attrelid=cl.oid AND a.attnum>0 AND NOT a.attisdropped
		    LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid=cl.oid AND ad.adnum=a.attnum
		    LEFT JOIN pg_catalog.pg_collation coll ON coll.oid=a.attcollation AND a.attcollation<>0
		   WHERE n.nspname=$1 AND cl.relname=ANY($2::text[]) AND cl.relkind IN ('r','p')
		  UNION ALL
		  SELECT 'constraint',cl.relname,con.conname,
		         pg_catalog.pg_get_constraintdef(con.oid,true)||chr(31)||con.convalidated::text
		    FROM pg_catalog.pg_constraint con
		    JOIN pg_catalog.pg_class cl ON cl.oid=con.conrelid
		    JOIN pg_catalog.pg_namespace n ON n.oid=cl.relnamespace
		   WHERE n.nspname=$1 AND cl.relname=ANY($2::text[])
		  UNION ALL
		  SELECT 'index',cl.relname,idx.relname,pg_catalog.pg_get_indexdef(i.indexrelid)
		    FROM pg_catalog.pg_index i
		    JOIN pg_catalog.pg_class cl ON cl.oid=i.indrelid
		    JOIN pg_catalog.pg_class idx ON idx.oid=i.indexrelid
		    JOIN pg_catalog.pg_namespace n ON n.oid=cl.relnamespace
		   WHERE n.nspname=$1 AND cl.relname=ANY($2::text[])
		) objects ORDER BY object_type,table_name,object_name,definition`, s.schema, physicalSourceTableNames(tables()))
	if err != nil {
		return "", fmt.Errorf("inspect PostgreSQL schema: %w", err)
	}
	defer rows.Close()
	h := sha256.New()
	expectedColumns := expectedSourceColumns(tables())
	seenColumns := make(map[string]map[string]bool, len(expectedColumns))
	var count int
	for rows.Next() {
		var objectType, tableName, objectName, definition string
		if err := rows.Scan(&objectType, &tableName, &objectName, &definition); err != nil {
			return "", err
		}
		if objectType == "column" {
			parts := strings.SplitN(objectName, ".", 2)
			if len(parts) == 2 {
				if seenColumns[tableName] == nil {
					seenColumns[tableName] = map[string]bool{}
				}
				seenColumns[tableName][parts[1]] = true
			}
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", objectType, tableName, objectName, definition)
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count == 0 {
		return "", fmt.Errorf("PostgreSQL business schema is empty")
	}
	for tableName, columns := range expectedColumns {
		for columnName := range columns {
			if !seenColumns[tableName][columnName] {
				return "", fmt.Errorf("PostgreSQL source column %s.%s is missing or not visible", tableName, columnName)
			}
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (s *sourceSnapshot) readAll(ctx context.Context, table tableSpec, maxRows, maxBytes int64) ([]Row, int64, error) {
	query := "SELECT " + table.sourceProjection() + " FROM " + s.qualifiedSourceFrom(table) + " ORDER BY " + strings.Join(table.keyNames(false), ",")
	rows, err := s.tx.Query(ctx, query)
	if err != nil {
		return nil, 0, fmt.Errorf("read PostgreSQL %s: %w", table.name, err)
	}
	defer rows.Close()
	result := make([]Row, 0)
	var canonicalBytes int64
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, 0, err
		}
		row, err := normalizeRow(table, values)
		if err != nil {
			return nil, 0, err
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, 0, err
		}
		canonicalBytes += int64(len(encoded) + 1)
		if int64(len(result)+1) > maxRows || canonicalBytes > maxBytes {
			return nil, canonicalBytes, fmt.Errorf("source snapshot exceeds in-memory limit while reading %s: rows>%d or canonicalBytes>%d", table.name, maxRows, maxBytes)
		}
		if len(result) > 0 {
			previous, _ := keyOf(table, result[len(result)-1])
			current, _ := keyOf(table, row)
			comparison, err := compareCursor(table, previous, current)
			if err != nil || comparison >= 0 {
				return nil, 0, fmt.Errorf("%s source primary key is not strictly increasing", table.name)
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate PostgreSQL %s: %w", table.name, err)
	}
	return result, canonicalBytes, nil
}

func physicalSourceTableNames(specs []tableSpec) []string {
	result := make([]string, 0, len(specs))
	seen := map[string]bool{}
	for _, table := range specs {
		name := table.name
		if table.name == "flash_sale_scope_lock" {
			name = "flash_sale_activity"
		}
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func expectedSourceColumns(specs []tableSpec) map[string]map[string]bool {
	result := make(map[string]map[string]bool, len(specs))
	for _, table := range specs {
		name := table.name
		if table.name == "flash_sale_scope_lock" {
			name = "flash_sale_activity"
		}
		if result[name] == nil {
			result[name] = map[string]bool{}
		}
		for _, column := range table.columns {
			for _, dependency := range column.sourceDependencies {
				result[name][dependency] = true
			}
		}
	}
	return result
}

func (s *sourceSnapshot) qualifiedSourceFrom(table tableSpec) string {
	if s.schema == "" {
		return table.sourceFrom
	}
	if table.name == "flash_sale_scope_lock" {
		relation := pgx.Identifier{s.schema, "flash_sale_activity"}.Sanitize()
		return strings.Replace(table.sourceFrom, "FROM flash_sale_activity", "FROM "+relation, 1)
	}
	return pgx.Identifier{s.schema, table.sourceFrom}.Sanitize()
}

func inspectSource(ctx context.Context, source *sourceSnapshot, specs []tableSpec, maxRows, maxBytes int64) (map[string][]Row, map[string]TableInventory, int64, int64, error) {
	allRows := make(map[string][]Row, len(specs))
	inventory := make(map[string]TableInventory, len(specs))
	snapshotInput := make([]string, 0, len(specs))
	var totalRows, totalBytes int64
	for _, table := range specs {
		rows, tableBytes, err := source.readAll(ctx, table, maxRows-totalRows, maxBytes-totalBytes)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		totalRows += int64(len(rows))
		totalBytes += tableBytes
		if totalRows > maxRows || totalBytes > maxBytes {
			return nil, nil, totalRows, totalBytes, fmt.Errorf("source snapshot exceeds in-memory limit: rows=%d/%d canonicalBytes=%d/%d", totalRows, maxRows, totalBytes, maxBytes)
		}
		digest := canonicalDigest(rows)
		item := TableInventory{Rows: int64(len(rows)), CanonicalBytes: tableBytes, DigestSHA256: digest}
		if len(rows) > 0 {
			item.LastKey, err = keyOf(table, rows[len(rows)-1])
			if err != nil {
				return nil, nil, 0, 0, err
			}
		}
		allRows[table.name] = rows
		inventory[table.name] = item
		snapshotInput = append(snapshotInput, table.name+"\x00"+fmt.Sprint(item.Rows)+"\x00"+item.DigestSHA256)
	}
	sort.Strings(snapshotInput)
	sum := sha256.Sum256([]byte(strings.Join(snapshotInput, "\n")))
	source.identity.SnapshotID = "sha256:" + hex.EncodeToString(sum[:])
	return allRows, inventory, totalRows, totalBytes, nil
}
