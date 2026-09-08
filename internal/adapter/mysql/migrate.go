package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

const migrationLockName = "xiaolanhe:mysql:migrate:v1"
const migrationTableDDL = `CREATE TABLE IF NOT EXISTS schema_migration (version VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,name VARCHAR(255) NOT NULL,checksum_sha256 BINARY(32) NOT NULL,dirty TINYINT(1) NOT NULL DEFAULT 1,started_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),completed_at DATETIME(6) NULL,failure_context VARCHAR(1024) NULL,CONSTRAINT ck_schema_migration_dirty CHECK (dirty IN (0,1)),CONSTRAINT ck_schema_migration_completion CHECK ((dirty=1 AND completed_at IS NULL) OR (dirty=0 AND completed_at IS NOT NULL))) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`

type MigrationOptions struct {
	LockTimeout time.Duration
	Metrics     *platformmetrics.Registry
}
type MigrationState struct {
	Version, Name  string
	Checksum       [32]byte
	Dirty          bool
	StartedAt      time.Time
	CompletedAt    sql.NullTime
	FailureContext sql.NullString
}
type Inspection struct {
	Applied []MigrationState
	Pending []string
	Dirty   []MigrationState
}

func Migrate(ctx context.Context, db *sql.DB, files fs.FS, options MigrationOptions) (retErr error) {
	started := time.Now()
	registry := options.Metrics
	if registry == nil {
		registry = platformmetrics.Default()
	}
	defer func() {
		registry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{
			Operation: "migrate", Outcome: mysqlOperationOutcome(retErr), Duration: time.Since(started),
		})
		refreshMigrationMetrics(ctx, db, files, registry)
	}()
	if db == nil || files == nil || options.LockTimeout <= 0 {
		return errors.New("invalid MySQL migration options")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()
	if err = acquireLock(ctx, conn, options.LockTimeout); err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if releaseErr := releaseLock(releaseCtx, conn); releaseErr != nil {
			retErr = errors.Join(retErr, releaseErr)
		}
	}()
	if _, err = conn.ExecContext(ctx, migrationTableDDL); err != nil {
		return fmt.Errorf("bootstrap migration metadata: %w", err)
	}
	entries, err := loadMigrations(files)
	if err != nil {
		return err
	}
	states, err := readStates(ctx, conn)
	if err != nil {
		return err
	}
	known := make(map[string]migrationFile, len(entries))
	for _, entry := range entries {
		known[entry.version] = entry
	}
	for _, state := range states {
		if state.Dirty {
			return fmt.Errorf("migration %s is dirty", state.Version)
		}
		entry, ok := known[state.Version]
		if !ok {
			return fmt.Errorf("unknown applied migration %s", state.Version)
		}
		if state.Name != entry.name || state.Checksum != entry.sum {
			return fmt.Errorf("migration %s metadata changed", state.Version)
		}
	}
	for _, entry := range entries {
		if _, ok := states[entry.version]; ok {
			continue
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO schema_migration(version,name,checksum_sha256,dirty,started_at,completed_at,failure_context) VALUES (?,?,?,1,CURRENT_TIMESTAMP(6),NULL,NULL)`, entry.version, entry.name, entry.sum[:]); err != nil {
			return fmt.Errorf("mark migration %s dirty: %w", entry.version, err)
		}
		if _, err = conn.ExecContext(ctx, entry.sql); err != nil {
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, _ = conn.ExecContext(recordCtx, `UPDATE schema_migration SET failure_context=? WHERE version=?`, migrationFailureContext("apply", err), entry.version)
			cancel()
			return fmt.Errorf("apply migration %s: %w", entry.version, err)
		}
		ok, verifyErr := postcondition(ctx, conn, entry)
		if verifyErr != nil || !ok {
			if verifyErr == nil {
				verifyErr = errors.New("postcondition failed")
			}
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, _ = conn.ExecContext(recordCtx, `UPDATE schema_migration SET failure_context=? WHERE version=?`, migrationFailureContext("verify", verifyErr), entry.version)
			cancel()
			return fmt.Errorf("verify migration %s: %w", entry.version, verifyErr)
		}
		result, cleanErr := conn.ExecContext(ctx, `UPDATE schema_migration SET dirty=0,completed_at=CURRENT_TIMESTAMP(6),failure_context=NULL WHERE version=? AND dirty=1`, entry.version)
		if cleanErr != nil {
			err = cleanErr
			return fmt.Errorf("mark migration %s clean: %w", entry.version, err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("mark migration %s clean affected %d rows", entry.version, changed)
		}
	}
	return nil
}

func migrationFailureContext(stage string, err error) string {
	if stage != "apply" && stage != "verify" {
		stage = "unknown"
	}
	errorClass := "database"
	switch {
	case errors.Is(err, context.Canceled):
		errorClass = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		errorClass = "deadline"
	case stage == "verify":
		errorClass = "postcondition"
	}
	return "stage=" + stage + ",error_class=" + errorClass
}

func Inspect(ctx context.Context, db *sql.DB, files fs.FS) (out Inspection, retErr error) {
	return inspect(ctx, db, files, platformmetrics.Default())
}

func inspect(ctx context.Context, db *sql.DB, files fs.FS, registry *platformmetrics.Registry) (out Inspection, retErr error) {
	if registry == nil {
		registry = platformmetrics.Default()
	}
	started := time.Now()
	defer func() {
		registry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{
			Operation: "inspect_migrations", Outcome: mysqlOperationOutcome(retErr), Duration: time.Since(started),
		})
		if retErr == nil {
			setMigrationMetrics(registry, out)
		}
	}()
	return inspectMigrations(ctx, db, files)
}

func inspectMigrations(ctx context.Context, db *sql.DB, files fs.FS) (Inspection, error) {
	if db == nil || files == nil {
		return Inspection{}, errors.New("invalid migration inspection")
	}
	entries, err := loadMigrations(files)
	if err != nil {
		return Inspection{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT version,name,checksum_sha256,dirty,started_at,completed_at,failure_context FROM schema_migration ORDER BY version`)
	if err != nil {
		return Inspection{}, err
	}
	defer rows.Close()
	out := Inspection{}
	seen := map[string]bool{}
	expected := make(map[string]migrationFile, len(entries))
	for _, entry := range entries {
		expected[entry.version] = entry
	}
	for rows.Next() {
		var s MigrationState
		var sum []byte
		if err := rows.Scan(&s.Version, &s.Name, &sum, &s.Dirty, &s.StartedAt, &s.CompletedAt, &s.FailureContext); err != nil {
			return out, err
		}
		if len(sum) != 32 {
			return out, fmt.Errorf("invalid checksum for %s", s.Version)
		}
		copy(s.Checksum[:], sum)
		out.Applied = append(out.Applied, s)
		entry, ok := expected[s.Version]
		if !ok {
			return out, fmt.Errorf("unknown applied migration %s", s.Version)
		}
		if s.Name != entry.name || s.Checksum != entry.sum {
			return out, fmt.Errorf("migration %s metadata changed", s.Version)
		}
		seen[s.Version] = true
		if s.Dirty {
			out.Dirty = append(out.Dirty, s)
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, e := range entries {
		if !seen[e.version] {
			out.Pending = append(out.Pending, e.version)
		}
	}
	return out, nil
}

func Repair(ctx context.Context, db *sql.DB, files fs.FS, version string) (retErr error) {
	return repair(ctx, db, files, version, platformmetrics.Default())
}

func repair(ctx context.Context, db *sql.DB, files fs.FS, version string, registry *platformmetrics.Registry) (retErr error) {
	if registry == nil {
		registry = platformmetrics.Default()
	}
	started := time.Now()
	defer func() {
		registry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{
			Operation: "repair_migration", Outcome: mysqlOperationOutcome(retErr), Duration: time.Since(started),
		})
		refreshMigrationMetrics(ctx, db, files, registry)
	}()
	if db == nil || files == nil || version == "" {
		return errors.New("invalid migration repair")
	}
	entries, err := loadMigrations(files)
	if err != nil {
		return err
	}
	var target *migrationFile
	for i := range entries {
		if entries[i].version == version {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("unknown migration %s", version)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = acquireLock(ctx, conn, 30*time.Second); err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := releaseLock(releaseCtx, conn); e != nil {
			retErr = errors.Join(retErr, e)
		}
	}()
	var stored []byte
	var dirty bool
	if err = conn.QueryRowContext(ctx, `SELECT checksum_sha256,dirty FROM schema_migration WHERE version=?`, version).Scan(&stored, &dirty); err != nil {
		return err
	}
	if len(stored) != 32 || !equal32(stored, target.sum[:]) {
		return fmt.Errorf("migration %s checksum changed", version)
	}
	if !dirty {
		return nil
	}
	ok, err := postcondition(ctx, conn, *target)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("migration %s postcondition failed", version)
	}
	result, err := conn.ExecContext(ctx, `UPDATE schema_migration SET dirty=0,completed_at=CURRENT_TIMESTAMP(6),failure_context=NULL WHERE version=? AND dirty=1`, version)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("repair migration %s affected %d rows", version, changed)
	}
	return nil
}

func setMigrationMetrics(registry *platformmetrics.Registry, inspection Inspection) {
	registry.SetMySQLMigrations(max(0, len(inspection.Applied)-len(inspection.Dirty)), len(inspection.Pending), len(inspection.Dirty))
}

func refreshMigrationMetrics(ctx context.Context, db *sql.DB, files fs.FS, registry *platformmetrics.Registry) {
	if db == nil || files == nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	inspection, err := inspectMigrations(refreshCtx, db, files)
	if err == nil {
		setMigrationMetrics(registry, inspection)
	}
}

type migrationFile struct {
	version, name, sql string
	sum                [32]byte
}

func loadMigrations(files fs.FS) ([]migrationFile, error) {
	es, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]migrationFile, 0, len(names))
	for _, n := range names {
		b, e := fs.ReadFile(files, n)
		if e != nil {
			return nil, e
		}
		q := strings.TrimSpace(string(b))
		if q == "" || !singleStatement(q) {
			return nil, fmt.Errorf("migration %s must contain exactly one statement", n)
		}
		out = append(out, migrationFile{version: n, name: strings.TrimSuffix(n, ".sql"), sql: q, sum: sha256.Sum256(b)})
	}
	return out, nil
}
func singleStatement(q string) bool {
	q = strings.TrimSpace(q)
	if strings.HasSuffix(q, ";") {
		q = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	}
	return q != "" && !strings.Contains(q, ";")
}
func acquireLock(ctx context.Context, c *sql.Conn, d time.Duration) error {
	var v sql.NullInt64
	if err := c.QueryRowContext(ctx, `SELECT GET_LOCK(?,?)`, migrationLockName, int64((d+time.Second-1)/time.Second)).Scan(&v); err != nil {
		return err
	}
	if !v.Valid {
		return errors.New("migration lock returned NULL")
	}
	if v.Int64 != 1 {
		return errors.New("migration lock timeout")
	}
	return nil
}
func releaseLock(ctx context.Context, c *sql.Conn) error {
	var v sql.NullInt64
	if err := c.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, migrationLockName).Scan(&v); err != nil {
		return err
	}
	if !v.Valid || v.Int64 != 1 {
		return errors.New("migration lock release failed")
	}
	return nil
}
func readStates(ctx context.Context, c *sql.Conn) (map[string]MigrationState, error) {
	rows, err := c.QueryContext(ctx, `SELECT version,name,checksum_sha256,dirty,started_at,completed_at,failure_context FROM schema_migration ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MigrationState{}
	for rows.Next() {
		var s MigrationState
		var b []byte
		if err := rows.Scan(&s.Version, &s.Name, &b, &s.Dirty, &s.StartedAt, &s.CompletedAt, &s.FailureContext); err != nil {
			return nil, err
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("invalid checksum for %s", s.Version)
		}
		copy(s.Checksum[:], b)
		out[s.Version] = s
	}
	return out, rows.Err()
}

const createTableIdentityQuery = `SELECT t.engine,c.character_set_name,t.table_collation FROM information_schema.tables t JOIN information_schema.collations c ON c.collation_name=t.table_collation WHERE t.table_schema=DATABASE() AND t.table_name=? AND t.table_type='BASE TABLE'`
const createTableColumnsQuery = `SELECT column_name,column_type,is_nullable,column_default,extra,generation_expression,character_set_name,collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`
const createTableIndexesQuery = `SELECT index_name,non_unique,seq_in_index,column_name,collation,sub_part,expression,index_type,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY index_name,seq_in_index`
const createTableConstraintsQuery = `SELECT constraint_name,constraint_type FROM information_schema.table_constraints WHERE table_schema=DATABASE() AND table_name=? ORDER BY constraint_name`
const createTableChecksQuery = `SELECT tc.constraint_name,cc.check_clause,tc.enforced FROM information_schema.table_constraints tc JOIN information_schema.check_constraints cc ON cc.constraint_catalog=tc.constraint_catalog AND cc.constraint_schema=tc.constraint_schema AND cc.constraint_name=tc.constraint_name WHERE tc.constraint_schema=DATABASE() AND tc.table_name=? AND tc.constraint_type='CHECK' ORDER BY tc.constraint_name`
const createTableForeignKeysQuery = `SELECT rc.constraint_name,kcu.ordinal_position,kcu.column_name,kcu.position_in_unique_constraint,kcu.referenced_table_schema=DATABASE(),kcu.referenced_table_name,kcu.referenced_column_name,rc.match_option,rc.update_rule,rc.delete_rule FROM information_schema.referential_constraints rc JOIN information_schema.key_column_usage kcu ON kcu.constraint_catalog=rc.constraint_catalog AND kcu.constraint_schema=rc.constraint_schema AND kcu.table_name=rc.table_name AND kcu.constraint_name=rc.constraint_name WHERE rc.constraint_schema=DATABASE() AND rc.table_name=? ORDER BY rc.constraint_name,kcu.ordinal_position`

func postcondition(ctx context.Context, c *sql.Conn, migration migrationFile) (bool, error) {
	if startsWithSQLKeywords(migration.sql, "CREATE", "TABLE") {
		contract, err := parseCreateTableContract(migration.sql)
		if err != nil {
			return false, fmt.Errorf("CREATE TABLE migration cannot be repaired automatically; rebuild the table manually: %w", err)
		}
		return verifyCreateTableContract(ctx, c, contract)
	}
	if startsWithSQLKeywords(migration.sql, "ALTER", "TABLE") {
		contract, err := parseAlterTableContract(migration.sql)
		if err != nil {
			return false, fmt.Errorf("ALTER TABLE migration cannot be repaired automatically; repair manually: %w", err)
		}
		return verifyAlterTableContract(ctx, c, contract)
	}
	return false, errors.New("migration has no repair postcondition")
}

// createTableContract captures every schema semantic used by the embedded
// migration subset. Any syntax outside that subset fails closed instead of
// falling back to table-name existence.
type createTableContract struct {
	table       string
	engine      string
	charset     string
	collation   string
	columns     []createTableColumn
	indexes     []createTableIndex
	constraints []createTableConstraint
}

type createTableColumn struct {
	name                 string
	columnType           string
	nullable             bool
	defaultValue         *string
	defaultExpression    bool
	extra                string
	generationExpression string
	charset              string
	collation            string
}

type createTableIndex struct {
	name    string
	unique  bool
	columns []createTableIndexColumn
}

type createTableIndexColumn struct {
	name       string
	descending bool
}

type createTableConstraint struct {
	name       string
	kind       string
	expression string
	foreignKey *createTableForeignKey
}

type createTableForeignKey struct {
	columns                     []string
	positionsInUniqueConstraint []int
	referencedSchemaIsCurrent   bool
	referencedTable             string
	referencedColumns           []string
	matchOption                 string
	updateRule                  string
	deleteRule                  string
}

func verifyCreateTableContract(ctx context.Context, c *sql.Conn, expected createTableContract) (bool, error) {
	var engine, charset, collation sql.NullString
	err := c.QueryRowContext(ctx, createTableIdentityQuery, expected.table).Scan(&engine, &charset, &collation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect CREATE TABLE identity: %w", err)
	}
	if !engine.Valid || !charset.Valid || !collation.Valid || !strings.EqualFold(engine.String, expected.engine) || !strings.EqualFold(charset.String, expected.charset) || !strings.EqualFold(collation.String, expected.collation) {
		return false, nil
	}

	columns, err := readCreateTableColumns(ctx, c, expected.table)
	if err != nil {
		return false, err
	}
	if !equalCreateTableColumns(expected.columns, columns) {
		return false, nil
	}

	indexes, err := readCreateTableIndexes(ctx, c, expected.table)
	if err != nil {
		return false, err
	}
	if !equalCreateTableIndexes(expected, indexes) {
		return false, nil
	}

	constraints, err := readCreateTableConstraints(ctx, c, expected.table)
	if err != nil {
		return false, err
	}
	if !equalCreateTableConstraints(expected.constraints, constraints) {
		return false, nil
	}

	checks, err := readCreateTableChecks(ctx, c, expected.table)
	if err != nil {
		return false, err
	}
	if !equalCreateTableChecks(expected.constraints, checks) {
		return false, nil
	}

	foreignKeys, err := readCreateTableForeignKeys(ctx, c, expected.table)
	if err != nil {
		return false, err
	}
	return equalCreateTableForeignKeys(expected.constraints, foreignKeys), nil
}

type alterTableContract struct {
	table      string
	column     *createTableColumn
	index      *createTableIndex
	constraint *createTableConstraint
}

func parseAlterTableContract(statement string) (alterTableContract, error) {
	statement = strings.TrimSpace(statement)
	if strings.HasSuffix(statement, ";") {
		statement = strings.TrimSpace(strings.TrimSuffix(statement, ";"))
	}
	p := ddlParser{text: statement}
	if !p.keyword("ALTER") || !p.keyword("TABLE") {
		return alterTableContract{}, errors.New("expected ALTER TABLE")
	}
	table, ok := p.identifier()
	if !ok || !p.keyword("ADD") {
		return alterTableContract{}, errors.New("invalid ALTER TABLE target")
	}
	rest := strings.TrimSpace(p.text[p.pos:])
	if rest == "" {
		return alterTableContract{}, errors.New("ALTER TABLE ADD has no definition")
	}

	parsed := createTableContract{table: table}
	result := alterTableContract{table: table}
	switch {
	case startsWithSQLKeywords(rest, "COLUMN"):
		columnParser := ddlParser{text: rest}
		columnParser.keyword("COLUMN")
		definition := strings.TrimSpace(rest[columnParser.pos:])
		if definition == "" {
			return alterTableContract{}, errors.New("ADD COLUMN has no definition")
		}
		if err := parsed.addColumn(definition); err != nil {
			return alterTableContract{}, err
		}
		if len(parsed.columns) != 1 {
			return alterTableContract{}, errors.New("ADD COLUMN must add exactly one column")
		}
		result.column = &parsed.columns[0]
	case startsWithSQLKeywords(rest, "KEY") || startsWithSQLKeywords(rest, "INDEX"):
		if err := parsed.addNamedIndex(rest); err != nil {
			return alterTableContract{}, err
		}
		if len(parsed.indexes) != 1 {
			return alterTableContract{}, errors.New("ADD KEY must add exactly one index")
		}
		result.index = &parsed.indexes[0]
	case startsWithSQLKeywords(rest, "CONSTRAINT"):
		if err := parsed.addNamedConstraint(rest); err != nil {
			return alterTableContract{}, err
		}
		if len(parsed.constraints) != 1 || parsed.constraints[0].foreignKey == nil {
			return alterTableContract{}, errors.New("only named FOREIGN KEY constraint additions are repairable automatically")
		}
		result.constraint = &parsed.constraints[0]
	default:
		return alterTableContract{}, errors.New("only ADD COLUMN, ADD KEY, and named FOREIGN KEY additions are repairable automatically")
	}
	return result, nil
}

func verifyAlterTableContract(ctx context.Context, c *sql.Conn, expected alterTableContract) (bool, error) {
	switch {
	case expected.column != nil:
		columns, err := readCreateTableColumns(ctx, c, expected.table)
		if err != nil {
			return false, err
		}
		found := false
		for _, column := range columns {
			if !strings.EqualFold(column.name, expected.column.name) {
				continue
			}
			if found || !equalCreateTableColumn(*expected.column, column) {
				return false, nil
			}
			found = true
		}
		return found, nil
	case expected.index != nil:
		indexes, err := readCreateTableIndexes(ctx, c, expected.table)
		if err != nil {
			return false, err
		}
		return containsCreateTableIndexes(indexes, []createTableIndex{*expected.index}), nil
	case expected.constraint != nil:
		constraints, err := readCreateTableConstraints(ctx, c, expected.table)
		if err != nil {
			return false, err
		}
		actual, ok := constraints[strings.ToLower(expected.constraint.name)]
		if !ok || actual.kind != expected.constraint.kind {
			return false, nil
		}
		foreignKeys, err := readCreateTableForeignKeys(ctx, c, expected.table)
		if err != nil {
			return false, err
		}
		foreignKey, ok := foreignKeys[strings.ToLower(expected.constraint.name)]
		return ok && equalCreateTableForeignKey(expected.constraint.foreignKey, foreignKey), nil
	default:
		return false, errors.New("empty ALTER TABLE postcondition")
	}
}

func readCreateTableColumns(ctx context.Context, c *sql.Conn, table string) ([]createTableColumn, error) {
	rows, err := c.QueryContext(ctx, createTableColumnsQuery, table)
	if err != nil {
		return nil, fmt.Errorf("inspect CREATE TABLE columns: %w", err)
	}
	defer rows.Close()

	columns := []createTableColumn{}
	for rows.Next() {
		var name, columnType, nullable, extra, generationExpression string
		var defaultValue, charset, collation sql.NullString
		if err := rows.Scan(&name, &columnType, &nullable, &defaultValue, &extra, &generationExpression, &charset, &collation); err != nil {
			return nil, fmt.Errorf("scan CREATE TABLE columns: %w", err)
		}
		var defaultPtr *string
		defaultExpression := strings.Contains(strings.ToUpper(extra), "DEFAULT_GENERATED")
		if defaultValue.Valid {
			value := defaultValue.String
			if defaultExpression {
				value, err = canonicalSQLExpression(value)
				if err != nil {
					return nil, fmt.Errorf("normalize CREATE TABLE column %s default: %w", name, err)
				}
			}
			defaultPtr = &value
		}
		canonicalGeneration := ""
		if strings.TrimSpace(generationExpression) != "" {
			canonicalGeneration, err = canonicalSQLExpression(generationExpression)
			if err != nil {
				return nil, fmt.Errorf("normalize CREATE TABLE column %s generated expression: %w", name, err)
			}
		}
		columns = append(columns, createTableColumn{
			name:                 name,
			columnType:           normalizeColumnType(columnType),
			nullable:             strings.EqualFold(nullable, "YES"),
			defaultValue:         defaultPtr,
			defaultExpression:    defaultExpression,
			extra:                normalizeColumnExtra(extra),
			generationExpression: canonicalGeneration,
			charset:              nullStringValue(charset),
			collation:            nullStringValue(collation),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate CREATE TABLE columns: %w", err)
	}
	return columns, nil
}

func readCreateTableIndexes(ctx context.Context, c *sql.Conn, table string) (map[string]createTableIndex, error) {
	rows, err := c.QueryContext(ctx, createTableIndexesQuery, table)
	if err != nil {
		return nil, fmt.Errorf("inspect CREATE TABLE indexes: %w", err)
	}
	defer rows.Close()

	indexes := map[string]createTableIndex{}
	for rows.Next() {
		var name, indexType, visible string
		var nonUnique, sequence int
		var column, collation, expression sql.NullString
		var prefixLength sql.NullInt64
		if err := rows.Scan(&name, &nonUnique, &sequence, &column, &collation, &prefixLength, &expression, &indexType, &visible); err != nil {
			return nil, fmt.Errorf("scan CREATE TABLE indexes: %w", err)
		}
		key := strings.ToLower(name)
		index := indexes[key]
		if index.name == "" {
			index.name = name
			index.unique = nonUnique == 0
		}
		if nonUnique != 0 && nonUnique != 1 || !column.Valid || !collation.Valid || !strings.EqualFold(collation.String, "A") && !strings.EqualFold(collation.String, "D") || prefixLength.Valid || expression.Valid || !strings.EqualFold(indexType, "BTREE") || !strings.EqualFold(visible, "YES") || sequence != len(index.columns)+1 || index.unique != (nonUnique == 0) {
			return nil, nil
		}
		index.columns = append(index.columns, createTableIndexColumn{name: column.String, descending: collation.Valid && strings.EqualFold(collation.String, "D")})
		indexes[key] = index
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate CREATE TABLE indexes: %w", err)
	}
	return indexes, nil
}

func readCreateTableConstraints(ctx context.Context, c *sql.Conn, table string) (map[string]createTableConstraint, error) {
	rows, err := c.QueryContext(ctx, createTableConstraintsQuery, table)
	if err != nil {
		return nil, fmt.Errorf("inspect CREATE TABLE constraints: %w", err)
	}
	defer rows.Close()

	constraints := map[string]createTableConstraint{}
	for rows.Next() {
		var constraint createTableConstraint
		if err := rows.Scan(&constraint.name, &constraint.kind); err != nil {
			return nil, fmt.Errorf("scan CREATE TABLE constraints: %w", err)
		}
		key := strings.ToLower(constraint.name)
		if _, duplicate := constraints[key]; duplicate {
			return nil, nil
		}
		constraint.kind = normalizeSQLWords(constraint.kind)
		constraints[key] = constraint
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate CREATE TABLE constraints: %w", err)
	}
	return constraints, nil
}

func readCreateTableChecks(ctx context.Context, c *sql.Conn, table string) (map[string]string, error) {
	rows, err := c.QueryContext(ctx, createTableChecksQuery, table)
	if err != nil {
		return nil, fmt.Errorf("inspect CREATE TABLE checks: %w", err)
	}
	defer rows.Close()
	checks := map[string]string{}
	for rows.Next() {
		var name, expression, enforced string
		if err := rows.Scan(&name, &expression, &enforced); err != nil {
			return nil, fmt.Errorf("scan CREATE TABLE checks: %w", err)
		}
		if !strings.EqualFold(enforced, "YES") {
			return nil, nil
		}
		key := strings.ToLower(name)
		if _, duplicate := checks[key]; duplicate {
			return nil, nil
		}
		canonical, err := canonicalSQLExpression(expression)
		if err != nil {
			return nil, fmt.Errorf("normalize CREATE TABLE check %s: %w", name, err)
		}
		checks[key] = canonical
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate CREATE TABLE checks: %w", err)
	}
	return checks, nil
}

func readCreateTableForeignKeys(ctx context.Context, c *sql.Conn, table string) (map[string]createTableForeignKey, error) {
	rows, err := c.QueryContext(ctx, createTableForeignKeysQuery, table)
	if err != nil {
		return nil, fmt.Errorf("inspect CREATE TABLE foreign keys: %w", err)
	}
	defer rows.Close()
	foreignKeys := map[string]createTableForeignKey{}
	for rows.Next() {
		var name, column, referencedTable, referencedColumn, matchOption, updateRule, deleteRule string
		var referencedSchemaIsCurrent bool
		var position int
		var positionInUnique sql.NullInt64
		if err := rows.Scan(&name, &position, &column, &positionInUnique, &referencedSchemaIsCurrent, &referencedTable, &referencedColumn, &matchOption, &updateRule, &deleteRule); err != nil {
			return nil, fmt.Errorf("scan CREATE TABLE foreign keys: %w", err)
		}
		key := strings.ToLower(name)
		foreignKey := foreignKeys[key]
		if position != len(foreignKey.columns)+1 {
			return nil, nil
		}
		if !positionInUnique.Valid || positionInUnique.Int64 <= 0 || len(foreignKey.columns) > 0 && (foreignKey.referencedSchemaIsCurrent != referencedSchemaIsCurrent || !strings.EqualFold(foreignKey.referencedTable, referencedTable) || foreignKey.matchOption != normalizeSQLWords(matchOption) || foreignKey.updateRule != normalizeSQLWords(updateRule) || foreignKey.deleteRule != normalizeSQLWords(deleteRule)) {
			return nil, nil
		}
		foreignKey.columns = append(foreignKey.columns, column)
		foreignKey.positionsInUniqueConstraint = append(foreignKey.positionsInUniqueConstraint, int(positionInUnique.Int64))
		foreignKey.referencedColumns = append(foreignKey.referencedColumns, referencedColumn)
		foreignKey.referencedSchemaIsCurrent = referencedSchemaIsCurrent
		foreignKey.referencedTable = referencedTable
		foreignKey.matchOption = normalizeSQLWords(matchOption)
		foreignKey.updateRule = normalizeSQLWords(updateRule)
		foreignKey.deleteRule = normalizeSQLWords(deleteRule)
		foreignKeys[key] = foreignKey
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate CREATE TABLE foreign keys: %w", err)
	}
	return foreignKeys, nil
}

func equalCreateTableColumns(expected, actual []createTableColumn) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if !equalCreateTableColumn(expected[i], actual[i]) {
			return false
		}
	}
	return true
}

func equalCreateTableColumn(expected, actual createTableColumn) bool {
	if !strings.EqualFold(expected.name, actual.name) || expected.columnType != actual.columnType || expected.nullable != actual.nullable || expected.defaultExpression != actual.defaultExpression || expected.extra != actual.extra || expected.generationExpression != actual.generationExpression || !strings.EqualFold(expected.charset, actual.charset) || !strings.EqualFold(expected.collation, actual.collation) {
		return false
	}
	if expected.defaultValue == nil || actual.defaultValue == nil {
		return expected.defaultValue == nil && actual.defaultValue == nil
	}
	return *expected.defaultValue == *actual.defaultValue
}

func containsCreateTableIndexes(actual map[string]createTableIndex, expected []createTableIndex) bool {
	if actual == nil {
		return false
	}
	for _, want := range expected {
		got, ok := actual[strings.ToLower(want.name)]
		if !ok || got.unique != want.unique || len(got.columns) != len(want.columns) {
			return false
		}
		for i := range want.columns {
			if !strings.EqualFold(got.columns[i].name, want.columns[i].name) || got.columns[i].descending != want.columns[i].descending {
				return false
			}
		}
	}
	return true
}

func equalCreateTableIndexes(expected createTableContract, actual map[string]createTableIndex) bool {
	wanted := append([]createTableIndex(nil), expected.indexes...)
	for _, constraint := range expected.constraints {
		if constraint.foreignKey == nil || hasSupportingCreateTableIndex(wanted, constraint.foreignKey.columns) {
			continue
		}
		implicit := createTableIndex{name: constraint.name}
		for _, column := range constraint.foreignKey.columns {
			implicit.columns = append(implicit.columns, createTableIndexColumn{name: column})
		}
		wanted = append(wanted, implicit)
	}
	if len(actual) != len(wanted) || !containsCreateTableIndexes(actual, wanted) {
		return false
	}
	return true
}

func hasSupportingCreateTableIndex(indexes []createTableIndex, columns []string) bool {
	for _, index := range indexes {
		if len(index.columns) < len(columns) {
			continue
		}
		matches := true
		for i, column := range columns {
			if !strings.EqualFold(index.columns[i].name, column) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func equalCreateTableConstraints(expected []createTableConstraint, actual map[string]createTableConstraint) bool {
	if len(expected) != len(actual) {
		return false
	}
	for _, want := range expected {
		got, ok := actual[strings.ToLower(want.name)]
		if !ok || normalizeSQLWords(got.kind) != normalizeSQLWords(want.kind) {
			return false
		}
	}
	return true
}

func equalCreateTableChecks(expected []createTableConstraint, actual map[string]string) bool {
	wanted := 0
	for _, constraint := range expected {
		if constraint.kind != "CHECK" {
			continue
		}
		wanted++
		if actual[strings.ToLower(constraint.name)] != constraint.expression {
			return false
		}
	}
	return wanted == len(actual)
}

func equalCreateTableForeignKeys(expected []createTableConstraint, actual map[string]createTableForeignKey) bool {
	wanted := 0
	for _, constraint := range expected {
		if constraint.foreignKey == nil {
			continue
		}
		wanted++
		got, ok := actual[strings.ToLower(constraint.name)]
		if !ok || !equalCreateTableForeignKey(constraint.foreignKey, got) {
			return false
		}
	}
	return wanted == len(actual)
}

func equalCreateTableForeignKey(expected *createTableForeignKey, actual createTableForeignKey) bool {
	return expected != nil &&
		equalIdentifierList(expected.columns, actual.columns) &&
		sequentialPositions(actual.positionsInUniqueConstraint) &&
		actual.referencedSchemaIsCurrent &&
		strings.EqualFold(expected.referencedTable, actual.referencedTable) &&
		equalIdentifierList(expected.referencedColumns, actual.referencedColumns) &&
		expected.matchOption == actual.matchOption &&
		expected.updateRule == actual.updateRule &&
		expected.deleteRule == actual.deleteRule
}

func sequentialPositions(positions []int) bool {
	for i, position := range positions {
		if position != i+1 {
			return false
		}
	}
	return true
}

func equalIdentifierList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func parseCreateTableContract(statement string) (createTableContract, error) {
	statement = strings.TrimSpace(statement)
	if strings.HasSuffix(statement, ";") {
		statement = strings.TrimSpace(strings.TrimSuffix(statement, ";"))
	}
	p := ddlParser{text: statement}
	if !p.keyword("CREATE") || !p.keyword("TABLE") {
		return createTableContract{}, errors.New("expected CREATE TABLE")
	}
	if p.keyword("IF") && (!p.keyword("NOT") || !p.keyword("EXISTS")) {
		return createTableContract{}, errors.New("invalid IF NOT EXISTS clause")
	}
	table, ok := p.identifier()
	if !ok {
		return createTableContract{}, errors.New("missing table name")
	}
	p.skipSpace()
	if p.pos >= len(statement) || statement[p.pos] != '(' {
		return createTableContract{}, errors.New("missing table definition")
	}
	closeAt, err := matchingSQLParen(statement, p.pos)
	if err != nil {
		return createTableContract{}, err
	}
	body := statement[p.pos+1 : closeAt]
	engine, charset, collation, err := parseCreateTableOptions(statement[closeAt+1:])
	if err != nil {
		return createTableContract{}, err
	}

	parts, err := splitTopLevelSQL(body)
	if err != nil {
		return createTableContract{}, err
	}
	contract := createTableContract{table: table, engine: engine, charset: charset, collation: collation}
	for _, part := range parts {
		if err := contract.addElement(part); err != nil {
			return createTableContract{}, err
		}
	}
	if len(contract.columns) == 0 {
		return createTableContract{}, errors.New("CREATE TABLE has no columns")
	}
	return contract, nil
}

func (contract *createTableContract) addElement(element string) error {
	probe := ddlParser{text: element}
	switch {
	case probe.keyword("PRIMARY"):
		return contract.addPrimaryKey(element)
	case startsWithSQLKeywords(element, "KEY") || startsWithSQLKeywords(element, "INDEX"):
		return contract.addNamedIndex(element)
	case startsWithSQLKeywords(element, "CONSTRAINT"):
		return contract.addNamedConstraint(element)
	case startsWithSQLKeywords(element, "UNIQUE") || startsWithSQLKeywords(element, "FOREIGN") || startsWithSQLKeywords(element, "CHECK") || startsWithSQLKeywords(element, "FULLTEXT") || startsWithSQLKeywords(element, "SPATIAL"):
		return errors.New("unnamed or unsupported table constraint")
	default:
		return contract.addColumn(element)
	}
}

func (contract *createTableContract) addColumn(definition string) error {
	p := ddlParser{text: definition}
	name, ok := p.identifier()
	if !ok {
		return errors.New("invalid column definition")
	}
	for _, existing := range contract.columns {
		if strings.EqualFold(existing.name, name) {
			return fmt.Errorf("duplicate column %s", name)
		}
	}
	p.skipSpace()
	typeStart := p.pos
	if _, ok := p.identifier(); !ok {
		return fmt.Errorf("column %s has no type", name)
	}
	p.skipSpace()
	if p.pos < len(definition) && definition[p.pos] == '(' {
		closeAt, err := matchingSQLParen(definition, p.pos)
		if err != nil {
			return fmt.Errorf("column %s type: %w", name, err)
		}
		p.pos = closeAt + 1
	}
	typeEnd := p.pos
	for p.keyword("UNSIGNED") || p.keyword("ZEROFILL") {
		typeEnd = p.pos
	}
	rest := definition[typeEnd:]
	column := createTableColumn{name: name, columnType: normalizeColumnType(definition[typeStart:typeEnd]), nullable: true}
	if isCharacterColumnType(column.columnType) {
		column.charset = contract.charset
		column.collation = contract.collation
	}
	options := ddlParser{text: rest}
	seenNullability := false
	for !options.done() {
		switch {
		case options.keyword("NOT"):
			if seenNullability || !options.keyword("NULL") {
				return fmt.Errorf("unsupported nullability for column %s", name)
			}
			column.nullable = false
			seenNullability = true
		case options.keyword("NULL"):
			if seenNullability {
				return fmt.Errorf("duplicate nullability for column %s", name)
			}
			column.nullable = true
			seenNullability = true
		case options.keyword("AUTO_INCREMENT"):
			column.extra = appendColumnExtra(column.extra, "AUTO_INCREMENT")
		case options.keyword("CHARACTER"):
			if !options.keyword("SET") {
				return fmt.Errorf("invalid CHARACTER SET for column %s", name)
			}
			value, ok := options.identifier()
			if !ok {
				return fmt.Errorf("missing CHARACTER SET for column %s", name)
			}
			column.charset = value
		case options.keyword("COLLATE"):
			value, ok := options.identifier()
			if !ok {
				return fmt.Errorf("missing COLLATE for column %s", name)
			}
			column.collation = value
		case options.keyword("DEFAULT"):
			if column.defaultValue != nil {
				return fmt.Errorf("duplicate DEFAULT for column %s", name)
			}
			value, expression, err := options.defaultClause()
			if err != nil {
				return fmt.Errorf("column %s DEFAULT: %w", name, err)
			}
			if expression {
				value, err = canonicalSQLExpression(value)
				if err != nil {
					return fmt.Errorf("column %s DEFAULT: %w", name, err)
				}
			}
			column.defaultValue = &value
			column.defaultExpression = expression
			if expression {
				column.extra = appendColumnExtra(column.extra, "DEFAULT_GENERATED")
			}
		case options.keyword("GENERATED"):
			if !options.keyword("ALWAYS") || !options.keyword("AS") {
				return fmt.Errorf("unsupported generated column %s", name)
			}
			expression, err := options.expressionValue()
			if err != nil {
				return fmt.Errorf("column %s generated expression: %w", name, err)
			}
			column.generationExpression, err = canonicalSQLExpression(expression)
			if err != nil {
				return fmt.Errorf("column %s generated expression: %w", name, err)
			}
			switch {
			case options.keyword("STORED"):
				column.extra = appendColumnExtra(column.extra, "STORED GENERATED")
			case options.keyword("VIRTUAL"):
				column.extra = appendColumnExtra(column.extra, "VIRTUAL GENERATED")
			default:
				return fmt.Errorf("generated column %s requires STORED or VIRTUAL", name)
			}
		case options.keyword("PRIMARY") || options.keyword("UNIQUE") || options.keyword("REFERENCES") || options.keyword("CHECK"):
			return fmt.Errorf("column %s uses an unsupported inline constraint", name)
		default:
			return fmt.Errorf("column %s has an unsupported option near %q", name, options.remaining())
		}
	}
	contract.columns = append(contract.columns, column)
	return nil
}

func (contract *createTableContract) addPrimaryKey(definition string) error {
	p := ddlParser{text: definition}
	if !p.keyword("PRIMARY") || !p.keyword("KEY") {
		return errors.New("invalid PRIMARY KEY")
	}
	columns, err := p.indexColumns()
	if err != nil || !p.done() {
		return errors.New("unsupported PRIMARY KEY definition")
	}
	if err := contract.appendIndex(createTableIndex{name: "PRIMARY", unique: true, columns: columns}); err != nil {
		return err
	}
	return contract.appendConstraint(createTableConstraint{name: "PRIMARY", kind: "PRIMARY KEY"})
}

func (contract *createTableContract) addNamedIndex(definition string) error {
	p := ddlParser{text: definition}
	if !p.keyword("KEY") && !p.keyword("INDEX") {
		return errors.New("invalid index")
	}
	name, ok := p.identifier()
	if !ok {
		return errors.New("index has no name")
	}
	columns, err := p.indexColumns()
	if err != nil || !p.done() {
		return fmt.Errorf("unsupported index %s definition", name)
	}
	return contract.appendIndex(createTableIndex{name: name, columns: columns})
}

func (contract *createTableContract) addNamedConstraint(definition string) error {
	p := ddlParser{text: definition}
	if !p.keyword("CONSTRAINT") {
		return errors.New("invalid constraint")
	}
	name, ok := p.identifier()
	if !ok {
		return errors.New("constraint has no name")
	}
	switch {
	case p.keyword("UNIQUE"):
		if !p.keyword("KEY") {
			p.keyword("INDEX")
		}
		columns, err := p.indexColumns()
		if err != nil || !p.done() {
			return fmt.Errorf("unsupported UNIQUE constraint %s", name)
		}
		if err := contract.appendIndex(createTableIndex{name: name, unique: true, columns: columns}); err != nil {
			return err
		}
		return contract.appendConstraint(createTableConstraint{name: name, kind: "UNIQUE"})
	case p.keyword("CHECK"):
		expression, err := p.expressionValue()
		if err != nil || !p.done() {
			return fmt.Errorf("unsupported CHECK constraint %s", name)
		}
		canonical, err := canonicalSQLExpression(expression)
		if err != nil {
			return fmt.Errorf("unsupported CHECK constraint %s: %w", name, err)
		}
		return contract.appendConstraint(createTableConstraint{name: name, kind: "CHECK", expression: canonical})
	case p.keyword("FOREIGN"):
		if !p.keyword("KEY") {
			return fmt.Errorf("invalid FOREIGN KEY constraint %s", name)
		}
		columns, err := p.identifierColumns()
		if err != nil || !p.keyword("REFERENCES") {
			return fmt.Errorf("unsupported FOREIGN KEY constraint %s", name)
		}
		referencedTable, ok := p.identifier()
		if !ok {
			return fmt.Errorf("FOREIGN KEY constraint %s has no referenced table", name)
		}
		referencedColumns, err := p.identifierColumns()
		if err != nil || len(columns) != len(referencedColumns) {
			return fmt.Errorf("FOREIGN KEY constraint %s has invalid referenced columns", name)
		}
		foreignKey := &createTableForeignKey{columns: columns, referencedTable: referencedTable, referencedColumns: referencedColumns, matchOption: "NONE", updateRule: "NO ACTION", deleteRule: "NO ACTION"}
		seenUpdate, seenDelete := false, false
		for !p.done() {
			if !p.keyword("ON") {
				return fmt.Errorf("unsupported FOREIGN KEY action for %s", name)
			}
			switch {
			case p.keyword("DELETE") && !seenDelete:
				foreignKey.deleteRule, ok = p.referenceAction()
				seenDelete = true
			case p.keyword("UPDATE") && !seenUpdate:
				foreignKey.updateRule, ok = p.referenceAction()
				seenUpdate = true
			default:
				ok = false
			}
			if !ok {
				return fmt.Errorf("unsupported FOREIGN KEY action for %s", name)
			}
		}
		return contract.appendConstraint(createTableConstraint{name: name, kind: "FOREIGN KEY", foreignKey: foreignKey})
	default:
		return fmt.Errorf("unsupported constraint %s", name)
	}
}

func (contract *createTableContract) appendIndex(index createTableIndex) error {
	for _, existing := range contract.indexes {
		if strings.EqualFold(existing.name, index.name) {
			return fmt.Errorf("duplicate index %s", index.name)
		}
	}
	contract.indexes = append(contract.indexes, index)
	return nil
}

func (contract *createTableContract) appendConstraint(constraint createTableConstraint) error {
	for _, existing := range contract.constraints {
		if strings.EqualFold(existing.name, constraint.name) {
			return fmt.Errorf("duplicate constraint %s", constraint.name)
		}
	}
	contract.constraints = append(contract.constraints, constraint)
	return nil
}

type ddlParser struct {
	text string
	pos  int
}

func (p *ddlParser) skipSpace() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *ddlParser) keyword(keyword string) bool {
	saved := p.pos
	p.skipSpace()
	end := p.pos + len(keyword)
	if end > len(p.text) || !strings.EqualFold(p.text[p.pos:end], keyword) || end < len(p.text) && isSQLIdentifierByte(p.text[end]) {
		p.pos = saved
		return false
	}
	p.pos = end
	return true
}

func (p *ddlParser) identifier() (string, bool) {
	p.skipSpace()
	if p.pos >= len(p.text) {
		return "", false
	}
	if p.text[p.pos] == '`' {
		p.pos++
		var value strings.Builder
		for p.pos < len(p.text) {
			if p.text[p.pos] != '`' {
				value.WriteByte(p.text[p.pos])
				p.pos++
				continue
			}
			if p.pos+1 < len(p.text) && p.text[p.pos+1] == '`' {
				value.WriteByte('`')
				p.pos += 2
				continue
			}
			p.pos++
			return value.String(), value.Len() > 0
		}
		return "", false
	}
	start := p.pos
	for p.pos < len(p.text) && isSQLIdentifierByte(p.text[p.pos]) {
		p.pos++
	}
	return p.text[start:p.pos], p.pos > start
}

func (p *ddlParser) indexColumns() ([]createTableIndexColumn, error) {
	p.skipSpace()
	if p.pos >= len(p.text) || p.text[p.pos] != '(' {
		return nil, errors.New("missing index columns")
	}
	closeAt, err := matchingSQLParen(p.text, p.pos)
	if err != nil {
		return nil, err
	}
	parts, err := splitTopLevelSQL(p.text[p.pos+1 : closeAt])
	if err != nil {
		return nil, err
	}
	columns := make([]createTableIndexColumn, 0, len(parts))
	for _, part := range parts {
		columnParser := ddlParser{text: part}
		name, ok := columnParser.identifier()
		if !ok {
			return nil, errors.New("unsupported expression index")
		}
		columnParser.skipSpace()
		if columnParser.pos < len(part) && part[columnParser.pos] == '(' {
			return nil, fmt.Errorf("index prefix on %s is not repairable automatically", name)
		}
		descending := columnParser.keyword("DESC")
		if !descending {
			columnParser.keyword("ASC")
		}
		if !columnParser.done() {
			return nil, fmt.Errorf("unsupported index column %s", name)
		}
		columns = append(columns, createTableIndexColumn{name: name, descending: descending})
	}
	p.pos = closeAt + 1
	return columns, nil
}

func (p *ddlParser) identifierColumns() ([]string, error) {
	indexed, err := p.indexColumns()
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(indexed))
	for i, column := range indexed {
		if column.descending {
			return nil, errors.New("descending foreign-key column")
		}
		columns[i] = column.name
	}
	return columns, nil
}

func (p *ddlParser) expressionValue() (string, error) {
	p.skipSpace()
	if p.pos >= len(p.text) || p.text[p.pos] != '(' {
		return "", errors.New("missing parenthesized expression")
	}
	closeAt, err := matchingSQLParen(p.text, p.pos)
	if err != nil {
		return "", err
	}
	expression := strings.TrimSpace(p.text[p.pos+1 : closeAt])
	if expression == "" {
		return "", errors.New("empty parenthesized expression")
	}
	p.pos = closeAt + 1
	return expression, nil
}

func (p *ddlParser) defaultClause() (string, bool, error) {
	p.skipSpace()
	if p.pos >= len(p.text) {
		return "", false, errors.New("missing value")
	}
	if p.text[p.pos] == '(' {
		expression, err := p.expressionValue()
		return expression, true, err
	}
	if p.text[p.pos] == '\'' || p.text[p.pos] == '"' {
		value, err := p.stringLiteral()
		return value, false, err
	}
	start := p.pos
	for p.pos < len(p.text) && !strings.ContainsRune(" \t\r\n", rune(p.text[p.pos])) {
		p.pos++
	}
	value := p.text[start:p.pos]
	if value == "" || strings.EqualFold(value, "NULL") {
		return "", false, errors.New("NULL or empty defaults are not repairable automatically")
	}
	if strings.HasPrefix(strings.ToUpper(value), "CURRENT_TIMESTAMP") {
		return value, true, nil
	}
	return value, false, nil
}

func (p *ddlParser) stringLiteral() (string, error) {
	p.skipSpace()
	if p.pos >= len(p.text) || p.text[p.pos] != '\'' && p.text[p.pos] != '"' {
		return "", errors.New("missing string literal")
	}
	quote := p.text[p.pos]
	p.pos++
	var value strings.Builder
	for p.pos < len(p.text) {
		ch := p.text[p.pos]
		if ch == '\\' && p.pos+1 < len(p.text) {
			value.WriteByte(p.text[p.pos+1])
			p.pos += 2
			continue
		}
		if ch == quote {
			if p.pos+1 < len(p.text) && p.text[p.pos+1] == quote {
				value.WriteByte(quote)
				p.pos += 2
				continue
			}
			p.pos++
			return value.String(), nil
		}
		value.WriteByte(ch)
		p.pos++
	}
	return "", errors.New("unterminated string literal")
}

func (p *ddlParser) referenceAction() (string, bool) {
	if p.keyword("RESTRICT") {
		return "RESTRICT", true
	}
	if p.keyword("CASCADE") {
		return "CASCADE", true
	}
	if p.keyword("SET") {
		if p.keyword("NULL") {
			return "SET NULL", true
		}
		return "", false
	}
	if p.keyword("NO") {
		if p.keyword("ACTION") {
			return "NO ACTION", true
		}
		return "", false
	}
	return "", false
}

func (p *ddlParser) done() bool {
	p.skipSpace()
	return p.pos == len(p.text)
}

func (p *ddlParser) remaining() string {
	p.skipSpace()
	return p.text[p.pos:]
}

func startsWithSQLKeywords(statement string, keywords ...string) bool {
	p := ddlParser{text: strings.TrimSpace(statement)}
	for _, keyword := range keywords {
		if !p.keyword(keyword) {
			return false
		}
	}
	return true
}

func matchingSQLParen(statement string, openAt int) (int, error) {
	if openAt >= len(statement) || statement[openAt] != '(' {
		return 0, errors.New("missing opening parenthesis")
	}
	depth := 0
	var quote byte
	for i := openAt; i < len(statement); i++ {
		ch := statement[i]
		if quote != 0 {
			if ch == '\\' && quote != '`' && i+1 < len(statement) {
				i++
				continue
			}
			if ch == quote {
				if i+1 < len(statement) && statement[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			quote = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
			if depth < 0 {
				return 0, errors.New("unbalanced parentheses")
			}
		}
	}
	return 0, errors.New("unbalanced parentheses or quote")
}

func splitTopLevelSQL(list string) ([]string, error) {
	parts := []string{}
	start, depth := 0, 0
	var quote byte
	for i := 0; i < len(list); i++ {
		ch := list[i]
		if quote != 0 {
			if ch == '\\' && quote != '`' && i+1 < len(list) {
				i++
				continue
			}
			if ch == quote {
				if i+1 < len(list) && list[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			quote = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced parentheses")
			}
		case ',':
			if depth == 0 {
				part := strings.TrimSpace(list[start:i])
				if part == "" {
					return nil, errors.New("empty table element")
				}
				parts = append(parts, part)
				start = i + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, errors.New("unbalanced parentheses or quote")
	}
	last := strings.TrimSpace(list[start:])
	if last == "" {
		return nil, errors.New("empty table element")
	}
	return append(parts, last), nil
}

func parseCreateTableOptions(options string) (string, string, string, error) {
	p := ddlParser{text: strings.TrimSpace(options)}
	values := map[string]string{}
	for !p.done() {
		name := ""
		switch {
		case p.keyword("ENGINE"):
			name = "ENGINE"
		case p.keyword("DEFAULT"):
			switch {
			case p.keyword("CHARACTER") && p.keyword("SET"):
				name = "CHARSET"
			case p.keyword("CHARSET"):
				name = "CHARSET"
			default:
				return "", "", "", errors.New("unsupported CREATE TABLE DEFAULT option")
			}
		case p.keyword("CHARACTER"):
			if !p.keyword("SET") {
				return "", "", "", errors.New("invalid CREATE TABLE CHARACTER SET option")
			}
			name = "CHARSET"
		case p.keyword("CHARSET"):
			name = "CHARSET"
		case p.keyword("COLLATE"):
			name = "COLLATE"
		default:
			return "", "", "", fmt.Errorf("unsupported CREATE TABLE option near %q", p.remaining())
		}
		p.skipSpace()
		if p.pos < len(p.text) && p.text[p.pos] == '=' {
			p.pos++
		}
		value, ok := p.identifier()
		if !ok {
			return "", "", "", fmt.Errorf("CREATE TABLE option %s has no value", name)
		}
		if _, duplicate := values[name]; duplicate {
			return "", "", "", fmt.Errorf("duplicate CREATE TABLE option %s", name)
		}
		values[name] = value
	}
	if values["ENGINE"] == "" || values["CHARSET"] == "" || values["COLLATE"] == "" {
		return "", "", "", errors.New("automatic repair requires explicit ENGINE, CHARACTER SET, and COLLATE")
	}
	return values["ENGINE"], values["CHARSET"], values["COLLATE"], nil
}

func appendColumnExtra(extra, flag string) string {
	if extra == "" {
		return flag
	}
	return extra + " " + flag
}

func normalizeColumnExtra(extra string) string {
	upper := normalizeSQLWords(extra)
	flags := []string{}
	for _, flag := range []string{"AUTO_INCREMENT", "DEFAULT_GENERATED", "STORED GENERATED", "VIRTUAL GENERATED"} {
		if strings.Contains(upper, flag) {
			flags = append(flags, flag)
			upper = strings.ReplaceAll(upper, flag, "")
		}
	}
	if strings.TrimSpace(upper) != "" {
		return "UNSUPPORTED:" + strings.TrimSpace(upper)
	}
	sort.Strings(flags)
	return strings.Join(flags, " ")
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func isCharacterColumnType(columnType string) bool {
	for _, prefix := range []string{"char(", "varchar(", "text", "tinytext", "mediumtext", "longtext", "enum(", "set("} {
		if strings.HasPrefix(columnType, prefix) {
			return true
		}
	}
	return false
}

func canonicalSQLExpression(expression string) (string, error) {
	parser := sqlExpressionParser{text: expression}
	node, err := parser.parse()
	if err != nil {
		return "", err
	}
	return node.canonical(), nil
}

type sqlExpressionNode struct {
	kind, value string
	children    []*sqlExpressionNode
}

func (node *sqlExpressionNode) canonical() string {
	if node == nil {
		return ""
	}
	children := node.children
	if node.kind == "op" && (node.value == "and" || node.value == "or") {
		flattened := make([]*sqlExpressionNode, 0, len(children))
		var appendChild func(*sqlExpressionNode)
		appendChild = func(child *sqlExpressionNode) {
			if child.kind == node.kind && child.value == node.value {
				for _, grandchild := range child.children {
					appendChild(grandchild)
				}
				return
			}
			flattened = append(flattened, child)
		}
		for _, child := range children {
			appendChild(child)
		}
		children = flattened
	}
	var out strings.Builder
	out.WriteString(node.kind)
	out.WriteByte(':')
	out.WriteString(strconv.Quote(node.value))
	out.WriteByte('(')
	for _, child := range children {
		value := child.canonical()
		out.WriteString(strconv.Itoa(len(value)))
		out.WriteByte(':')
		out.WriteString(value)
	}
	out.WriteByte(')')
	return out.String()
}

type sqlExpressionToken struct {
	kind  byte
	value string
}

type sqlExpressionParser struct {
	text  string
	pos   int
	token sqlExpressionToken
	err   error
}

func (parser *sqlExpressionParser) parse() (*sqlExpressionNode, error) {
	parser.next()
	node := parser.parseOr()
	if parser.err != nil {
		return nil, parser.err
	}
	if node == nil || parser.token.kind != 0 {
		return nil, fmt.Errorf("unsupported SQL expression near %q", parser.remaining())
	}
	return node, nil
}

func (parser *sqlExpressionParser) parseOr() *sqlExpressionNode {
	left := parser.parseAnd()
	for parser.consumeKeyword("OR") || parser.consumeOperator("||") {
		left = sqlBinaryExpression("or", left, parser.parseAnd())
	}
	return left
}

func (parser *sqlExpressionParser) parseAnd() *sqlExpressionNode {
	left := parser.parseNot()
	for parser.consumeKeyword("AND") || parser.consumeOperator("&&") {
		left = sqlBinaryExpression("and", left, parser.parseNot())
	}
	return left
}

func (parser *sqlExpressionParser) parseNot() *sqlExpressionNode {
	if parser.consumeKeyword("NOT") || parser.consumeOperator("!") {
		return &sqlExpressionNode{kind: "op", value: "not", children: []*sqlExpressionNode{parser.parseNot()}}
	}
	return parser.parseComparison()
}

func (parser *sqlExpressionParser) parseComparison() *sqlExpressionNode {
	left := parser.parseAdditive()
	for parser.err == nil {
		if parser.consumeKeyword("IS") {
			operator := "is"
			if parser.consumeKeyword("NOT") {
				operator = "is not"
			}
			right := parser.parseComparisonLiteral()
			left = sqlBinaryExpression(operator, left, right)
			continue
		}
		not := parser.consumeKeyword("NOT")
		if parser.consumeKeyword("IN") {
			children := []*sqlExpressionNode{left}
			if !parser.consumePunctuation("(") {
				parser.fail("IN requires a parenthesized list")
				return left
			}
			for {
				children = append(children, parser.parseOr())
				if !parser.consumePunctuation(",") {
					break
				}
			}
			if len(children) == 1 || !parser.consumePunctuation(")") {
				parser.fail("invalid IN list")
				return left
			}
			operator := "in"
			if not {
				operator = "not in"
			}
			left = &sqlExpressionNode{kind: "op", value: operator, children: children}
			continue
		}
		if parser.consumeKeyword("BETWEEN") {
			lower := parser.parseAdditive()
			if !parser.consumeKeyword("AND") {
				parser.fail("BETWEEN requires AND")
				return left
			}
			upper := parser.parseAdditive()
			operator := "between"
			if not {
				operator = "not between"
			}
			left = &sqlExpressionNode{kind: "op", value: operator, children: []*sqlExpressionNode{left, lower, upper}}
			continue
		}
		if not {
			parser.fail("unsupported NOT expression")
			return left
		}
		if parser.token.kind != 'o' || !isSQLComparisonOperator(parser.token.value) {
			return left
		}
		operator := parser.token.value
		parser.next()
		left = sqlBinaryExpression(operator, left, parser.parseAdditive())
	}
	return left
}

func (parser *sqlExpressionParser) parseComparisonLiteral() *sqlExpressionNode {
	if parser.token.kind == 'i' {
		switch strings.ToUpper(parser.token.value) {
		case "NULL", "TRUE", "FALSE", "UNKNOWN":
			value := strings.ToLower(parser.token.value)
			parser.next()
			return &sqlExpressionNode{kind: "literal", value: value}
		}
	}
	parser.fail("IS requires NULL, TRUE, FALSE, or UNKNOWN")
	return nil
}

func (parser *sqlExpressionParser) parseAdditive() *sqlExpressionNode {
	left := parser.parseMultiplicative()
	for parser.token.kind == 'o' && (parser.token.value == "+" || parser.token.value == "-") {
		operator := parser.token.value
		parser.next()
		left = sqlBinaryExpression(operator, left, parser.parseMultiplicative())
	}
	return left
}

func (parser *sqlExpressionParser) parseMultiplicative() *sqlExpressionNode {
	left := parser.parseUnary()
	for parser.token.kind == 'o' && (parser.token.value == "*" || parser.token.value == "/" || parser.token.value == "%") {
		operator := parser.token.value
		parser.next()
		left = sqlBinaryExpression(operator, left, parser.parseUnary())
	}
	return left
}

func (parser *sqlExpressionParser) parseUnary() *sqlExpressionNode {
	if parser.token.kind == 'o' && (parser.token.value == "+" || parser.token.value == "-" || parser.token.value == "~") {
		operator := parser.token.value
		parser.next()
		return &sqlExpressionNode{kind: "op", value: "unary " + operator, children: []*sqlExpressionNode{parser.parseUnary()}}
	}
	return parser.parsePrimary()
}

func (parser *sqlExpressionParser) parsePrimary() *sqlExpressionNode {
	if parser.err != nil {
		return nil
	}
	if parser.consumePunctuation("(") {
		node := parser.parseOr()
		if !parser.consumePunctuation(")") {
			parser.fail("missing closing parenthesis")
		}
		return node
	}
	token := parser.token
	switch token.kind {
	case 's':
		parser.next()
		return &sqlExpressionNode{kind: "string", value: token.value}
	case 'n':
		parser.next()
		return &sqlExpressionNode{kind: "number", value: strings.ToLower(token.value)}
	case 'i':
		parser.next()
		name := strings.ToLower(token.value)
		if name == "null" || name == "true" || name == "false" || name == "unknown" {
			return &sqlExpressionNode{kind: "literal", value: name}
		}
		if !parser.consumePunctuation("(") {
			return &sqlExpressionNode{kind: "identifier", value: name}
		}
		function := &sqlExpressionNode{kind: "function", value: name}
		if parser.consumePunctuation(")") {
			return function
		}
		for {
			function.children = append(function.children, parser.parseOr())
			if !parser.consumePunctuation(",") {
				break
			}
		}
		if !parser.consumePunctuation(")") {
			parser.fail("invalid function arguments")
		}
		return function
	default:
		parser.fail("missing SQL expression operand")
		return nil
	}
}

func sqlBinaryExpression(operator string, left, right *sqlExpressionNode) *sqlExpressionNode {
	return &sqlExpressionNode{kind: "op", value: operator, children: []*sqlExpressionNode{left, right}}
}

func isSQLComparisonOperator(operator string) bool {
	switch operator {
	case "=", "!=", "<>", "<", "<=", ">", ">=", "<=>":
		return true
	default:
		return false
	}
}

func (parser *sqlExpressionParser) consumeKeyword(keyword string) bool {
	if parser.token.kind != 'i' || !strings.EqualFold(parser.token.value, keyword) {
		return false
	}
	parser.next()
	return true
}

func (parser *sqlExpressionParser) consumeOperator(operator string) bool {
	if parser.token.kind != 'o' || parser.token.value != operator {
		return false
	}
	parser.next()
	return true
}

func (parser *sqlExpressionParser) consumePunctuation(value string) bool {
	if parser.token.kind != 'p' || parser.token.value != value {
		return false
	}
	parser.next()
	return true
}

func (parser *sqlExpressionParser) fail(message string) {
	if parser.err == nil {
		parser.err = fmt.Errorf("%s near %q", message, parser.remaining())
	}
}

func (parser *sqlExpressionParser) remaining() string {
	start := parser.pos - len(parser.token.value)
	if start < 0 || start > len(parser.text) {
		start = parser.pos
	}
	return strings.TrimSpace(parser.text[start:])
}

func (parser *sqlExpressionParser) next() {
	if parser.err != nil {
		parser.token = sqlExpressionToken{}
		return
	}
	for parser.pos < len(parser.text) && strings.ContainsRune(" \t\r\n", rune(parser.text[parser.pos])) {
		parser.pos++
	}
	if parser.pos >= len(parser.text) {
		parser.token = sqlExpressionToken{}
		return
	}
	ch := parser.text[parser.pos]
	if ch == '`' {
		value, ok := parser.quotedIdentifier()
		if !ok {
			parser.fail("unterminated quoted identifier")
			return
		}
		parser.token = sqlExpressionToken{kind: 'i', value: value}
		return
	}
	if ch == '\'' || ch == '"' {
		value, ok := parser.quotedString(ch)
		if !ok {
			parser.fail("unterminated string literal")
			return
		}
		parser.token = sqlExpressionToken{kind: 's', value: value}
		return
	}
	if ch >= '0' && ch <= '9' {
		start := parser.pos
		for parser.pos < len(parser.text) && ((parser.text[parser.pos] >= '0' && parser.text[parser.pos] <= '9') || parser.text[parser.pos] == '.') {
			parser.pos++
		}
		parser.token = sqlExpressionToken{kind: 'n', value: parser.text[start:parser.pos]}
		return
	}
	if isSQLIdentifierByte(ch) {
		start := parser.pos
		for parser.pos < len(parser.text) && isSQLIdentifierByte(parser.text[parser.pos]) {
			parser.pos++
		}
		value := parser.text[start:parser.pos]
		if strings.EqualFold(value, "_utf8mb4") && parser.pos < len(parser.text) && (parser.text[parser.pos] == '\'' || parser.text[parser.pos] == '"') {
			quote := parser.text[parser.pos]
			literal, ok := parser.quotedString(quote)
			if !ok {
				parser.fail("unterminated introduced string literal")
				return
			}
			parser.token = sqlExpressionToken{kind: 's', value: literal}
			return
		}
		if strings.EqualFold(value, "_utf8mb4") && parser.pos+1 < len(parser.text) && parser.text[parser.pos] == '\\' && parser.text[parser.pos+1] == '\'' {
			literal, ok := parser.escapedIntroducedString()
			if !ok {
				parser.fail("invalid or unterminated escaped introduced string literal")
				return
			}
			parser.token = sqlExpressionToken{kind: 's', value: literal}
			return
		}
		parser.token = sqlExpressionToken{kind: 'i', value: value}
		return
	}
	for _, operator := range []string{"<=>", "<=", ">=", "<>", "!=", "||", "&&"} {
		if strings.HasPrefix(parser.text[parser.pos:], operator) {
			parser.pos += len(operator)
			parser.token = sqlExpressionToken{kind: 'o', value: operator}
			return
		}
	}
	if strings.ContainsRune("=<>+-*/%!~", rune(ch)) {
		parser.pos++
		parser.token = sqlExpressionToken{kind: 'o', value: string(ch)}
		return
	}
	if strings.ContainsRune("(),", rune(ch)) {
		parser.pos++
		parser.token = sqlExpressionToken{kind: 'p', value: string(ch)}
		return
	}
	parser.fail("unsupported SQL expression token")
}

func (parser *sqlExpressionParser) quotedIdentifier() (string, bool) {
	parser.pos++
	var value strings.Builder
	for parser.pos < len(parser.text) {
		if parser.text[parser.pos] != '`' {
			value.WriteByte(parser.text[parser.pos])
			parser.pos++
			continue
		}
		if parser.pos+1 < len(parser.text) && parser.text[parser.pos+1] == '`' {
			value.WriteByte('`')
			parser.pos += 2
			continue
		}
		parser.pos++
		return value.String(), true
	}
	return "", false
}

func (parser *sqlExpressionParser) quotedString(quote byte) (string, bool) {
	parser.pos++
	var value strings.Builder
	for parser.pos < len(parser.text) {
		ch := parser.text[parser.pos]
		if ch == '\\' {
			if parser.pos+1 >= len(parser.text) {
				return "", false
			}
			escaped := parser.text[parser.pos+1]
			switch escaped {
			case '0':
				value.WriteByte(0)
			case 'b':
				value.WriteByte('\b')
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 't':
				value.WriteByte('\t')
			case 'Z':
				value.WriteByte(0x1a)
			case '%', '_':
				// MySQL preserves these two escapes for pattern matching.
				value.WriteByte('\\')
				value.WriteByte(escaped)
			default:
				// MySQL ignores the slash for every other escape.
				value.WriteByte(escaped)
			}
			parser.pos += 2
			continue
		}
		if ch != quote {
			value.WriteByte(ch)
			parser.pos++
			continue
		}
		if parser.pos+1 < len(parser.text) && parser.text[parser.pos+1] == quote {
			value.WriteByte(quote)
			parser.pos += 2
			continue
		}
		parser.pos++
		return value.String(), true
	}
	return "", false
}

// escapedIntroducedString decodes the two String::print layers used by MySQL
// 8.4 for INFORMATION_SCHEMA.CHECK_CONSTRAINTS: the inner Item_string::print
// escapes the literal value, then check-clause conversion escapes that printed
// expression again. It is intentionally only called after the exact _utf8mb4
// introducer and opening \' are recognized.
func (parser *sqlExpressionParser) escapedIntroducedString() (string, bool) {
	parser.pos += 2
	var value strings.Builder
	for parser.pos < len(parser.text) {
		ch := parser.text[parser.pos]
		if ch == '\'' {
			return "", false
		}
		if ch != '\\' {
			// The outer String::print always escapes these bytes. Their raw
			// presence cannot be output by the MySQL 8.4 metadata path.
			if ch == 0 || ch == '\n' || ch == '\r' || ch == 0x1a {
				return "", false
			}
			value.WriteByte(ch)
			parser.pos++
			continue
		}

		start := parser.pos
		for parser.pos < len(parser.text) && parser.text[parser.pos] == '\\' {
			parser.pos++
		}
		slashes := parser.pos - start
		if parser.pos < len(parser.text) && parser.text[parser.pos] == '\'' {
			for range slashes / 4 {
				value.WriteByte('\\')
			}
			switch slashes % 4 {
			case 1:
				parser.pos++
				return value.String(), true
			case 3:
				value.WriteByte('\'')
				parser.pos++
				continue
			default:
				return "", false
			}
		}

		for range slashes / 4 {
			value.WriteByte('\\')
		}
		switch slashes % 4 {
		case 0:
			continue
		case 2:
			if parser.pos >= len(parser.text) {
				return "", false
			}
			switch parser.text[parser.pos] {
			case '0':
				value.WriteByte(0)
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 'Z':
				value.WriteByte(0x1a)
			default:
				return "", false
			}
			parser.pos++
		default:
			return "", false
		}
	}
	return "", false
}

func stripOuterSQLParens(expression string) string {
	for len(expression) >= 2 && expression[0] == '(' {
		closeAt, err := matchingSQLParen(expression, 0)
		if err != nil || closeAt != len(expression)-1 {
			break
		}
		expression = strings.TrimSpace(expression[1:closeAt])
	}
	return expression
}

func normalizeColumnType(columnType string) string {
	return strings.ToLower(strings.Join(strings.Fields(columnType), ""))
}

func normalizeSQLWords(value string) string {
	return strings.ToUpper(strings.Join(strings.Fields(value), " "))
}

func isSQLIdentifierByte(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '$'
}

func equal32(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
func bounded(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
