package postgrestomysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestExpectedTargetMigrationManifestMatchesEmbeddedHistory(t *testing.T) {
	manifest, err := expectedTargetMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 27 || manifest[0].Version != "001_create_user_account.sql" || manifest[len(manifest)-2].Version != "026_add_flash_sale_release_claimable_at.sql" || manifest[len(manifest)-1].Version != "027_add_flash_sale_release_claimable_index.sql" {
		t.Fatalf("unexpected embedded migration manifest: first=%+v last=%+v count=%d", manifest[0], manifest[len(manifest)-1], len(manifest))
	}
	for _, item := range manifest {
		if len(item.Checksum) != 64 {
			t.Fatalf("migration %s checksum = %q", item.Version, item.Checksum)
		}
		if _, err := hex.DecodeString(item.Checksum); err != nil {
			t.Fatalf("migration %s checksum: %v", item.Version, err)
		}
	}

	for name, mutate := range map[string]func([]targetMigrationIdentity) []targetMigrationIdentity{
		"missing": func(items []targetMigrationIdentity) []targetMigrationIdentity { return items[:len(items)-1] },
		"extra": func(items []targetMigrationIdentity) []targetMigrationIdentity {
			return append(items, targetMigrationIdentity{Version: "999_extra.sql"})
		},
		"name": func(items []targetMigrationIdentity) []targetMigrationIdentity {
			items[0].Name = "changed"
			return items
		},
		"checksum": func(items []targetMigrationIdentity) []targetMigrationIdentity {
			items[0].Checksum = strings.Repeat("0", 64)
			return items
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyOfManifest := append([]targetMigrationIdentity(nil), manifest...)
			if err := validateTargetMigrations(mutate(copyOfManifest), manifest); err == nil {
				t.Fatal("migration history drift was accepted")
			}
		})
	}
}

func TestTargetSchemaContractIsExactAndDetectsSemanticDrift(t *testing.T) {
	widget := tinyTable()
	contract, err := expectedTargetSchemaContract([]tableSpec{widget})
	if err == nil {
		t.Fatal("ad-hoc table without an embedded migration was accepted")
	}

	contract, err = expectedTargetSchemaContract(tables())
	if err != nil {
		t.Fatal(err)
	}
	releaseJob := contract.tables["flash_sale_release_job"]
	if releaseJob == nil {
		t.Fatal("flash_sale_release_job contract is missing")
	}
	var claimable *targetColumnContract
	for i := range releaseJob.columns {
		if releaseJob.columns[i].name == "claimable_at" {
			claimable = &releaseJob.columns[i]
			break
		}
	}
	wantGenerated := normalizeTargetExpression("IF(status='pending',next_attempt_at,IF(status='leased',lease_until,NULL))")
	if claimable == nil || claimable.columnType != "datetime(6)" || !claimable.nullable || claimable.generated != wantGenerated || !claimable.storedGenerated {
		t.Fatalf("unexpected claimable_at contract: %+v", claimable)
	}
	claimableIndex, ok := releaseJob.indexes["idx_flash_sale_release_job_claimable"]
	if !ok || claimableIndex.unique || len(claimableIndex.columns) != 2 || claimableIndex.columns[0].name != "claimable_at" || claimableIndex.columns[0].descending || claimableIndex.columns[1].name != "id" || claimableIndex.columns[1].descending {
		t.Fatalf("unexpected claimable index contract: %+v", claimableIndex)
	}
	objects := targetSchemaObjectsForContract(contract)
	if err := validateTargetSchemaObjects(objects, contract); err != nil {
		t.Fatalf("exact schema rejected: %v", err)
	}
	for _, kind := range []string{"column", "index", "check", "referential"} {
		t.Run(kind, func(t *testing.T) {
			drifted := append([]targetSchemaObject(nil), objects...)
			for i := range drifted {
				if drifted[i].Type == kind {
					drifted[i].Definition = strings.Replace(drifted[i].Definition, "[", `["tampered",`, 1)
					break
				}
			}
			if err := validateTargetSchemaObjects(drifted, contract); err == nil {
				t.Fatalf("%s drift was accepted", kind)
			}
		})
	}
}

func TestTargetSchemaGeneratedExpressionAcceptsMySQL84IntroducedLiterals(t *testing.T) {
	contract, err := expectedTargetSchemaContract(tables())
	if err != nil {
		t.Fatal(err)
	}
	want := expectedTargetSchemaSignatures(contract)
	for _, expression := range []string{
		"if((`status` = _utf8mb4'pending'),`next_attempt_at`,if((`status` = _utf8mb4'leased'),`lease_until`,NULL))",
		"if((`status` = _utf8mb4\\'pending\\'),`next_attempt_at`,if((`status` = _UTF8MB4\\'leased\\'),`lease_until`,NULL))",
	} {
		objects := targetSchemaObjectsForContract(contract)
		setTargetClaimableExpression(t, objects, expression)
		got, err := targetSchemaSemanticSignatures(objects, contract)
		if err != nil {
			t.Fatalf("normalize live generated expression %q: %v", expression, err)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("introduced literal changed schema identity for %q", expression)
		}
	}
}

func TestTargetSchemaGeneratedExpressionRejectsMalformedIntroducedLiterals(t *testing.T) {
	contract, err := expectedTargetSchemaContract(tables())
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		`if(status=_utf8mb4evil\'pending\',next_attempt_at,NULL)`,
		`if(status=_utf8mb4_0900_ai_ci\'pending\',next_attempt_at,NULL)`,
		`if(status=_latin1\'pending\',next_attempt_at,NULL)`,
		`if(status=_utf8mb4 \'pending\',next_attempt_at,NULL)`,
		`if(status=_utf8mb4\'pending,next_attempt_at,NULL)`,
	} {
		objects := targetSchemaObjectsForContract(contract)
		setTargetClaimableExpression(t, objects, expression)
		if _, err := targetSchemaSemanticSignatures(objects, contract); err == nil {
			t.Fatalf("malformed introduced literal %q was accepted", expression)
		}
	}
}

func setTargetClaimableExpression(t *testing.T, objects []targetSchemaObject, expression string) {
	t.Helper()
	for i := range objects {
		if objects[i].Type != "column" || objects[i].Table != "flash_sale_release_job" || !strings.HasSuffix(objects[i].Name, ".claimable_at") {
			continue
		}
		var definition []any
		if err := json.Unmarshal([]byte(objects[i].Definition), &definition); err != nil {
			t.Fatal(err)
		}
		definition[5] = expression
		encoded, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		objects[i].Definition = string(encoded)
		return
	}
	t.Fatal("claimable_at live schema object is missing")
}

func TestTargetRunLockUsesCoordinatesAndDedicatedConnection(t *testing.T) {
	state := &targetIdentityFakeState{instance: "server-a", database: "database-a", getLock: sql.NullInt64{Valid: true, Int64: 1}, releaseLock: sql.NullInt64{Valid: true, Int64: 1}}
	db := sql.OpenDB(targetIdentityFakeConnector{state: state})
	defer db.Close()
	lock, coordinates, err := acquireTargetRunLock(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if coordinates.InstanceID != state.instance || coordinates.DatabaseID != state.database {
		t.Fatalf("coordinates=%+v", coordinates)
	}
	wantName, _ := targetRunLockName(coordinates)
	if state.lockName != wantName || len(state.connections) != 1 || state.connections[0].closed {
		t.Fatalf("lock=%q connections=%+v", state.lockName, state.connections)
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !state.released {
		t.Fatalf("released=%t", state.released)
	}

	state = &targetIdentityFakeState{instance: "server-a", database: "database-a", getLock: sql.NullInt64{Valid: true, Int64: 0}}
	db = sql.OpenDB(targetIdentityFakeConnector{state: state})
	defer db.Close()
	if lock, _, err := acquireTargetRunLock(context.Background(), db); lock != nil || !errors.Is(err, ErrTargetLockUnavailable) {
		t.Fatalf("lock=%v err=%v", lock, err)
	}
}

func TestTargetSnapshotUsesRepeatableReadReadOnlyDedicatedConnection(t *testing.T) {
	state := &targetIdentityFakeState{}
	db := sql.OpenDB(targetIdentityFakeConnector{state: state})
	defer db.Close()
	snapshot, err := beginTargetSnapshot(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.connections) != 1 || state.beginOptions.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !state.beginOptions.ReadOnly {
		t.Fatalf("connections=%d options=%+v", len(state.connections), state.beginOptions)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !state.rolledBack {
		t.Fatalf("rolledBack=%t", state.rolledBack)
	}
}

type targetIdentityFakeState struct {
	mu                           sync.Mutex
	instance, database, lockName string
	getLock, releaseLock         sql.NullInt64
	connections                  []*targetIdentityFakeConn
	beginOptions                 driver.TxOptions
	released, rolledBack         bool
}

type targetIdentityFakeConnector struct{ state *targetIdentityFakeState }

func (c targetIdentityFakeConnector) Connect(context.Context) (driver.Conn, error) {
	connection := &targetIdentityFakeConn{state: c.state}
	c.state.mu.Lock()
	c.state.connections = append(c.state.connections, connection)
	c.state.mu.Unlock()
	return connection, nil
}
func (targetIdentityFakeConnector) Driver() driver.Driver { return targetIdentityFakeDriver{} }

type targetIdentityFakeDriver struct{}

func (targetIdentityFakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type targetIdentityFakeConn struct {
	state  *targetIdentityFakeState
	closed bool
}

func (*targetIdentityFakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *targetIdentityFakeConn) Close() error                      { c.closed = true; return nil }
func (c *targetIdentityFakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *targetIdentityFakeConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.state.beginOptions = options
	return &targetIdentityFakeTx{state: c.state}, nil
}
func (c *targetIdentityFakeConn) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	switch query {
	case `SELECT @@server_uuid,DATABASE()`:
		return &cutoverRows{columns: []string{"server_uuid", "database"}, rows: [][]driver.Value{{c.state.instance, c.state.database}}}, nil
	case `SELECT GET_LOCK(?,0)`:
		c.state.lockName = fmt.Sprint(arguments[0].Value)
		if !c.state.getLock.Valid {
			return &cutoverRows{columns: []string{"lock"}, rows: [][]driver.Value{{nil}}}, nil
		}
		return &cutoverRows{columns: []string{"lock"}, rows: [][]driver.Value{{c.state.getLock.Int64}}}, nil
	case `SELECT RELEASE_LOCK(?)`:
		c.state.released = true
		if !c.state.releaseLock.Valid {
			return &cutoverRows{columns: []string{"lock"}, rows: [][]driver.Value{{nil}}}, nil
		}
		return &cutoverRows{columns: []string{"lock"}, rows: [][]driver.Value{{c.state.releaseLock.Int64}}}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

type targetIdentityFakeTx struct{ state *targetIdentityFakeState }

func (*targetIdentityFakeTx) Commit() error      { return errors.New("read-only snapshot must not commit") }
func (tx *targetIdentityFakeTx) Rollback() error { tx.state.rolledBack = true; return nil }

var _ driver.ConnBeginTx = (*targetIdentityFakeConn)(nil)
var _ driver.QueryerContext = (*targetIdentityFakeConn)(nil)
