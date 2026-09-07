package postgrestomysql

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	mysqlmigrations "github.com/red060324/XiaoLanHe/migrations/mysql"
)

// targetSchemaContract is derived from the same immutable embedded migration
// bytes used by the MySQL migration runner. It intentionally contains the
// semantic fields exposed by information_schema; the separately persisted
// schema digest binds the remaining physical metadata as well.
type targetSchemaContract struct {
	tables map[string]*targetTableContract
}

type targetTableContract struct {
	name, engine, charset, collation string
	columns                          []targetColumnContract
	indexes                          map[string]targetIndexContract
	constraints                      map[string]targetConstraintContract
}

type targetColumnContract struct {
	name, columnType, defaultValue, generated, charset, collation string
	nullable, hasDefault, autoIncrement                           bool
}

type targetIndexContract struct {
	name    string
	unique  bool
	columns []targetIndexColumnContract
}

type targetIndexColumnContract struct {
	name       string
	descending bool
}

type targetConstraintContract struct {
	name, kind, check, referencedTable, deleteRule, updateRule string
	columns, referencedColumns                                 []string
}

var (
	targetCreateTablePattern = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+` + "`?([a-zA-Z0-9_]+)`?" + `\s*\(`)
	targetAlterTablePattern  = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+` + "`?([a-zA-Z0-9_]+)`?" + `\s+ADD\s+(.+)$`)
	targetTableOptions       = regexp.MustCompile(`(?is)\bENGINE\s*=\s*([a-zA-Z0-9_]+).*?\bDEFAULT\s+CHARACTER\s+SET\s*=\s*([a-zA-Z0-9_]+).*?\bCOLLATE\s*=\s*([a-zA-Z0-9_]+)`)
	targetConstraintPrefix   = regexp.MustCompile(`(?is)^CONSTRAINT\s+` + "`?([a-zA-Z0-9_]+)`?" + `\s+(.+)$`)
	targetForeignKeyPattern  = regexp.MustCompile(`(?is)^FOREIGN\s+KEY\s*\(([^)]*)\)\s+REFERENCES\s+` + "`?([a-zA-Z0-9_]+)`?" + `\s*\(([^)]*)\)(.*)$`)
	targetCharsetPattern     = regexp.MustCompile(`(?i)\bCHARACTER\s+SET\s+([a-zA-Z0-9_]+)`)
	targetCollationPattern   = regexp.MustCompile(`(?i)\bCOLLATE\s+([a-zA-Z0-9_]+)`)
)

func expectedTargetSchemaContract(specs []tableSpec) (targetSchemaContract, error) {
	wanted := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if spec.name == "" || wanted[spec.name] {
			return targetSchemaContract{}, fmt.Errorf("invalid duplicate mapped MySQL table %q", spec.name)
		}
		wanted[spec.name] = true
	}
	entries, err := fs.ReadDir(mysqlmigrations.Files, ".")
	if err != nil {
		return targetSchemaContract{}, fmt.Errorf("read embedded MySQL migrations for schema contract: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	contract := targetSchemaContract{tables: make(map[string]*targetTableContract, len(wanted))}
	for _, name := range names {
		contents, readErr := fs.ReadFile(mysqlmigrations.Files, name)
		if readErr != nil {
			return targetSchemaContract{}, fmt.Errorf("read embedded MySQL migration %s: %w", name, readErr)
		}
		statement := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(contents)), ";"))
		if matches := targetCreateTablePattern.FindStringSubmatch(statement); len(matches) == 2 {
			if !wanted[matches[1]] {
				continue
			}
			table, parseErr := parseTargetCreateTable(statement, matches[1])
			if parseErr != nil {
				return targetSchemaContract{}, fmt.Errorf("parse embedded MySQL migration %s: %w", name, parseErr)
			}
			if contract.tables[table.name] != nil {
				return targetSchemaContract{}, fmt.Errorf("embedded MySQL table %s is created more than once", table.name)
			}
			contract.tables[table.name] = table
			continue
		}
		if matches := targetAlterTablePattern.FindStringSubmatch(statement); len(matches) == 3 {
			if !wanted[matches[1]] {
				continue
			}
			table := contract.tables[matches[1]]
			if table == nil {
				return targetSchemaContract{}, fmt.Errorf("embedded MySQL ALTER TABLE precedes CREATE TABLE for %s", matches[1])
			}
			constraint, parseErr := parseTargetConstraint(matches[2])
			if parseErr != nil {
				return targetSchemaContract{}, fmt.Errorf("parse embedded MySQL migration %s: %w", name, parseErr)
			}
			if parseErr = addTargetConstraint(table, constraint); parseErr != nil {
				return targetSchemaContract{}, fmt.Errorf("parse embedded MySQL migration %s: %w", name, parseErr)
			}
			continue
		}
		return targetSchemaContract{}, fmt.Errorf("unsupported embedded MySQL migration statement in %s", name)
	}
	if contract.tables["schema_migration"] != nil {
		return targetSchemaContract{}, errors.New("embedded MySQL migrations must not create schema_migration")
	}
	contract.tables["schema_migration"] = targetMigrationMetadataContract()
	for tableName, spec := range mapTargetSpecs(specs) {
		table := contract.tables[tableName]
		if table == nil {
			return targetSchemaContract{}, fmt.Errorf("embedded MySQL schema has no mapped table %s", tableName)
		}
		columns := make(map[string]bool, len(table.columns))
		for _, column := range table.columns {
			columns[column.name] = true
		}
		for _, column := range spec.columns {
			if !columns[column.name] {
				return targetSchemaContract{}, fmt.Errorf("embedded MySQL table %s lacks mapped cutover column %s", tableName, column.name)
			}
		}
		primary := table.constraints["PRIMARY"]
		if strings.Join(primary.columns, "\x00") != strings.Join(spec.keyNames(false), "\x00") {
			return targetSchemaContract{}, fmt.Errorf("embedded MySQL table %s primary key %v differs from mapped key %v", tableName, primary.columns, spec.keyNames(false))
		}
	}
	return contract, nil
}

func targetMigrationMetadataContract() *targetTableContract {
	table := &targetTableContract{
		name: "schema_migration", engine: "innodb", charset: "utf8mb4", collation: "utf8mb4_0900_ai_ci",
		columns: []targetColumnContract{
			{name: "version", columnType: "varchar(255)", charset: "ascii", collation: "ascii_bin"},
			{name: "name", columnType: "varchar(255)", charset: "utf8mb4", collation: "utf8mb4_0900_ai_ci"},
			{name: "checksum_sha256", columnType: "binary(32)"},
			{name: "dirty", columnType: "tinyint(1)", hasDefault: true, defaultValue: "value:1"},
			{name: "started_at", columnType: "datetime(6)", hasDefault: true, defaultValue: "value:current_timestamp(6)"},
			{name: "completed_at", columnType: "datetime(6)", nullable: true},
			{name: "failure_context", columnType: "varchar(1024)", nullable: true, charset: "utf8mb4", collation: "utf8mb4_0900_ai_ci"},
		},
		indexes: map[string]targetIndexContract{},
		constraints: map[string]targetConstraintContract{
			"PRIMARY":                        {name: "PRIMARY", kind: "PRIMARY KEY", columns: []string{"version"}},
			"ck_schema_migration_dirty":      {name: "ck_schema_migration_dirty", kind: "CHECK", check: normalizeTargetExpression("dirty IN (0,1)")},
			"ck_schema_migration_completion": {name: "ck_schema_migration_completion", kind: "CHECK", check: normalizeTargetExpression("(dirty=1 AND completed_at IS NULL) OR (dirty=0 AND completed_at IS NOT NULL)")},
		},
	}
	table.indexes["PRIMARY"] = targetIndexContract{name: "PRIMARY", unique: true, columns: []targetIndexColumnContract{{name: "version"}}}
	return table
}

func mapTargetSpecs(specs []tableSpec) map[string]tableSpec {
	result := make(map[string]tableSpec, len(specs))
	for _, spec := range specs {
		result[spec.name] = spec
	}
	return result
}

func parseTargetCreateTable(statement, tableName string) (*targetTableContract, error) {
	open := strings.Index(statement, "(")
	close, err := matchingTargetParen(statement, open)
	if err != nil {
		return nil, err
	}
	options := targetTableOptions.FindStringSubmatch(statement[close+1:])
	if len(options) != 4 {
		return nil, errors.New("CREATE TABLE must declare engine, default character set, and collation")
	}
	table := &targetTableContract{
		name: tableName, engine: strings.ToLower(options[1]), charset: strings.ToLower(options[2]), collation: strings.ToLower(options[3]),
		indexes: map[string]targetIndexContract{}, constraints: map[string]targetConstraintContract{},
	}
	parts, err := splitTargetTopLevel(statement[open+1:close], ',')
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		upper := strings.ToUpper(strings.TrimSpace(part))
		switch {
		case strings.HasPrefix(upper, "PRIMARY KEY"):
			columns, parseErr := targetParenthesizedIdentifiers(part[strings.Index(upper, "KEY")+len("KEY"):])
			if parseErr != nil {
				return nil, fmt.Errorf("parse PRIMARY KEY %q: %w", part, parseErr)
			}
			constraint := targetConstraintContract{name: "PRIMARY", kind: "PRIMARY KEY", columns: columns}
			if parseErr = addTargetConstraint(table, constraint); parseErr != nil {
				return nil, parseErr
			}
		case strings.HasPrefix(upper, "CONSTRAINT "):
			constraint, parseErr := parseTargetConstraint(part)
			if parseErr != nil {
				return nil, fmt.Errorf("parse constraint %q: %w", part, parseErr)
			}
			if parseErr = addTargetConstraint(table, constraint); parseErr != nil {
				return nil, parseErr
			}
		case strings.HasPrefix(upper, "KEY "):
			index, parseErr := parseTargetIndex(part, false)
			if parseErr != nil {
				return nil, fmt.Errorf("parse index %q: %w", part, parseErr)
			}
			if _, duplicate := table.indexes[index.name]; duplicate {
				return nil, fmt.Errorf("duplicate index %s.%s", table.name, index.name)
			}
			table.indexes[index.name] = index
		default:
			column, parseErr := parseTargetColumn(part, *table)
			if parseErr != nil {
				return nil, fmt.Errorf("parse column %q: %w", part, parseErr)
			}
			table.columns = append(table.columns, column)
		}
	}
	return table, nil
}

func parseTargetColumn(definition string, table targetTableContract) (targetColumnContract, error) {
	name, rest, err := targetLeadingIdentifier(strings.TrimSpace(definition))
	if err != nil {
		return targetColumnContract{}, err
	}
	columnType, rest := targetLeadingType(rest)
	if columnType == "" {
		return targetColumnContract{}, fmt.Errorf("column %s has no type", name)
	}
	column := targetColumnContract{name: name, columnType: normalizeTargetType(columnType), nullable: !containsTargetPhrase(rest, "NOT NULL")}
	if match := targetCharsetPattern.FindStringSubmatch(rest); len(match) == 2 {
		column.charset = strings.ToLower(match[1])
	}
	if match := targetCollationPattern.FindStringSubmatch(rest); len(match) == 2 {
		column.collation = strings.ToLower(match[1])
	}
	if targetCharacterType(column.columnType) {
		if column.charset == "" {
			column.charset = table.charset
		}
		if column.collation == "" {
			column.collation = table.collation
		}
	}
	column.autoIncrement = containsTargetPhrase(rest, "AUTO_INCREMENT")
	if offset := targetKeywordOffset(rest, "DEFAULT"); offset >= 0 {
		raw, _, valueErr := targetLeadingSQLValue(strings.TrimSpace(rest[offset+len("DEFAULT"):]))
		if valueErr != nil {
			return targetColumnContract{}, fmt.Errorf("column %s default: %w", name, valueErr)
		}
		column.hasDefault = true
		column.defaultValue = normalizeTargetDefault(raw, column.columnType)
	}
	if offset := targetPhraseOffset(rest, "GENERATED ALWAYS AS"); offset >= 0 {
		raw, _, valueErr := targetLeadingSQLValue(strings.TrimSpace(rest[offset+len("GENERATED ALWAYS AS"):]))
		if valueErr != nil {
			return targetColumnContract{}, fmt.Errorf("column %s generated expression: %w", name, valueErr)
		}
		column.generated = normalizeTargetExpression(raw)
	}
	return column, nil
}

func parseTargetConstraint(definition string) (targetConstraintContract, error) {
	matches := targetConstraintPrefix.FindStringSubmatch(strings.TrimSpace(definition))
	if len(matches) != 3 {
		return targetConstraintContract{}, fmt.Errorf("invalid named constraint %q", definition)
	}
	constraint := targetConstraintContract{name: matches[1]}
	rest := strings.TrimSpace(matches[2])
	upper := strings.ToUpper(rest)
	switch {
	case strings.HasPrefix(upper, "UNIQUE"):
		columns, err := targetParenthesizedIdentifiers(rest[len("UNIQUE"):])
		if err != nil {
			return targetConstraintContract{}, err
		}
		constraint.kind, constraint.columns = "UNIQUE", columns
	case strings.HasPrefix(upper, "CHECK"):
		check, _, err := targetLeadingSQLValue(strings.TrimSpace(rest[len("CHECK"):]))
		if err != nil {
			return targetConstraintContract{}, err
		}
		constraint.kind, constraint.check = "CHECK", normalizeTargetExpression(check)
	case strings.HasPrefix(upper, "FOREIGN KEY"):
		fk := targetForeignKeyPattern.FindStringSubmatch(rest)
		if len(fk) != 5 {
			return targetConstraintContract{}, fmt.Errorf("invalid foreign key %s", constraint.name)
		}
		columns, err := targetIdentifierList(fk[1])
		if err != nil {
			return targetConstraintContract{}, err
		}
		referenced, err := targetIdentifierList(fk[3])
		if err != nil {
			return targetConstraintContract{}, err
		}
		constraint.kind, constraint.columns = "FOREIGN KEY", columns
		constraint.referencedTable, constraint.referencedColumns = fk[2], referenced
		constraint.updateRule, constraint.deleteRule = "NO ACTION", "NO ACTION"
		tail := strings.ToUpper(fk[4])
		if rule := targetActionRule(tail, "ON UPDATE"); rule != "" {
			constraint.updateRule = rule
		}
		if rule := targetActionRule(tail, "ON DELETE"); rule != "" {
			constraint.deleteRule = rule
		}
	default:
		return targetConstraintContract{}, fmt.Errorf("unsupported constraint %s", constraint.name)
	}
	return constraint, nil
}

func addTargetConstraint(table *targetTableContract, constraint targetConstraintContract) error {
	if _, duplicate := table.constraints[constraint.name]; duplicate {
		return fmt.Errorf("duplicate constraint %s.%s", table.name, constraint.name)
	}
	table.constraints[constraint.name] = constraint
	if constraint.kind == "PRIMARY KEY" || constraint.kind == "UNIQUE" {
		columns := make([]targetIndexColumnContract, len(constraint.columns))
		for i, column := range constraint.columns {
			columns[i] = targetIndexColumnContract{name: column}
		}
		table.indexes[constraint.name] = targetIndexContract{name: constraint.name, unique: true, columns: columns}
	}
	return nil
}

func parseTargetIndex(definition string, unique bool) (targetIndexContract, error) {
	rest := strings.TrimSpace(definition)
	if strings.HasPrefix(strings.ToUpper(rest), "KEY") {
		rest = strings.TrimSpace(rest[len("KEY"):])
	}
	name, rest, err := targetLeadingIdentifier(rest)
	if err != nil {
		return targetIndexContract{}, err
	}
	open := strings.Index(rest, "(")
	close, err := matchingTargetParen(rest, open)
	if err != nil {
		return targetIndexContract{}, err
	}
	parts, err := splitTargetTopLevel(rest[open+1:close], ',')
	if err != nil {
		return targetIndexContract{}, err
	}
	index := targetIndexContract{name: name, unique: unique}
	for _, part := range parts {
		fields := strings.Fields(strings.ReplaceAll(strings.TrimSpace(part), "`", ""))
		if len(fields) < 1 || len(fields) > 2 || (len(fields) == 2 && !strings.EqualFold(fields[1], "DESC")) {
			return targetIndexContract{}, fmt.Errorf("unsupported index column %q", part)
		}
		index.columns = append(index.columns, targetIndexColumnContract{name: fields[0], descending: len(fields) == 2})
	}
	return index, nil
}

func targetSchemaSemanticSignatures(objects []targetSchemaObject, contract targetSchemaContract) ([]string, error) {
	actual := make([]string, 0, len(objects))
	for _, object := range objects {
		table := contract.tables[object.Table]
		if table == nil {
			continue
		}
		signature, err := targetSchemaObjectSignature(object, *table)
		if err != nil {
			return nil, fmt.Errorf("normalize MySQL target schema %s %s.%s: %w", object.Type, object.Table, object.Name, err)
		}
		actual = append(actual, signature)
	}
	sort.Strings(actual)
	return actual, nil
}

func expectedTargetSchemaSignatures(contract targetSchemaContract) []string {
	result := []string{}
	for _, table := range contract.tables {
		result = append(result, targetSignature("table", table.name, table.name, table.engine, table.collation))
		for ordinal, column := range table.columns {
			defaultValue := "<null>"
			if column.hasDefault {
				defaultValue = column.defaultValue
			}
			result = append(result, targetSignature("column", table.name, strconv.Itoa(ordinal+1), column.name, column.columnType, strconv.FormatBool(column.nullable), defaultValue, strconv.FormatBool(column.autoIncrement), column.generated, column.charset, column.collation))
		}
		columns := make(map[string]targetColumnContract, len(table.columns))
		for _, column := range table.columns {
			columns[column.name] = column
		}
		for _, index := range table.indexes {
			for ordinal, column := range index.columns {
				direction := "A"
				if column.descending {
					direction = "D"
				}
				nullable := ""
				if columns[column.name].nullable {
					nullable = "YES"
				}
				result = append(result, targetSignature("index", table.name, index.name, strconv.FormatBool(index.unique), strconv.Itoa(ordinal+1), column.name, "", direction, "", nullable, "BTREE", "YES"))
			}
		}
		for _, constraint := range table.constraints {
			result = append(result, targetSignature("constraint", table.name, constraint.name, constraint.kind, "YES"))
			for ordinal, column := range constraint.columns {
				referencedTable, referencedColumn, position := "", "", ""
				if constraint.kind == "FOREIGN KEY" {
					referencedTable, referencedColumn, position = constraint.referencedTable, constraint.referencedColumns[ordinal], strconv.Itoa(ordinal+1)
				}
				result = append(result, targetSignature("key-column", table.name, constraint.name, strconv.Itoa(ordinal+1), column, referencedTable, referencedColumn, position))
			}
			switch constraint.kind {
			case "FOREIGN KEY":
				result = append(result, targetSignature("referential", table.name, constraint.name, "PRIMARY", "NONE", constraint.updateRule, constraint.deleteRule, constraint.referencedTable))
			case "CHECK":
				result = append(result, targetSignature("check", table.name, constraint.name, constraint.check))
			}
		}
	}
	sort.Strings(result)
	return result
}

// targetSchemaObjectsForContract is a deterministic information_schema-shaped
// fixture builder. Keeping it next to the contract lets focused tests exercise
// the exact same normalization path used for a live MySQL target.
func targetSchemaObjectsForContract(contract targetSchemaContract) []targetSchemaObject {
	objects := []targetSchemaObject{}
	tableNames := make([]string, 0, len(contract.tables))
	for tableName := range contract.tables {
		tableNames = append(tableNames, tableName)
	}
	sort.Strings(tableNames)
	for _, tableName := range tableNames {
		table := contract.tables[tableName]
		objects = append(objects, targetSchemaJSON("table", tableName, tableName, table.engine, "Dynamic", table.collation, ""))
		for ordinal, column := range table.columns {
			var defaultValue any
			if column.hasDefault {
				defaultValue = strings.TrimPrefix(strings.TrimPrefix(column.defaultValue, "string:"), "value:")
				if strings.HasPrefix(column.defaultValue, "value:") && !strings.HasPrefix(defaultValue.(string), "(") && strings.Contains(defaultValue.(string), "(") {
					defaultValue = "(" + defaultValue.(string) + ")"
				}
			}
			extra := ""
			if column.autoIncrement {
				extra = "auto_increment"
			}
			if column.generated != "" {
				extra = "STORED GENERATED"
			}
			if column.hasDefault && !strings.HasPrefix(column.defaultValue, "string:") {
				extra = strings.TrimSpace(extra + " DEFAULT_GENERATED")
			}
			nullable := "NO"
			if column.nullable {
				nullable = "YES"
			}
			objects = append(objects, targetSchemaJSON("column", tableName, fmt.Sprintf("%06d.%s", ordinal+1, column.name), column.name, column.columnType, nullable, defaultValue, extra, column.generated, nullableString(column.charset), nullableString(column.collation)))
		}
		indexNames := make([]string, 0, len(table.indexes))
		for name := range table.indexes {
			indexNames = append(indexNames, name)
		}
		sort.Strings(indexNames)
		columns := make(map[string]targetColumnContract, len(table.columns))
		for _, column := range table.columns {
			columns[column.name] = column
		}
		for _, name := range indexNames {
			index := table.indexes[name]
			for ordinal, column := range index.columns {
				direction := "A"
				if column.descending {
					direction = "D"
				}
				nullable := ""
				if columns[column.name].nullable {
					nullable = "YES"
				}
				nonUnique := 1
				if index.unique {
					nonUnique = 0
				}
				objects = append(objects, targetSchemaJSON("index", tableName, fmt.Sprintf("%s.%06d", name, ordinal+1), name, nonUnique, ordinal+1, column.name, nil, direction, nil, nullable, "BTREE", "YES"))
			}
		}
		constraintNames := make([]string, 0, len(table.constraints))
		for name := range table.constraints {
			constraintNames = append(constraintNames, name)
		}
		sort.Strings(constraintNames)
		for _, name := range constraintNames {
			constraint := table.constraints[name]
			objects = append(objects, targetSchemaJSON("constraint", tableName, name, constraint.kind, "YES"))
			for ordinal, column := range constraint.columns {
				var referencedSchema, referencedTable, referencedColumn, position any
				if constraint.kind == "FOREIGN KEY" {
					referencedSchema, referencedTable = "target", constraint.referencedTable
					referencedColumn, position = constraint.referencedColumns[ordinal], ordinal+1
				}
				objects = append(objects, targetSchemaJSON("key-column", tableName, fmt.Sprintf("%s.%06d.%s", name, ordinal+1, column), name, ordinal+1, column, referencedSchema, referencedTable, referencedColumn, position))
			}
			switch constraint.kind {
			case "FOREIGN KEY":
				objects = append(objects, targetSchemaJSON("referential", tableName, name, "target", "PRIMARY", "NONE", constraint.updateRule, constraint.deleteRule, constraint.referencedTable))
			case "CHECK":
				objects = append(objects, targetSchemaJSON("check", tableName, name, constraint.check))
			}
		}
	}
	return objects
}

func targetSchemaJSON(kind, table, name string, values ...any) targetSchemaObject {
	encoded, _ := json.Marshal(values)
	return targetSchemaObject{Type: kind, Table: table, Name: name, Definition: string(encoded)}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validateTargetSchemaObjects(objects []targetSchemaObject, contract targetSchemaContract) error {
	actual, err := targetSchemaSemanticSignatures(objects, contract)
	if err != nil {
		return err
	}
	expected := expectedTargetSchemaSignatures(contract)
	if len(actual) != len(expected) {
		return fmt.Errorf("MySQL target schema has %d mapped semantic objects, expected exactly %d", len(actual), len(expected))
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return fmt.Errorf("MySQL target schema differs at semantic object %d: got %q, expected %q", i+1, actual[i], expected[i])
		}
	}
	return nil
}

func targetSchemaObjectSignature(object targetSchemaObject, table targetTableContract) (string, error) {
	var values []any
	if err := json.Unmarshal([]byte(object.Definition), &values); err != nil {
		return "", fmt.Errorf("decode definition: %w", err)
	}
	switch object.Type {
	case "table":
		if len(values) != 4 {
			return "", fmt.Errorf("table definition has %d fields", len(values))
		}
		return targetSignature("table", object.Table, object.Name, strings.ToLower(targetJSONText(values[0])), strings.ToLower(targetJSONText(values[2]))), nil
	case "column":
		if len(values) != 8 {
			return "", fmt.Errorf("column definition has %d fields", len(values))
		}
		ordinal, name, ok := strings.Cut(object.Name, ".")
		if !ok || name != targetJSONText(values[0]) {
			return "", errors.New("column identity is inconsistent")
		}
		columnType := normalizeTargetType(targetJSONText(values[1]))
		nullable := strings.EqualFold(targetJSONText(values[2]), "YES")
		defaultValue := "<null>"
		if values[3] != nil {
			defaultValue = normalizeTargetDefault(targetJSONText(values[3]), columnType)
		}
		extra := strings.ToLower(strings.TrimSpace(targetJSONText(values[4])))
		autoIncrement := strings.Contains(extra, "auto_increment")
		generated := normalizeTargetExpression(targetJSONText(values[5]))
		unsupportedExtra := strings.TrimSpace(strings.NewReplacer("auto_increment", "", "default_generated", "", "stored generated", "", "virtual generated", "").Replace(extra))
		if unsupportedExtra != "" || strings.Contains(extra, "virtual generated") {
			return "", fmt.Errorf("unsupported column extra %q", extra)
		}
		return targetSignature("column", object.Table, strings.TrimLeft(ordinal, "0"), name, columnType, strconv.FormatBool(nullable), defaultValue, strconv.FormatBool(autoIncrement), generated, strings.ToLower(targetJSONText(values[6])), strings.ToLower(targetJSONText(values[7]))), nil
	case "index":
		if len(values) != 10 {
			return "", fmt.Errorf("index definition has %d fields", len(values))
		}
		return targetSignature("index", object.Table, targetJSONText(values[0]), strconv.FormatBool(targetJSONInt(values[1]) == 0), strconv.FormatInt(targetJSONInt(values[2]), 10), targetJSONText(values[3]), normalizeTargetExpression(targetJSONText(values[4])), strings.ToUpper(targetJSONText(values[5])), targetJSONText(values[6]), strings.ToUpper(targetJSONText(values[7])), strings.ToUpper(targetJSONText(values[8])), strings.ToUpper(targetJSONText(values[9]))), nil
	case "constraint":
		if len(values) != 2 {
			return "", fmt.Errorf("constraint definition has %d fields", len(values))
		}
		return targetSignature("constraint", object.Table, object.Name, strings.ToUpper(targetJSONText(values[0])), strings.ToUpper(targetJSONText(values[1]))), nil
	case "key-column":
		if len(values) != 7 {
			return "", fmt.Errorf("key-column definition has %d fields", len(values))
		}
		return targetSignature("key-column", object.Table, targetJSONText(values[0]), strconv.FormatInt(targetJSONInt(values[1]), 10), targetJSONText(values[2]), targetJSONText(values[4]), targetJSONText(values[5]), targetJSONNumber(values[6])), nil
	case "referential":
		if len(values) != 6 {
			return "", fmt.Errorf("referential definition has %d fields", len(values))
		}
		return targetSignature("referential", object.Table, object.Name, targetJSONText(values[1]), strings.ToUpper(targetJSONText(values[2])), strings.ToUpper(targetJSONText(values[3])), strings.ToUpper(targetJSONText(values[4])), targetJSONText(values[5])), nil
	case "check":
		if len(values) != 1 {
			return "", fmt.Errorf("check definition has %d fields", len(values))
		}
		return targetSignature("check", object.Table, object.Name, normalizeTargetExpression(targetJSONText(values[0]))), nil
	default:
		return "", fmt.Errorf("unsupported schema object type %q", object.Type)
	}
}

func canonicalizeTargetSchemaObject(object targetSchemaObject) (targetSchemaObject, error) {
	var definition any
	if err := json.Unmarshal([]byte(object.Definition), &definition); err != nil {
		return targetSchemaObject{}, err
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return targetSchemaObject{}, err
	}
	object.Definition = string(encoded)
	return object, nil
}

func targetSignature(parts ...string) string { return strings.Join(parts, "\x1f") }

func targetJSONText(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

func targetJSONInt(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}

func targetJSONNumber(value any) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(targetJSONInt(value), 10)
}

func normalizeTargetType(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func normalizeTargetDefault(value, columnType string) string {
	value = strings.TrimSpace(value)
	for targetFullyParenthesized(value) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	if targetCharacterType(columnType) {
		return "string:" + value
	}
	return "value:" + normalizeTargetExpression(value)
}

func normalizeTargetExpression(value string) string {
	value = stripOuterTargetSQLParens(strings.TrimSpace(value))
	var result strings.Builder
	inQuote := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if inQuote {
			result.WriteByte(ch)
			if ch == '\'' {
				if i+1 < len(value) && value[i+1] == '\'' {
					result.WriteByte(value[i+1])
					i++
				} else {
					inQuote = false
				}
			}
			continue
		}
		if ch == '\'' {
			inQuote = true
			result.WriteByte(ch)
			continue
		}
		if ch == '`' || unicode.IsSpace(rune(ch)) {
			continue
		}
		if ch == '_' && i+1 < len(value) {
			lower := strings.ToLower(value[i:])
			for _, introducer := range []string{"_utf8mb4", "_utf8", "_ascii", "_binary"} {
				if strings.HasPrefix(lower, introducer+"'") {
					i += len(introducer) - 1
					ch = 0
					break
				}
			}
			if ch == 0 {
				continue
			}
		}
		result.WriteRune(unicode.ToLower(rune(ch)))
	}
	return removeRedundantTargetSQLParens(stripOuterTargetSQLParens(result.String()))
}

func removeRedundantTargetSQLParens(expression string) string {
	for {
		changed := false
		var result strings.Builder
		inQuote := false
		for i := 0; i < len(expression); i++ {
			ch := expression[i]
			if inQuote {
				result.WriteByte(ch)
				if ch == '\'' {
					if i+1 < len(expression) && expression[i+1] == '\'' {
						i++
						result.WriteByte(expression[i])
					} else {
						inQuote = false
					}
				}
				continue
			}
			if ch == '\'' {
				inQuote = true
				result.WriteByte(ch)
				continue
			}
			if ch == '(' && (i == 0 || !targetIdentifierByte(expression[i-1])) {
				closeAt, err := matchingTargetParen(expression, i)
				if err == nil && redundantTargetSQLParenContent(expression[i+1:closeAt]) {
					result.WriteString(expression[i+1 : closeAt])
					i = closeAt
					changed = true
					continue
				}
			}
			result.WriteByte(ch)
		}
		expression = stripOuterTargetSQLParens(result.String())
		if !changed {
			return expression
		}
	}
}

func redundantTargetSQLParenContent(content string) bool {
	if content == "" {
		return false
	}
	depth, inQuote := 0, false
	for i := 0; i < len(content); i++ {
		ch := content[i]
		if inQuote {
			if ch == '\'' {
				if i+1 < len(content) && content[i+1] == '\'' {
					i++
				} else {
					inQuote = false
				}
			}
			continue
		}
		switch ch {
		case '\'':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return false
			}
		}
	}
	return true
}

func stripOuterTargetSQLParens(expression string) string {
	for targetFullyParenthesized(expression) {
		expression = strings.TrimSpace(expression[1 : len(expression)-1])
	}
	return expression
}

func targetCharacterType(columnType string) bool {
	base := columnType
	if index := strings.IndexAny(base, "( 	"); index >= 0 {
		base = base[:index]
	}
	switch base {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
		return true
	default:
		return false
	}
}

func targetLeadingIdentifier(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", errors.New("missing identifier")
	}
	if value[0] == '`' {
		end := strings.IndexByte(value[1:], '`')
		if end < 0 {
			return "", "", errors.New("unterminated quoted identifier")
		}
		return value[1 : end+1], strings.TrimSpace(value[end+2:]), nil
	}
	end := strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
	if end < 0 {
		return value, "", nil
	}
	return value[:end], strings.TrimSpace(value[end:]), nil
}

func targetLeadingType(value string) (string, string) {
	depth := 0
	for i, ch := range value {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if unicode.IsSpace(ch) && depth == 0 {
				return value[:i], strings.TrimSpace(value[i:])
			}
		}
	}
	return value, ""
}

func targetParenthesizedIdentifiers(value string) ([]string, error) {
	open := strings.Index(value, "(")
	close, err := matchingTargetParen(value, open)
	if err != nil {
		return nil, err
	}
	return targetIdentifierList(value[open+1 : close])
}

func targetIdentifierList(value string) ([]string, error) {
	parts, err := splitTargetTopLevel(value, ',')
	if err != nil {
		return nil, err
	}
	result := make([]string, len(parts))
	for i, part := range parts {
		identifier := strings.Trim(strings.TrimSpace(part), "`")
		if identifier == "" || strings.ContainsAny(identifier, " ()") {
			return nil, fmt.Errorf("unsupported identifier %q", part)
		}
		result[i] = identifier
	}
	return result, nil
}

func targetLeadingSQLValue(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", errors.New("missing SQL value")
	}
	if value[0] == '(' {
		end, err := matchingTargetParen(value, 0)
		if err != nil {
			return "", "", err
		}
		return value[:end+1], strings.TrimSpace(value[end+1:]), nil
	}
	if value[0] == '\'' {
		for i := 1; i < len(value); i++ {
			if value[i] != '\'' {
				continue
			}
			if i+1 < len(value) && value[i+1] == '\'' {
				i++
				continue
			}
			return value[:i+1], strings.TrimSpace(value[i+1:]), nil
		}
		return "", "", errors.New("unterminated SQL string")
	}
	end := strings.IndexFunc(value, unicode.IsSpace)
	if end < 0 {
		return value, "", nil
	}
	return value[:end], strings.TrimSpace(value[end:]), nil
}

func matchingTargetParen(value string, open int) (int, error) {
	if open < 0 || open >= len(value) || value[open] != '(' {
		return -1, errors.New("missing opening parenthesis")
	}
	depth, inQuote := 0, false
	for i := open; i < len(value); i++ {
		ch := value[i]
		if inQuote {
			if ch == '\'' {
				if i+1 < len(value) && value[i+1] == '\'' {
					i++
				} else {
					inQuote = false
				}
			}
			continue
		}
		switch ch {
		case '\'':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return -1, errors.New("unterminated parenthesized SQL")
}

func splitTargetTopLevel(value string, separator byte) ([]string, error) {
	depth, inQuote := 0, false
	start := 0
	result := []string{}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if inQuote {
			if ch == '\'' {
				if i+1 < len(value) && value[i+1] == '\'' {
					i++
				} else {
					inQuote = false
				}
			}
			continue
		}
		switch ch {
		case '\'':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
		case separator:
			if depth == 0 {
				result = append(result, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
		if depth < 0 {
			return nil, errors.New("unbalanced SQL parentheses")
		}
	}
	if inQuote || depth != 0 {
		return nil, errors.New("unterminated SQL expression")
	}
	result = append(result, strings.TrimSpace(value[start:]))
	return result, nil
}

func targetFullyParenthesized(value string) bool {
	if len(value) < 2 || value[0] != '(' {
		return false
	}
	close, err := matchingTargetParen(value, 0)
	return err == nil && close == len(value)-1
}

func targetPhraseOffset(value, phrase string) int {
	return strings.Index(strings.ToUpper(value), phrase)
}

func targetKeywordOffset(value, keyword string) int {
	upper := strings.ToUpper(value)
	for offset := 0; offset < len(upper); {
		index := strings.Index(upper[offset:], keyword)
		if index < 0 {
			return -1
		}
		index += offset
		leftOK := index == 0 || !targetIdentifierByte(upper[index-1])
		right := index + len(keyword)
		rightOK := right == len(upper) || !targetIdentifierByte(upper[right])
		if leftOK && rightOK {
			return index
		}
		offset = index + len(keyword)
	}
	return -1
}

func containsTargetPhrase(value, phrase string) bool { return targetPhraseOffset(value, phrase) >= 0 }

func targetIdentifierByte(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func targetActionRule(tail, phrase string) string {
	offset := strings.Index(tail, phrase)
	if offset < 0 {
		return ""
	}
	value := strings.TrimSpace(tail[offset+len(phrase):])
	for _, rule := range []string{"NO ACTION", "SET NULL", "RESTRICT", "CASCADE"} {
		if strings.HasPrefix(value, rule) {
			return rule
		}
	}
	return ""
}
