package mysql

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	migrations "github.com/red060324/XiaoLanHe/migrations/mysql"
)

func TestMySQLMigrationIntegration(t *testing.T) {
	dsn := os.Getenv("XLH_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("XLH_MYSQL_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tlsOptions := TLSOptions{CAFile: os.Getenv("XLH_DATABASE_TLS_CA_FILE"), ServerName: os.Getenv("XLH_DATABASE_TLS_SERVER_NAME")}
	secure := tlsOptions.CAFile != "" || tlsOptions.ServerName != ""
	options := Options{MaxOpenConnections: 4, MaxIdleConnections: 1, ConnectionMaxLifetime: time.Minute, ConnectionMaxIdleTime: 30 * time.Second, AllowInsecure: !secure, TLS: tlsOptions}
	db, err := Open(ctx, dsn, options)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db, migrations.Files, MigrationOptions{LockTimeout: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, migrations.Files, MigrationOptions{LockTimeout: 10 * time.Second}); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	migrationErrors := make(chan error, 2)
	for range 2 {
		go func() {
			migrationErrors <- Migrate(ctx, db, migrations.Files, MigrationOptions{LockTimeout: 10 * time.Second})
		}()
	}
	for range 2 {
		if err := <-migrationErrors; err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
	inspection, err := Inspect(ctx, db, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Pending) != 0 || len(inspection.Dirty) != 0 || len(inspection.Applied) != 25 {
		t.Fatalf("inspection=%+v", inspection)
	}
	t.Run("live schema and repository invariants", func(t *testing.T) {
		runMySQLLiveIntegrationSuite(t, ctx, db)
	})
	assertPhysicalConnectionInvariants(t, ctx, db, secure)
	if secure {
		bad := options
		bad.TLS.ServerName = "wrong-host.invalid"
		if wrong, openErr := Open(ctx, dsn, bad); openErr == nil {
			wrong.Close()
			t.Fatal("wrong TLS server name unexpectedly connected")
		}
		if untrusted := os.Getenv("XLH_MYSQL_TEST_UNTRUSTED_CA_FILE"); untrusted != "" {
			bad = options
			bad.TLS.CAFile = untrusted
			if wrong, openErr := Open(ctx, dsn, bad); openErr == nil {
				wrong.Close()
				t.Fatal("untrusted MySQL CA unexpectedly connected")
			}
		}
	}
}

func assertPhysicalConnectionInvariants(t *testing.T, ctx context.Context, db *sql.DB, secure bool) {
	t.Helper()
	connections := make([]*sql.Conn, 0, 4)
	ids := map[int64]struct{}{}
	for range 4 {
		connection, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		var id int64
		var timezone, mode string
		if err := connection.QueryRowContext(ctx, "SELECT CONNECTION_ID(), @@session.time_zone, @@session.sql_mode").Scan(&id, &timezone, &mode); err != nil {
			t.Fatal(err)
		}
		if timezone != "+00:00" || !hasModes(mode, RequiredSQLMode) {
			t.Fatalf("connection %d has timezone=%q mode=%q", id, timezone, mode)
		}
		var statusName, cipher string
		if err := connection.QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(&statusName, &cipher); err != nil {
			t.Fatal(err)
		}
		if secure && (statusName != "Ssl_cipher" || strings.TrimSpace(cipher) == "") {
			t.Fatalf("connection %d did not negotiate TLS", id)
		}
		ids[id] = struct{}{}
	}
	if len(ids) != len(connections) {
		t.Fatalf("pool growth reused physical IDs: %v", ids)
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db.SetConnMaxLifetime(time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	var timezone, mode string
	if err := db.QueryRowContext(ctx, "SELECT @@session.time_zone, @@session.sql_mode").Scan(&timezone, &mode); err != nil {
		t.Fatal(err)
	}
	if timezone != "+00:00" || !hasModes(mode, RequiredSQLMode) {
		t.Fatalf("replacement connection has timezone=%q mode=%q", timezone, mode)
	}
}
