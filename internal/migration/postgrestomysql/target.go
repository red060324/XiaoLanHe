package postgrestomysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"

	mysqlmigrations "github.com/red060324/XiaoLanHe/migrations/mysql"
)

type targetDatabase struct{ db *sql.DB }

func (t *targetDatabase) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.db.QueryContext(ctx, query, args...)
}

func (t *targetDatabase) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.db.QueryRowContext(ctx, query, args...)
}

type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type sqlReader interface {
	sqlQueryer
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type targetMigrationIdentity struct {
	Version  string `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksumSha256"`
}

type targetSchemaObject struct {
	Type       string `json:"type"`
	Table      string `json:"table"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

type targetCoordinates struct {
	InstanceID string
	DatabaseID string
}

const targetSchemaSQLPrefix = `SELECT object_type,table_name,object_name,definition FROM (`

func inspectTargetIdentity(ctx context.Context, db *sql.DB) (TargetIdentity, error) {
	return inspectTargetIdentityForTables(ctx, db, targetCoordinates{}, tables())
}

func inspectTargetIdentityForTables(ctx context.Context, reader sqlReader, expectedCoordinates targetCoordinates, specs []tableSpec) (TargetIdentity, error) {
	var identity TargetIdentity
	if reader == nil || len(specs) == 0 {
		return TargetIdentity{}, errors.New("MySQL target identity requires a database reader and mapped tables")
	}
	if err := reader.QueryRowContext(ctx, `SELECT @@server_uuid,DATABASE()`).Scan(&identity.InstanceID, &identity.DatabaseID); err != nil {
		return TargetIdentity{}, fmt.Errorf("read MySQL target identity: %w", err)
	}
	if strings.TrimSpace(identity.InstanceID) == "" || strings.TrimSpace(identity.DatabaseID) == "" {
		return TargetIdentity{}, errors.New("MySQL target instance or database identity is empty")
	}
	if expectedCoordinates.InstanceID != "" || expectedCoordinates.DatabaseID != "" {
		if identity.InstanceID != expectedCoordinates.InstanceID || identity.DatabaseID != expectedCoordinates.DatabaseID {
			return TargetIdentity{}, fmt.Errorf("MySQL target coordinates changed after lock acquisition: got %s/%s, expected %s/%s: %w", identity.InstanceID, identity.DatabaseID, expectedCoordinates.InstanceID, expectedCoordinates.DatabaseID, ErrIdentityMismatch)
		}
	}

	expected, err := expectedTargetMigrations()
	if err != nil {
		return TargetIdentity{}, err
	}
	rows, err := reader.QueryContext(ctx, `SELECT version,name,checksum_sha256,dirty FROM schema_migration ORDER BY version`)
	if err != nil {
		return TargetIdentity{}, fmt.Errorf("read MySQL migration identity: %w", err)
	}
	defer rows.Close()
	migrations := make([]targetMigrationIdentity, 0, len(expected))
	for rows.Next() {
		var item targetMigrationIdentity
		var checksum []byte
		var dirty bool
		if err := rows.Scan(&item.Version, &item.Name, &checksum, &dirty); err != nil {
			return TargetIdentity{}, fmt.Errorf("scan MySQL migration identity: %w", err)
		}
		if dirty {
			return TargetIdentity{}, fmt.Errorf("target migration %s is dirty", item.Version)
		}
		if len(checksum) != sha256Bytes {
			return TargetIdentity{}, fmt.Errorf("target migration %s checksum has %d bytes", item.Version, len(checksum))
		}
		item.Checksum = hex.EncodeToString(checksum)
		migrations = append(migrations, item)
	}
	if err := rows.Err(); err != nil {
		return TargetIdentity{}, err
	}
	if err := validateTargetMigrations(migrations, expected); err != nil {
		return TargetIdentity{}, err
	}
	identity.MigrationVersion = migrations[len(migrations)-1].Version
	digest, err := digestJSON(migrations)
	if err != nil {
		return TargetIdentity{}, err
	}
	identity.MigrationChecksumSHA256 = digest
	identity.SchemaSHA256, err = inspectTargetSchema(ctx, reader, specs)
	if err != nil {
		return TargetIdentity{}, err
	}
	return identity, nil
}

func expectedTargetMigrations() ([]targetMigrationIdentity, error) {
	entries, err := fs.ReadDir(mysqlmigrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded MySQL migration manifest: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("embedded MySQL migration manifest is empty")
	}
	result := make([]targetMigrationIdentity, 0, len(names))
	for _, name := range names {
		contents, readErr := fs.ReadFile(mysqlmigrations.Files, name)
		if readErr != nil {
			return nil, fmt.Errorf("read embedded MySQL migration %s: %w", name, readErr)
		}
		sum := sha256.Sum256(contents)
		result = append(result, targetMigrationIdentity{
			Version: name, Name: strings.TrimSuffix(name, ".sql"), Checksum: hex.EncodeToString(sum[:]),
		})
	}
	return result, nil
}

func validateTargetMigrations(actual, expected []targetMigrationIdentity) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("target MySQL migration history has %d rows, expected exactly %d", len(actual), len(expected))
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return fmt.Errorf("target MySQL migration history differs at position %d: got %s/%s/%s, expected %s/%s/%s",
				i+1, actual[i].Version, actual[i].Name, actual[i].Checksum, expected[i].Version, expected[i].Name, expected[i].Checksum)
		}
	}
	return nil
}

const targetSchemaSQL = targetSchemaSQLPrefix + `
  SELECT 'table' AS object_type,t.table_name,t.table_name AS object_name,
         CAST(JSON_ARRAY(t.engine,t.row_format,t.table_collation,t.create_options) AS CHAR) AS definition
    FROM information_schema.tables t
   WHERE t.table_schema=DATABASE() AND t.table_type='BASE TABLE'
  UNION ALL
  SELECT 'column',c.table_name,CONCAT(LPAD(c.ordinal_position,6,'0'),'.',c.column_name),
         CAST(JSON_ARRAY(c.column_name,c.column_type,c.is_nullable,c.column_default,c.extra,c.generation_expression,c.character_set_name,c.collation_name) AS CHAR)
    FROM information_schema.columns c
   WHERE c.table_schema=DATABASE()
  UNION ALL
  SELECT 'index',s.table_name,CONCAT(s.index_name,'.',LPAD(s.seq_in_index,6,'0')),
         CAST(JSON_ARRAY(s.index_name,s.non_unique,s.seq_in_index,s.column_name,s.expression,s.collation,s.sub_part,s.nullable,s.index_type,s.is_visible) AS CHAR)
    FROM information_schema.statistics s
   WHERE s.table_schema=DATABASE()
  UNION ALL
  SELECT 'constraint',tc.table_name,tc.constraint_name,
         CAST(JSON_ARRAY(tc.constraint_type,tc.enforced) AS CHAR)
    FROM information_schema.table_constraints tc
   WHERE tc.table_schema=DATABASE()
  UNION ALL
  SELECT 'key-column',k.table_name,CONCAT(k.constraint_name,'.',LPAD(k.ordinal_position,6,'0'),'.',k.column_name),
         CAST(JSON_ARRAY(k.constraint_name,k.ordinal_position,k.column_name,k.referenced_table_schema,k.referenced_table_name,k.referenced_column_name,k.position_in_unique_constraint) AS CHAR)
    FROM information_schema.key_column_usage k
   WHERE k.table_schema=DATABASE()
  UNION ALL
  SELECT 'referential',r.table_name,r.constraint_name,
         CAST(JSON_ARRAY(r.unique_constraint_schema,r.unique_constraint_name,r.match_option,r.update_rule,r.delete_rule,r.referenced_table_name) AS CHAR)
    FROM information_schema.referential_constraints r
   WHERE r.constraint_schema=DATABASE()
  UNION ALL
  SELECT 'check',tc.table_name,cc.constraint_name,CAST(JSON_ARRAY(cc.check_clause) AS CHAR)
    FROM information_schema.check_constraints cc
    JOIN information_schema.table_constraints tc
      ON tc.constraint_schema=cc.constraint_schema AND tc.constraint_name=cc.constraint_name
     AND tc.constraint_type='CHECK'
   WHERE tc.table_schema=DATABASE()
) AS target_schema_objects
ORDER BY object_type,table_name,object_name,definition`

func inspectTargetSchema(ctx context.Context, reader sqlReader, specs []tableSpec) (string, error) {
	contract, err := expectedTargetSchemaContract(specs)
	if err != nil {
		return "", err
	}

	rows, err := reader.QueryContext(ctx, targetSchemaSQL)
	if err != nil {
		return "", fmt.Errorf("inspect MySQL target schema: %w", err)
	}
	defer rows.Close()

	objects := make([]targetSchemaObject, 0)
	seenTables := make(map[string]bool, len(specs)+1)
	for rows.Next() {
		var object targetSchemaObject
		if err := rows.Scan(&object.Type, &object.Table, &object.Name, &object.Definition); err != nil {
			return "", fmt.Errorf("scan MySQL target schema: %w", err)
		}
		if _, expected := contract.tables[object.Table]; !expected {
			if object.Type == "table" {
				return "", fmt.Errorf("MySQL target has unexpected base table %s", object.Table)
			}
			continue
		}
		if object.Type == "table" {
			seenTables[object.Table] = true
		}
		canonical, canonicalErr := canonicalizeTargetSchemaObject(object)
		if canonicalErr != nil {
			return "", fmt.Errorf("canonicalize MySQL target schema %s %s.%s: %w", object.Type, object.Table, object.Name, canonicalErr)
		}
		objects = append(objects, canonical)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate MySQL target schema: %w", err)
	}
	if len(objects) == 0 {
		return "", errors.New("MySQL target schema contains no mapped objects")
	}

	for tableName := range contract.tables {
		if !seenTables[tableName] {
			return "", fmt.Errorf("MySQL target table %s is missing or is not a base table", tableName)
		}
	}
	if err := validateTargetSchemaObjects(objects, contract); err != nil {
		return "", err
	}

	sort.Slice(objects, func(i, j int) bool {
		left, _ := json.Marshal(objects[i])
		right, _ := json.Marshal(objects[j])
		return string(left) < string(right)
	})
	h := sha256.New()
	for _, object := range objects {
		encoded, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			return "", marshalErr
		}
		h.Write(encoded)
		h.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

const targetRunLockNamespace = "xlh:pg2mysql:v1:"

type targetRunLock struct {
	conn *sql.Conn
	name string
}

func targetRunLockName(coordinates targetCoordinates) (string, error) {
	if strings.TrimSpace(coordinates.InstanceID) == "" || strings.TrimSpace(coordinates.DatabaseID) == "" {
		return "", errors.New("target lock requires MySQL instance and database identity")
	}
	sum := sha256.Sum256([]byte(coordinates.InstanceID + "\x00" + coordinates.DatabaseID))
	return targetRunLockNamespace + hex.EncodeToString(sum[:24]), nil
}

// acquireTargetRunLock obtains a fail-fast MySQL named lock on one dedicated
// connection. The caller must hold the returned value for the entire run and
// release it with Release using a non-cancelled, bounded context.
func acquireTargetRunLock(ctx context.Context, db *sql.DB) (*targetRunLock, targetCoordinates, error) {
	if db == nil {
		return nil, targetCoordinates{}, errors.New("target lock requires a MySQL database")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, targetCoordinates{}, fmt.Errorf("acquire dedicated MySQL target lock connection: %w", err)
	}
	var coordinates targetCoordinates
	if err := conn.QueryRowContext(ctx, `SELECT @@server_uuid,DATABASE()`).Scan(&coordinates.InstanceID, &coordinates.DatabaseID); err != nil {
		_ = conn.Close()
		return nil, targetCoordinates{}, fmt.Errorf("read MySQL target coordinates before lock: %w", err)
	}
	name, err := targetRunLockName(coordinates)
	if err != nil {
		_ = conn.Close()
		return nil, targetCoordinates{}, err
	}
	var result sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?,0)`, name).Scan(&result); err != nil {
		_ = conn.Close()
		return nil, targetCoordinates{}, fmt.Errorf("acquire MySQL target lock: %w", err)
	}
	if !result.Valid {
		_ = conn.Close()
		return nil, targetCoordinates{}, fmt.Errorf("%w: GET_LOCK returned NULL", ErrTargetLockUnavailable)
	}
	if result.Int64 != 1 {
		_ = conn.Close()
		return nil, targetCoordinates{}, fmt.Errorf("%w: GET_LOCK returned %d", ErrTargetLockUnavailable, result.Int64)
	}
	return &targetRunLock{conn: conn, name: name}, coordinates, nil
}

func (l *targetRunLock) Release(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return nil
	}
	conn := l.conn
	l.conn = nil
	var result sql.NullInt64
	releaseErr := conn.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, l.name).Scan(&result)
	closeErr := conn.Close()
	if releaseErr != nil {
		return errors.Join(fmt.Errorf("release MySQL target lock: %w", releaseErr), closeErr)
	}
	if !result.Valid || result.Int64 != 1 {
		return errors.Join(fmt.Errorf("release MySQL target lock: RELEASE_LOCK returned %v", result), closeErr)
	}
	return closeErr
}

func (l *targetRunLock) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return l.Release(ctx)
}

// targetSnapshot pins every reconciliation read to one MySQL repeatable-read,
// read-only transaction on a dedicated physical connection.
type targetSnapshot struct {
	conn *sql.Conn
	tx   *sql.Tx
}

func beginTargetSnapshot(ctx context.Context, db *sql.DB) (*targetSnapshot, error) {
	if db == nil {
		return nil, errors.New("target snapshot requires a MySQL database")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire dedicated MySQL target snapshot connection: %w", err)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("begin MySQL repeatable-read read-only target snapshot: %w", err)
	}
	return &targetSnapshot{conn: conn, tx: tx}, nil
}

func (s *targetSnapshot) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.tx.QueryContext(ctx, query, args...)
}

func (s *targetSnapshot) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.tx.QueryRowContext(ctx, query, args...)
}

func (s *targetSnapshot) readAll(ctx context.Context, table tableSpec) ([]Row, error) {
	query := "SELECT " + table.targetProjection() + " FROM `" + table.name + "` ORDER BY " + strings.Join(table.keyNames(true), ",")
	return readTargetRows(ctx, s, table, query)
}

func (s *targetSnapshot) inventory(ctx context.Context, table tableSpec, maxRows, maxBytes int64) (TableInventory, error) {
	return targetInventory(ctx, s, table, maxRows, maxBytes)
}

func (s *targetSnapshot) Close() error {
	if s == nil {
		return nil
	}
	var rollbackErr, closeErr error
	if s.tx != nil {
		rollbackErr = s.tx.Rollback()
		s.tx = nil
	}
	if s.conn != nil {
		closeErr = s.conn.Close()
		s.conn = nil
	}
	if errors.Is(rollbackErr, sql.ErrTxDone) {
		rollbackErr = nil
	}
	return errors.Join(rollbackErr, closeErr)
}

func (t *targetDatabase) readAll(ctx context.Context, table tableSpec) ([]Row, error) {
	query := "SELECT " + table.targetProjection() + " FROM `" + table.name + "` ORDER BY " + strings.Join(table.keyNames(true), ",")
	return readTargetRows(ctx, t.db, table, query)
}

func (t *targetDatabase) inventory(ctx context.Context, table tableSpec, maxRows, maxBytes int64) (TableInventory, error) {
	return targetInventory(ctx, t.db, table, maxRows, maxBytes)
}

func targetInventory(ctx context.Context, reader sqlReader, table tableSpec, maxRows, maxBytes int64) (TableInventory, error) {
	var count int64
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table.name+"`").Scan(&count); err != nil {
		return TableInventory{}, fmt.Errorf("count MySQL %s: %w", table.name, err)
	}
	if count > maxRows {
		return TableInventory{Rows: count, DigestSHA256: "uncomputed:row-limit-exceeded"}, nil
	}
	query := "SELECT " + table.targetProjection() + " FROM `" + table.name + "` ORDER BY " + strings.Join(table.keyNames(true), ",")
	rows, err := reader.QueryContext(ctx, query)
	if err != nil {
		return TableInventory{}, err
	}
	defer rows.Close()
	h := sha256.New()
	var observed, canonicalBytes int64
	var last Cursor
	for rows.Next() {
		values := make([]any, len(table.columns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return TableInventory{}, err
		}
		row, err := normalizeRow(table, values)
		if err != nil {
			return TableInventory{}, err
		}
		encoded, _ := json.Marshal(row)
		canonicalBytes += int64(len(encoded) + 1)
		observed++
		if canonicalBytes > maxBytes {
			return TableInventory{Rows: count, CanonicalBytes: canonicalBytes, DigestSHA256: "uncomputed:byte-limit-exceeded"}, nil
		}
		h.Write(encoded)
		h.Write([]byte{'\n'})
		last, err = keyOf(table, row)
		if err != nil {
			return TableInventory{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return TableInventory{}, err
	}
	if observed != count {
		return TableInventory{}, fmt.Errorf("MySQL %s count changed during verification", table.name)
	}
	return TableInventory{Rows: count, CanonicalBytes: canonicalBytes, DigestSHA256: "sha256:" + hex.EncodeToString(h.Sum(nil)), LastKey: last}, nil
}

func readTargetRows(ctx context.Context, queryer sqlQueryer, table tableSpec, query string, args ...any) ([]Row, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read MySQL %s: %w", table.name, err)
	}
	defer rows.Close()
	result := make([]Row, 0)
	for rows.Next() {
		values := make([]any, len(table.columns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		row, err := normalizeRow(table, values)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (t *targetDatabase) writeBatch(ctx context.Context, table tableSpec, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := t.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin MySQL %s batch: %w", table.name, err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range rows {
		existing, found, err := readTargetRow(ctx, tx, table, row)
		if err != nil {
			return err
		}
		if found {
			if canonicalDigest([]Row{existing}) != canonicalDigest([]Row{row}) {
				return fmt.Errorf("%s primary key %v already exists with different data: %w", table.name, mustKey(table, row), ErrCheckpointMismatch)
			}
			continue
		}
		values := make([]any, len(row))
		for i := range row {
			values[i], err = row[i].driverValue()
			if err != nil {
				return fmt.Errorf("decode %s.%s: %w", table.name, table.columns[i].name, err)
			}
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(row)), ",")
		query := "INSERT INTO `" + table.name + "` (" + table.targetProjection() + ") VALUES (" + placeholders + ")"
		if _, err := tx.ExecContext(ctx, query, values...); err != nil {
			return fmt.Errorf("insert MySQL %s primary key %v: %w", table.name, mustKey(table, row), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit MySQL %s batch: %w", table.name, err)
	}
	return nil
}

func readTargetRow(ctx context.Context, queryer sqlQueryer, table tableSpec, expected Row) (Row, bool, error) {
	return readTargetRowWithLock(ctx, queryer, table, expected, true)
}

func readTargetRowReadOnly(ctx context.Context, queryer sqlQueryer, table tableSpec, expected Row) (Row, bool, error) {
	return readTargetRowWithLock(ctx, queryer, table, expected, false)
}

func readTargetRowWithLock(ctx context.Context, queryer sqlQueryer, table tableSpec, expected Row, lock bool) (Row, bool, error) {
	key, err := keyOf(table, expected)
	if err != nil {
		return nil, false, err
	}
	predicates := make([]string, len(table.keyColumns))
	args := make([]any, len(key))
	for i, column := range table.keyColumns {
		predicates[i] = "`" + table.columns[column].name + "`=?"
		args[i], err = expected[column].driverValue()
		if err != nil {
			return nil, false, err
		}
	}
	query := "SELECT " + table.targetProjection() + " FROM `" + table.name + "` WHERE " + strings.Join(predicates, " AND ")
	if lock {
		query += " FOR UPDATE"
	}
	rows, err := readTargetRows(ctx, queryer, table, query, args...)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	if len(rows) != 1 {
		return nil, false, fmt.Errorf("%s primary key %v returned %d rows", table.name, key, len(rows))
	}
	return rows[0], true, nil
}

func (t *targetDatabase) verifyBatch(ctx context.Context, table tableSpec, expected []Row) error {
	for _, row := range expected {
		actual, found, err := readTargetRow(ctx, t.db, table, row)
		if err != nil {
			return err
		}
		if !found || canonicalDigest([]Row{actual}) != canonicalDigest([]Row{row}) {
			return fmt.Errorf("MySQL %s primary key %v does not match checkpoint: %w", table.name, mustKey(table, row), ErrCheckpointMismatch)
		}
	}
	return nil
}

func (t *targetDatabase) verifyResumableRows(ctx context.Context, table tableSpec, initial, final []Row, finalizedPrefix int) error {
	return verifyTargetResumableRows(ctx, t.db, table, initial, final, finalizedPrefix)
}

func verifyTargetResumableRows(ctx context.Context, reader sqlReader, table tableSpec, initial, final []Row, finalizedPrefix int) error {
	if len(initial) != len(final) || finalizedPrefix < 0 || finalizedPrefix > len(final) {
		return ErrCheckpointMismatch
	}
	for i := range final {
		actual, found, err := readTargetRowReadOnly(ctx, reader, table, final[i])
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("MySQL %s primary key %v is missing: %w", table.name, mustKey(table, final[i]), ErrCheckpointMismatch)
		}
		actualDigest := canonicalDigest([]Row{actual})
		finalDigest := canonicalDigest([]Row{final[i]})
		if i < finalizedPrefix {
			if actualDigest != finalDigest {
				return fmt.Errorf("MySQL %s primary key %v deferred state differs: %w", table.name, mustKey(table, final[i]), ErrCheckpointMismatch)
			}
			continue
		}
		initialDigest := canonicalDigest([]Row{initial[i]})
		if actualDigest != initialDigest && actualDigest != finalDigest {
			return fmt.Errorf("MySQL %s primary key %v has neither pending nor committed deferred state: %w", table.name, mustKey(table, final[i]), ErrCheckpointMismatch)
		}
	}
	return nil
}

func (t *targetDatabase) writeDeferredBatch(ctx context.Context, table tableSpec, rows []Row) error {
	deferredColumns := table.deferredColumns()
	if len(deferredColumns) == 0 || len(rows) == 0 {
		return nil
	}
	tx, err := t.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range rows {
		sets := make([]string, len(deferredColumns))
		args := make([]any, 0, len(deferredColumns)+len(table.keyColumns))
		for i, column := range deferredColumns {
			sets[i] = "`" + table.columns[column].name + "`=?"
			value, err := row[column].driverValue()
			if err != nil {
				return err
			}
			args = append(args, value)
		}
		predicates := make([]string, len(table.keyColumns))
		for i, column := range table.keyColumns {
			predicates[i] = "`" + table.columns[column].name + "`=?"
			value, err := row[column].driverValue()
			if err != nil {
				return err
			}
			args = append(args, value)
		}
		query := "UPDATE `" + table.name + "` SET " + strings.Join(sets, ",") + " WHERE " + strings.Join(predicates, " AND ")
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("update deferred links for %s primary key %v: %w", table.name, mustKey(table, row), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit deferred links for %s: %w", table.name, err)
	}
	return nil
}

func (t *targetDatabase) advanceAutoIncrement(ctx context.Context, table tableSpec, sourceRows []Row) error {
	if !table.autoIncrement {
		return nil
	}
	next := int64(1)
	if len(sourceRows) > 0 {
		key, err := keyOf(table, sourceRows[len(sourceRows)-1])
		if err != nil {
			return err
		}
		maximum, err := exactInt64(key[0])
		if err != nil || maximum == math.MaxInt64 {
			return fmt.Errorf("invalid maximum id for %s", table.name)
		}
		next = maximum + 1
	}
	if _, err := t.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE `%s` AUTO_INCREMENT = %d", table.name, next)); err != nil {
		return fmt.Errorf("advance %s AUTO_INCREMENT: %w", table.name, err)
	}
	var observed uint64
	if err := t.db.QueryRowContext(ctx, `SELECT AUTO_INCREMENT FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, table.name).Scan(&observed); err != nil {
		return fmt.Errorf("verify %s AUTO_INCREMENT: %w", table.name, err)
	}
	if observed < uint64(next) {
		return fmt.Errorf("%s AUTO_INCREMENT=%d, want at least %d", table.name, observed, next)
	}
	return nil
}

func mustKey(table tableSpec, row Row) Cursor {
	key, _ := keyOf(table, row)
	return key
}
