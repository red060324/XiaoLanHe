package postgrestomysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
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
	if len(manifest) != 25 || manifest[0].Version != "001_create_user_account.sql" || manifest[len(manifest)-1].Version != "025_create_flash_sale_release_job.sql" {
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
	objects := targetSchemaObjectsForContract(contract)
	if err := validateTargetSchemaObjects(objects, contract); err != nil {
		t.Fatalf("exact schema rejected: %v", err)
	}
	for _, kind := range []string{"column", "check", "referential"} {
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
