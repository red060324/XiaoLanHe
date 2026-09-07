package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
	migrations "github.com/red060324/XiaoLanHe/migrations/mysql"
)

const widgetMigrationSQL = `CREATE TABLE widget (id BIGINT NOT NULL AUTO_INCREMENT,slug VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'new',score BIGINT NOT NULL DEFAULT 0,metadata JSON NOT NULL DEFAULT (JSON_OBJECT()),active TINYINT GENERATED ALWAYS AS(IF(score>0,1,NULL)) STORED,owner_id BIGINT NULL,PRIMARY KEY(id),CONSTRAINT uk_widget_slug UNIQUE(slug),KEY idx_widget_score(score DESC),CONSTRAINT ck_widget_score CHECK(score>=0),CONSTRAINT fk_widget_owner FOREIGN KEY(owner_id) REFERENCES user_account(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`

func TestLoadMigrations(t *testing.T) {
	entries, err := loadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 25 {
		t.Fatalf("migration count=%d", len(entries))
	}
	if entries[0].version != "001_create_user_account.sql" || entries[len(entries)-1].version != "025_create_flash_sale_release_job.sql" {
		t.Fatalf("range=%s..%s", entries[0].version, entries[len(entries)-1].version)
	}
	seen := map[[32]byte]string{}
	all := strings.Builder{}
	for _, entry := range entries {
		if other := seen[entry.sum]; other != "" {
			t.Fatalf("duplicate checksum %s and %s", other, entry.version)
		}
		seen[entry.sum] = entry.version
		all.WriteString(entry.sql)
	}
	for _, table := range []string{"user_account", "user_session", "player_profile", "conversation_session", "conversation_message", "game", "game_edition", "game_price", "game_entitlement", "community_post", "community_comment", "community_reaction", "coupon_campaign", "coupon_definition", "coupon_claim", "purchase_order", "purchase_order_item", "payment_record", "flash_sale_scope_lock", "flash_sale_activity", "flash_sale_reservation", "flash_sale_release_job"} {
		if !strings.Contains(all.String(), "CREATE TABLE "+table+" ") {
			t.Errorf("missing table %s", table)
		}
	}
	for _, forbidden := range []string{"knowledge_document", "knowledge_chunk", "tool_call_log", "CREATE EXTENSION", "JSONB", "TIMESTAMPTZ"} {
		if strings.Contains(strings.ToUpper(all.String()), strings.ToUpper(forbidden)) {
			t.Errorf("forbidden legacy construct %s", forbidden)
		}
	}
	for _, required := range []string{"idx_conversation_message_cursor(session_id,id)", "ck_coupon_campaign_code CHECK", "ck_coupon_definition_code CHECK", "ck_coupon_definition_currency CHECK"} {
		if !strings.Contains(all.String(), required) {
			t.Errorf("missing required schema fragment %s", required)
		}
	}
}

func TestCreateTableContractsParseForEmbeddedMigrations(t *testing.T) {
	entries, err := loadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	createTables := 0
	for _, entry := range entries {
		if !startsWithSQLKeywords(entry.sql, "CREATE", "TABLE") {
			continue
		}
		createTables++
		contract, err := parseCreateTableContract(entry.sql)
		if err != nil {
			t.Errorf("%s: %v", entry.version, err)
			continue
		}
		if contract.table == "" || len(contract.columns) == 0 || len(contract.indexes) == 0 || len(contract.constraints) == 0 {
			t.Errorf("%s produced incomplete contract: %+v", entry.version, contract)
		}
	}
	if createTables != 22 {
		t.Fatalf("CREATE TABLE migration count=%d", createTables)
	}
}

func TestWidgetContractMatchesCompleteFixture(t *testing.T) {
	contract, err := parseCreateTableContract(widgetMigrationSQL)
	if err != nil {
		t.Fatal(err)
	}
	schema := completeWidgetSchema()
	actualColumns := []createTableColumn{}
	for _, row := range schema.columns {
		var defaultValue *string
		if row[3] != nil {
			value := row[3].(string)
			if strings.Contains(strings.ToUpper(row[4].(string)), "DEFAULT_GENERATED") {
				value, err = canonicalSQLExpression(value)
				if err != nil {
					t.Fatal(err)
				}
			}
			defaultValue = &value
		}
		generation := ""
		if row[5].(string) != "" {
			generation, err = canonicalSQLExpression(row[5].(string))
			if err != nil {
				t.Fatal(err)
			}
		}
		actualColumns = append(actualColumns, createTableColumn{name: row[0].(string), columnType: row[1].(string), nullable: row[2].(string) == "YES", defaultValue: defaultValue, defaultExpression: strings.Contains(strings.ToUpper(row[4].(string)), "DEFAULT_GENERATED"), extra: normalizeColumnExtra(row[4].(string)), generationExpression: generation, charset: nullableDriverString(row[6]), collation: nullableDriverString(row[7])})
	}
	if !equalCreateTableColumns(contract.columns, actualColumns) {
		t.Fatalf("columns mismatch\nexpected=%+v\nactual=%+v", contract.columns, actualColumns)
	}
	check, err := canonicalSQLExpression(schema.checks[0][1].(string))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{"ck_widget_score": check}
	if !equalCreateTableChecks(contract.constraints, checks) {
		t.Fatalf("checks mismatch: %+v != %+v", contract.constraints, checks)
	}
}

func TestCanonicalSQLExpressionPreservesSemanticParentheses(t *testing.T) {
	want, err := canonicalSQLExpression(`(a AND b) OR (c AND d)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonicalSQLExpression(`a AND (b OR c) AND d`)
	if err != nil {
		t.Fatal(err)
	}
	if want == got {
		t.Fatalf("semantically distinct expressions canonicalized identically: %q", want)
	}
	metadata, err := canonicalSQLExpression("((`score` >= 0))")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := canonicalSQLExpression("score>=0")
	if err != nil {
		t.Fatal(err)
	}
	if metadata != direct {
		t.Fatal("outer metadata parentheses and backticks should normalize")
	}
	if _, err := canonicalSQLExpression("score LIKE '1%'"); err == nil {
		t.Fatal("unsupported expression syntax must fail closed")
	}
}

func TestRepairCreateTableRejectsCheckPrecedenceTampering(t *testing.T) {
	ddl := strings.Replace(widgetMigrationSQL, "CHECK(score>=0)", "CHECK((score>=0 AND owner_id IS NULL) OR (score<0 AND owner_id IS NOT NULL))", 1)
	files, checksum := widgetMigrationFiles(t, ddl)
	schema := completeWidgetSchema()
	schema.checks[0][1] = "(`score` >= 0 AND (`owner_id` IS NULL OR `score` < 0) AND `owner_id` IS NOT NULL)"
	state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, schema: schema}
	db := sql.OpenDB(migrationFakeConnector{state: state})
	defer db.Close()

	err := Repair(context.Background(), db, files, "001_create_widget.sql")
	if err == nil || !strings.Contains(err.Error(), "postcondition failed") {
		t.Fatalf("expected precedence-changing CHECK rejection, got %v", err)
	}
	if strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
		t.Fatalf("precedence-changing CHECK was marked clean: %v", state.execs)
	}
}

func nullableDriverString(value driver.Value) string {
	if value == nil {
		return ""
	}
	return value.(string)
}

func TestLoadMigrationsRejectsMultipleStatements(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{"001_bad.sql": {Data: []byte("SELECT 1; SELECT 2")}})
	if err == nil {
		t.Fatal("expected multi-statement migration rejection")
	}
}

func TestMigrate(t *testing.T) {
	t.Run("applies dirty statement clean in order", func(t *testing.T) {
		state := &migrationFakeState{lock: ptr(1), release: ptr(1)}
		db := sql.OpenDB(migrationFakeConnector{state: state})
		defer db.Close()
		files := fstest.MapFS{"001_create_widget.sql": {Data: []byte(widgetMigrationSQL + ";\n")}}
		if err := Migrate(context.Background(), db, files, MigrationOptions{LockTimeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(state.execs, "\n")
		for _, fragment := range []string{"CREATE TABLE IF NOT EXISTS schema_migration", "INSERT INTO schema_migration", "CREATE TABLE widget", "UPDATE schema_migration SET dirty=0"} {
			if !strings.Contains(joined, fragment) {
				t.Errorf("missing %q in %s", fragment, joined)
			}
		}
	})
	t.Run("failed statement remains dirty", func(t *testing.T) {
		const canary = "password=secret-canary SELECT * FROM private_table WHERE user_id=991337"
		state := &migrationFakeState{lock: ptr(1), release: ptr(1), failContains: "CREATE TABLE widget", failErr: errors.New(canary)}
		db := sql.OpenDB(migrationFakeConnector{state: state})
		defer db.Close()
		err := Migrate(context.Background(), db, fstest.MapFS{"001_create_widget.sql": {Data: []byte(widgetMigrationSQL)}}, MigrationOptions{LockTimeout: time.Second})
		if err == nil || strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
			t.Fatalf("execs=%v err=%v", state.execs, err)
		}
		if !strings.Contains(strings.Join(state.execs, "\n"), "failure_context") {
			t.Fatalf("failure was not recorded: %v", state.execs)
		}
		if got := state.failureContexts; len(got) != 1 || got[0] != "stage=apply,error_class=database" {
			t.Fatalf("failure contexts=%q", got)
		}
		if strings.Contains(strings.Join(state.failureContexts, "\n"), canary) {
			t.Fatal("raw database error leaked into migration metadata")
		}
	})
	t.Run("failed postcondition remains dirty", func(t *testing.T) {
		state := &migrationFakeState{lock: ptr(1), release: ptr(1), schema: incompleteWidgetSchema()}
		db := sql.OpenDB(migrationFakeConnector{state: state})
		defer db.Close()
		err := Migrate(context.Background(), db, fstest.MapFS{"001_create_widget.sql": {Data: []byte(widgetMigrationSQL)}}, MigrationOptions{LockTimeout: time.Second})
		joined := strings.Join(state.execs, "\n")
		if err == nil || !strings.Contains(err.Error(), "postcondition failed") {
			t.Fatalf("expected postcondition failure, got %v", err)
		}
		if strings.Contains(joined, "SET dirty=0") {
			t.Fatalf("migration was incorrectly marked clean: %v", state.execs)
		}
		if !strings.Contains(joined, "failure_context") {
			t.Fatalf("postcondition failure was not recorded: %v", state.execs)
		}
		if got := state.failureContexts; len(got) != 1 || got[0] != "stage=verify,error_class=postcondition" {
			t.Fatalf("failure contexts=%q", got)
		}
	})
}

func TestMigrationFailureContextIsFixedAndPrivate(t *testing.T) {
	const canary = "mysql://admin:secret@private/db SELECT token_hash FROM user_session user_id=918273"
	for _, test := range []struct {
		name, stage, want string
		err               error
	}{
		{name: "database", stage: "apply", err: errors.New(canary), want: "stage=apply,error_class=database"},
		{name: "postcondition", stage: "verify", err: errors.New(canary), want: "stage=verify,error_class=postcondition"},
		{name: "cancelled", stage: "apply", err: fmt.Errorf("%s: %w", canary, context.Canceled), want: "stage=apply,error_class=cancelled"},
		{name: "deadline", stage: "verify", err: fmt.Errorf("%s: %w", canary, context.DeadlineExceeded), want: "stage=verify,error_class=deadline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := migrationFailureContext(test.stage, test.err)
			if got != test.want || strings.Contains(got, canary) {
				t.Fatalf("failure context=%q want=%q", got, test.want)
			}
		})
	}
}

func TestMigrationOperationsAndStatesAreObserved(t *testing.T) {
	const canary = "mysql://admin:secret@private/db SELECT * FROM users WHERE user_id=42"
	registry := platformmetrics.NewRegistry()
	if err := Migrate(context.Background(), nil, nil, MigrationOptions{Metrics: registry}); err == nil {
		t.Fatal("expected invalid migrate options")
	}
	if _, err := inspect(context.Background(), nil, nil, registry); err == nil {
		t.Fatal("expected invalid inspection")
	}
	if err := repair(context.Background(), nil, nil, canary, registry); err == nil {
		t.Fatal("expected invalid repair")
	}
	setMigrationMetrics(registry, Inspection{
		Applied: []MigrationState{{}, {}, {}}, Pending: []string{"pending"}, Dirty: []MigrationState{{}},
	})
	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_mysql_operations_total{operation="migrate",outcome="error"} 1`,
		`xiaolanhe_mysql_operations_total{operation="inspect_migrations",outcome="error"} 1`,
		`xiaolanhe_mysql_operations_total{operation="repair_migration",outcome="error"} 1`,
		`xiaolanhe_mysql_migrations{state="applied"} 2`,
		`xiaolanhe_mysql_migrations{state="pending"} 1`,
		`xiaolanhe_mysql_migrations{state="dirty"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
	if strings.Contains(output, canary) {
		t.Fatalf("private input leaked to metrics:\n%s", output)
	}
}

func TestRepairCreateTableRejectsIncompleteSameNameTable(t *testing.T) {
	tests := map[string]func(*migrationFakeSchema){
		"missing column": func(schema *migrationFakeSchema) { schema.columns = schema.columns[:1] },
		"wrong column type": func(schema *migrationFakeSchema) {
			schema.columns[1] = []driver.Value{"slug", "varchar(32)", "NO", "new", "", "", "ascii", "ascii_bin"}
		},
		"wrong literal default":           func(schema *migrationFakeSchema) { schema.columns[1][3] = "old" },
		"wrong expression default":        func(schema *migrationFakeSchema) { schema.columns[3][3] = "json_array()" },
		"missing expression default flag": func(schema *migrationFakeSchema) { schema.columns[3][4] = "" },
		"wrong generated expression":      func(schema *migrationFakeSchema) { schema.columns[4][5] = "if(`score` >= 0,1,NULL)" },
		"wrong generated storage":         func(schema *migrationFakeSchema) { schema.columns[4][4] = "VIRTUAL GENERATED" },
		"wrong character set":             func(schema *migrationFakeSchema) { schema.columns[1][6] = "utf8mb4" },
		"wrong column collation":          func(schema *migrationFakeSchema) { schema.columns[1][7] = "ascii_general_ci" },
		"wrong table character set":       func(schema *migrationFakeSchema) { schema.charset = "latin1" },
		"tampered CHECK clause":           func(schema *migrationFakeSchema) { schema.checks[0][1] = "(`score` >= -1)" },
		"unenforced CHECK":                func(schema *migrationFakeSchema) { schema.checks[0][2] = "NO" },
		"tampered FK local column":        func(schema *migrationFakeSchema) { schema.foreignKeys[0][2] = "score" },
		"tampered FK referenced schema":   func(schema *migrationFakeSchema) { schema.foreignKeys[0][4] = false },
		"tampered FK referenced table":    func(schema *migrationFakeSchema) { schema.foreignKeys[0][5] = "other_account" },
		"tampered FK referenced column":   func(schema *migrationFakeSchema) { schema.foreignKeys[0][6] = "other_id" },
		"tampered FK update rule":         func(schema *migrationFakeSchema) { schema.foreignKeys[0][8] = "CASCADE" },
		"tampered FK delete rule":         func(schema *migrationFakeSchema) { schema.foreignKeys[0][9] = "RESTRICT" },
		"missing index": func(schema *migrationFakeSchema) {
			schema.indexes = append(schema.indexes[:2], schema.indexes[3:]...)
		},
		"unexpected unrelated index": func(schema *migrationFakeSchema) {
			schema.indexes = append(schema.indexes, indexRow("idx_widget_unexpected", 1, 1, "owner_id", "A"))
		},
		"invalid implicit FK index": func(schema *migrationFakeSchema) {
			schema.indexes[1][1] = int64(0)
		},
		"prefix index": func(schema *migrationFakeSchema) { schema.indexes[2][5] = int64(8) },
		"expression index": func(schema *migrationFakeSchema) {
			schema.indexes[2][6] = "(`score`)"
		},
		"non-BTREE index":    func(schema *migrationFakeSchema) { schema.indexes[2][7] = "HASH" },
		"invisible index":    func(schema *migrationFakeSchema) { schema.indexes[2][8] = "NO" },
		"missing constraint": func(schema *migrationFakeSchema) { schema.constraints = schema.constraints[:2] },
		"wrong engine":       func(schema *migrationFakeSchema) { schema.engine = "MyISAM" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			files, checksum := widgetMigrationFiles(t, widgetMigrationSQL)
			schema := completeWidgetSchema()
			mutate(schema)
			state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, schema: schema}
			db := sql.OpenDB(migrationFakeConnector{state: state})
			defer db.Close()

			err := Repair(context.Background(), db, files, "001_create_widget.sql")
			if err == nil || !strings.Contains(err.Error(), "postcondition failed") {
				t.Fatalf("expected incomplete table rejection, got %v", err)
			}
			if strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
				t.Fatalf("incomplete table was marked clean: %v", state.execs)
			}
		})
	}
}

func TestRepairCreateTableSucceedsForCompleteContract(t *testing.T) {
	files, checksum := widgetMigrationFiles(t, widgetMigrationSQL)
	state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true}
	db := sql.OpenDB(migrationFakeConnector{state: state})
	defer db.Close()

	if err := Repair(context.Background(), db, files, "001_create_widget.sql"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
		t.Fatalf("complete table was not marked clean: %v", state.execs)
	}
	for _, fragment := range []string{"information_schema.tables", "information_schema.columns", "information_schema.statistics", "information_schema.table_constraints", "information_schema.check_constraints", "information_schema.referential_constraints"} {
		if !strings.Contains(strings.Join(state.queries, "\n"), fragment) {
			t.Errorf("missing schema verification query %q: %v", fragment, state.queries)
		}
	}
}

func TestRepairCreateTableAllowsMySQLImplicitForeignKeyIndex(t *testing.T) {
	files, checksum := widgetMigrationFiles(t, widgetMigrationSQL)
	schema := completeWidgetSchema()
	state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, schema: schema}
	db := sql.OpenDB(migrationFakeConnector{state: state})
	defer db.Close()

	if err := Repair(context.Background(), db, files, "001_create_widget.sql"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTableIndexContractRequiresOnlyNecessaryImplicitForeignKeyIndexes(t *testing.T) {
	ddl := `CREATE TABLE child (tenant_id BIGINT NOT NULL,parent_id BIGINT NOT NULL,owner_id BIGINT NOT NULL,KEY idx_child_parent(tenant_id,parent_id,owner_id),CONSTRAINT fk_child_parent FOREIGN KEY(tenant_id,parent_id) REFERENCES parent(tenant_id,id),CONSTRAINT fk_child_owner FOREIGN KEY(owner_id) REFERENCES user_account(id)) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
	contract, err := parseCreateTableContract(ddl)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]createTableIndex{
		"idx_child_parent": {name: "idx_child_parent", columns: []createTableIndexColumn{{name: "tenant_id"}, {name: "parent_id"}, {name: "owner_id"}}},
		"fk_child_owner":   {name: "fk_child_owner", columns: []createTableIndexColumn{{name: "owner_id"}}},
	}
	if !equalCreateTableIndexes(contract, actual) {
		t.Fatal("expected explicit left-prefix and necessary implicit FK indexes to match")
	}
	actual["fk_child_parent"] = createTableIndex{name: "fk_child_parent", columns: []createTableIndexColumn{{name: "tenant_id"}, {name: "parent_id"}}}
	if equalCreateTableIndexes(contract, actual) {
		t.Fatal("unnecessary FK-named index was accepted")
	}
}

func TestRepairCreateTableSchemaQueryErrorsFailClosed(t *testing.T) {
	for _, fragment := range []string{"information_schema.tables", "information_schema.columns", "information_schema.statistics", "information_schema.table_constraints", "information_schema.check_constraints", "information_schema.referential_constraints"} {
		t.Run(fragment, func(t *testing.T) {
			files, checksum := widgetMigrationFiles(t, widgetMigrationSQL)
			state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, failQueryContains: fragment}
			db := sql.OpenDB(migrationFakeConnector{state: state})
			defer db.Close()

			err := Repair(context.Background(), db, files, "001_create_widget.sql")
			if err == nil || !strings.Contains(err.Error(), "injected schema query failure") {
				t.Fatalf("expected query error, got %v", err)
			}
			if strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
				t.Fatalf("query error marked migration clean: %v", state.execs)
			}
		})
	}
}

func TestRepairCreateTableUnsupportedDDLRequiresManualRebuild(t *testing.T) {
	ddl := `CREATE TABLE widget(id BIGINT PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
	files, checksum := widgetMigrationFiles(t, ddl)
	state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true}
	db := sql.OpenDB(migrationFakeConnector{state: state})
	defer db.Close()

	err := Repair(context.Background(), db, files, "001_create_widget.sql")
	if err == nil || !strings.Contains(err.Error(), "rebuild the table manually") {
		t.Fatalf("expected fail-closed repair error, got %v", err)
	}
	if strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
		t.Fatalf("unsupported DDL was marked clean: %v", state.execs)
	}
}

func TestCreateTableOptionsFailClosed(t *testing.T) {
	tests := map[string]string{
		"unknown option":             `ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='ENGINE=MyISAM'`,
		"duplicate engine":           `ENGINE=InnoDB ENGINE=MyISAM DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`,
		"duplicate charset spelling": `ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 CHARSET=latin1 COLLATE=utf8mb4_0900_ai_ci`,
		"duplicate collation":        `ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COLLATE=utf8mb4_bin`,
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			ddl := `CREATE TABLE widget (id BIGINT NOT NULL,PRIMARY KEY(id)) ` + options
			if _, err := parseCreateTableContract(ddl); err == nil {
				t.Fatal("expected unsupported table options to fail closed")
			}
		})
	}
}

func TestRepairEmbeddedAlterTableForeignKeys(t *testing.T) {
	tests := []struct {
		version          string
		table            string
		constraint       string
		column           string
		referencedTable  string
		referencedColumn string
		deleteRule       string
	}{
		{"006_add_conversation_summary_fk.sql", "conversation_session", "fk_conversation_summary_message", "summary_through_message_id", "conversation_message", "id", "SET NULL"},
		{"014_add_order_claim_foreign_keys.sql", "purchase_order", "fk_purchase_order_coupon_claim", "coupon_claim_id", "coupon_claim", "id", "RESTRICT"},
		{"015_add_claim_order_foreign_key.sql", "coupon_claim", "fk_coupon_claim_redeemed_order", "redeemed_order_id", "purchase_order", "id", "RESTRICT"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			ddl, err := fs.ReadFile(migrations.Files, test.version)
			if err != nil {
				t.Fatal(err)
			}
			files, checksum := migrationFiles(t, test.version, string(ddl))
			schema := alterForeignKeySchema(test.constraint, [][]driver.Value{{test.constraint, int64(1), test.column, int64(1), true, test.referencedTable, test.referencedColumn, "NONE", "NO ACTION", test.deleteRule}})
			state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, schema: schema}
			db := sql.OpenDB(migrationFakeConnector{state: state})
			defer db.Close()

			if err := Repair(context.Background(), db, files, test.version); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
				t.Fatalf("verified ALTER migration was not marked clean: %v", state.execs)
			}
			queries := strings.Join(state.queries, "\n")
			for _, fragment := range []string{"information_schema.table_constraints", "information_schema.referential_constraints"} {
				if !strings.Contains(queries, fragment) {
					t.Errorf("missing ALTER verification query %q: %v", fragment, state.queries)
				}
			}
		})
	}
}

func TestRepairAlterTableChecksOnlyTargetForeignKey(t *testing.T) {
	const version = "001_add_target_fk.sql"
	const ddl = `ALTER TABLE child ADD CONSTRAINT fk_child_parent FOREIGN KEY(parent_id) REFERENCES parent(id) ON DELETE RESTRICT`
	files, checksum := migrationFiles(t, version, ddl)
	schema := alterForeignKeySchema("fk_child_parent", [][]driver.Value{
		{"fk_child_existing", int64(1), "owner_id", int64(1), true, "user_account", "id", "NONE", "NO ACTION", "CASCADE"},
		{"fk_child_parent", int64(1), "parent_id", int64(1), true, "parent", "id", "NONE", "NO ACTION", "RESTRICT"},
	})
	schema.constraints = append(schema.constraints, []driver.Value{"fk_child_existing", "FOREIGN KEY"})
	state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, schema: schema}
	db := sql.OpenDB(migrationFakeConnector{state: state})
	defer db.Close()

	if err := Repair(context.Background(), db, files, version); err != nil {
		t.Fatal(err)
	}
}

func TestRepairAlterTableRejectsSemanticTampering(t *testing.T) {
	const version = "001_add_child_parent_fk.sql"
	const ddl = `ALTER TABLE child ADD CONSTRAINT fk_child_parent FOREIGN KEY(tenant_id,parent_id) REFERENCES parent(tenant_id,id) ON UPDATE CASCADE ON DELETE SET NULL`
	tests := map[string]func(*migrationFakeSchema){
		"missing constraint": func(schema *migrationFakeSchema) { schema.constraints = nil },
		"wrong constraint kind": func(schema *migrationFakeSchema) {
			schema.constraints[0][1] = "UNIQUE"
		},
		"missing foreign key":        func(schema *migrationFakeSchema) { schema.foreignKeys = nil },
		"wrong local column order":   func(schema *migrationFakeSchema) { schema.foreignKeys[0][2] = "parent_id" },
		"wrong referenced key order": func(schema *migrationFakeSchema) { schema.foreignKeys[0][3] = int64(2) },
		"wrong referenced schema":    func(schema *migrationFakeSchema) { schema.foreignKeys[0][4] = false },
		"wrong referenced table":     func(schema *migrationFakeSchema) { schema.foreignKeys[0][5] = "other_parent" },
		"wrong referenced column":    func(schema *migrationFakeSchema) { schema.foreignKeys[1][6] = "other_id" },
		"wrong match option":         func(schema *migrationFakeSchema) { schema.foreignKeys[0][7] = "PARTIAL" },
		"wrong update rule":          func(schema *migrationFakeSchema) { schema.foreignKeys[0][8] = "NO ACTION" },
		"wrong delete rule":          func(schema *migrationFakeSchema) { schema.foreignKeys[0][9] = "RESTRICT" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			files, checksum := migrationFiles(t, version, ddl)
			schema := alterForeignKeySchema("fk_child_parent", [][]driver.Value{
				{"fk_child_parent", int64(1), "tenant_id", int64(1), true, "parent", "tenant_id", "NONE", "CASCADE", "SET NULL"},
				{"fk_child_parent", int64(2), "parent_id", int64(2), true, "parent", "id", "NONE", "CASCADE", "SET NULL"},
			})
			mutate(schema)
			state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, schema: schema}
			db := sql.OpenDB(migrationFakeConnector{state: state})
			defer db.Close()

			err := Repair(context.Background(), db, files, version)
			if err == nil || !strings.Contains(err.Error(), "postcondition failed") {
				t.Fatalf("expected ALTER semantic mismatch, got %v", err)
			}
			if strings.Contains(strings.Join(state.execs, "\n"), "SET dirty=0") {
				t.Fatalf("tampered ALTER was marked clean: %v", state.execs)
			}
		})
	}
}

func TestRepairAlterTableSchemaQueryErrorsFailClosed(t *testing.T) {
	const version = "001_add_target_fk.sql"
	const ddl = `ALTER TABLE child ADD CONSTRAINT fk_child_parent FOREIGN KEY(parent_id) REFERENCES parent(id)`
	for _, fragment := range []string{"information_schema.table_constraints", "information_schema.referential_constraints"} {
		t.Run(fragment, func(t *testing.T) {
			files, checksum := migrationFiles(t, version, ddl)
			schema := alterForeignKeySchema("fk_child_parent", [][]driver.Value{{"fk_child_parent", int64(1), "parent_id", int64(1), true, "parent", "id", "NONE", "NO ACTION", "NO ACTION"}})
			state := &migrationFakeState{lock: ptr(1), release: ptr(1), storedChecksum: checksum, storedDirty: true, schema: schema, failQueryContains: fragment}
			db := sql.OpenDB(migrationFakeConnector{state: state})
			defer db.Close()

			err := Repair(context.Background(), db, files, version)
			if err == nil || !strings.Contains(err.Error(), "injected schema query failure") {
				t.Fatalf("expected ALTER query failure, got %v", err)
			}
		})
	}
}

func widgetMigrationFiles(t *testing.T, ddl string) (fstest.MapFS, []byte) {
	t.Helper()
	return migrationFiles(t, "001_create_widget.sql", ddl)
}

func migrationFiles(t *testing.T, version, ddl string) (fstest.MapFS, []byte) {
	t.Helper()
	files := fstest.MapFS{version: {Data: []byte(ddl)}}
	entries, err := loadMigrations(files)
	if err != nil {
		t.Fatal(err)
	}
	checksum := append([]byte(nil), entries[0].sum[:]...)
	return files, checksum
}

func alterForeignKeySchema(constraint string, foreignKeys [][]driver.Value) *migrationFakeSchema {
	return &migrationFakeSchema{
		constraints: [][]driver.Value{{constraint, "FOREIGN KEY"}},
		foreignKeys: foreignKeys,
	}
}

type migrationFakeState struct {
	execs, queries    []string
	failureContexts   []string
	lock, release     *int64
	failContains      string
	failErr           error
	failQueryContains string
	storedChecksum    []byte
	storedDirty       bool
	schema            *migrationFakeSchema
}

type migrationFakeSchema struct {
	exists                     bool
	engine, charset, collation string
	columns, indexes           [][]driver.Value
	constraints, checks        [][]driver.Value
	foreignKeys                [][]driver.Value
}
type migrationFakeConnector struct{ state *migrationFakeState }

func (c migrationFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &migrationFakeConn{state: c.state}, nil
}
func (migrationFakeConnector) Driver() driver.Driver { return migrationFakeDriver{} }

type migrationFakeDriver struct{}

func (migrationFakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type migrationFakeConn struct{ state *migrationFakeState }

func (*migrationFakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*migrationFakeConn) Close() error                        { return nil }
func (*migrationFakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *migrationFakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.state.execs = append(c.state.execs, q)
	if strings.Contains(q, "SET failure_context=?") && len(args) > 0 {
		if value, ok := args[0].Value.(string); ok {
			c.state.failureContexts = append(c.state.failureContexts, value)
		}
	}
	if c.state.failContains != "" && strings.Contains(q, c.state.failContains) {
		if c.state.failErr != nil {
			return nil, c.state.failErr
		}
		return nil, errors.New("injected DDL failure")
	}
	return driver.RowsAffected(1), nil
}
func (c *migrationFakeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, q)
	if c.state.failQueryContains != "" && strings.Contains(q, c.state.failQueryContains) {
		return nil, errors.New("injected schema query failure")
	}
	schema := c.state.schema
	if schema == nil {
		schema = completeWidgetSchema()
	}
	switch {
	case q == `SELECT GET_LOCK(?,?)`:
		return &migrationRows{columns: []string{"lock"}, rows: [][]driver.Value{{nullableValue(c.state.lock)}}}, nil
	case q == `SELECT RELEASE_LOCK(?)`:
		return &migrationRows{columns: []string{"release"}, rows: [][]driver.Value{{nullableValue(c.state.release)}}}, nil
	case strings.Contains(q, "FROM schema_migration ORDER BY version"):
		return &migrationRows{columns: []string{"version", "name", "checksum", "dirty", "started_at", "completed_at", "failure_context"}}, nil
	case strings.Contains(q, "SELECT checksum_sha256,dirty FROM schema_migration"):
		return &migrationRows{columns: []string{"checksum_sha256", "dirty"}, rows: [][]driver.Value{{c.state.storedChecksum, c.state.storedDirty}}}, nil
	case strings.Contains(q, "FROM information_schema.tables"):
		rows := [][]driver.Value{}
		if schema.exists {
			rows = append(rows, []driver.Value{schema.engine, schema.charset, schema.collation})
		}
		return &migrationRows{columns: []string{"engine", "character_set_name", "table_collation"}, rows: rows}, nil
	case strings.Contains(q, "FROM information_schema.columns"):
		return &migrationRows{columns: []string{"column_name", "column_type", "is_nullable", "column_default", "extra", "generation_expression", "character_set_name", "collation_name"}, rows: schema.columns}, nil
	case strings.Contains(q, "FROM information_schema.statistics"):
		return &migrationRows{columns: []string{"index_name", "non_unique", "seq_in_index", "column_name", "collation", "sub_part", "expression", "index_type", "is_visible"}, rows: schema.indexes}, nil
	case strings.Contains(q, "JOIN information_schema.check_constraints"):
		return &migrationRows{columns: []string{"constraint_name", "check_clause", "enforced"}, rows: schema.checks}, nil
	case strings.Contains(q, "FROM information_schema.referential_constraints"):
		return &migrationRows{columns: []string{"constraint_name", "ordinal_position", "column_name", "position_in_unique_constraint", "referenced_schema_is_current", "referenced_table_name", "referenced_column_name", "match_option", "update_rule", "delete_rule"}, rows: schema.foreignKeys}, nil
	case strings.Contains(q, "FROM information_schema.table_constraints"):
		return &migrationRows{columns: []string{"constraint_name", "constraint_type"}, rows: schema.constraints}, nil
	default:
		return nil, errors.New("unexpected query: " + q)
	}
}

func completeWidgetSchema() *migrationFakeSchema {
	return &migrationFakeSchema{
		exists:    true,
		engine:    "InnoDB",
		charset:   "utf8mb4",
		collation: "utf8mb4_0900_ai_ci",
		columns: [][]driver.Value{
			{"id", "bigint", "NO", nil, "auto_increment", "", nil, nil},
			{"slug", "varchar(64)", "NO", "new", "", "", "ascii", "ascii_bin"},
			{"score", "bigint", "NO", "0", "", "", nil, nil},
			{"metadata", "json", "NO", "json_object()", "DEFAULT_GENERATED", "", nil, nil},
			{"active", "tinyint", "YES", nil, "STORED GENERATED", "if(`score` > 0,1,NULL)", nil, nil},
			{"owner_id", "bigint", "YES", nil, "", "", nil, nil},
		},
		indexes: [][]driver.Value{
			indexRow("PRIMARY", 0, 1, "id", "A"),
			indexRow("fk_widget_owner", 1, 1, "owner_id", "A"),
			indexRow("idx_widget_score", 1, 1, "score", "D"),
			indexRow("uk_widget_slug", 0, 1, "slug", "A"),
		},
		constraints: [][]driver.Value{
			{"PRIMARY", "PRIMARY KEY"},
			{"ck_widget_score", "CHECK"},
			{"fk_widget_owner", "FOREIGN KEY"},
			{"uk_widget_slug", "UNIQUE"},
		},
		checks:      [][]driver.Value{{"ck_widget_score", "(`score` >= 0)", "YES"}},
		foreignKeys: [][]driver.Value{{"fk_widget_owner", int64(1), "owner_id", int64(1), true, "user_account", "id", "NONE", "NO ACTION", "CASCADE"}},
	}
}

func indexRow(name string, nonUnique, sequence int64, column, direction string) []driver.Value {
	return []driver.Value{name, nonUnique, sequence, column, direction, nil, nil, "BTREE", "YES"}
}

func incompleteWidgetSchema() *migrationFakeSchema {
	schema := completeWidgetSchema()
	schema.columns = schema.columns[:1]
	return schema
}

type migrationRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *migrationRows) Columns() []string { return r.columns }
func (*migrationRows) Close() error        { return nil }
func (r *migrationRows) Next(dst []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dst, r.rows[r.index])
	r.index++
	return nil
}
func nullableValue(v *int64) driver.Value {
	if v == nil {
		return nil
	}
	return *v
}
