package mysql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"database/sql/driver"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

func TestParseDSN(t *testing.T) {
	valid := "user:pass@tcp(localhost:3306)/xiaolanhe?parseTime=true&loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s"
	tests := map[string]string{
		"no parse time":    "user:pass@tcp(localhost:3306)/db?loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s",
		"local timezone":   "user:pass@tcp(localhost:3306)/db?parseTime=true&loc=Local&timeout=5s&readTimeout=5s&writeTimeout=5s",
		"missing timeout":  "user:pass@tcp(localhost:3306)/db?parseTime=true&loc=UTC",
		"multi statements": valid + "&multiStatements=true",
		"interpolation":    valid + "&interpolateParams=true",
		"found rows":       valid + "&clientFoundRows=true",
		"no tls":           "user:pass@tcp(localhost:3306)/db?parseTime=true&loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s",
	}
	for name, dsn := range tests {
		t.Run(name, func(t *testing.T) {
			if name == "no tls" {
				return
			}
			if _, err := ParseDSN(dsn, true); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if cfg, err := ParseDSN(valid, true); err != nil || cfg.DBName != "xiaolanhe" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	if _, err := ParseDSN(tests["no tls"], false); err == nil {
		t.Fatal("production DSN without TLS must fail")
	}
	if _, err := ParseDSN(tests["no tls"], true); err != nil {
		t.Fatalf("local insecure DSN: %v", err)
	}
}

func TestParseDSNWithProductionTLS(t *testing.T) {
	caFile := writeTestCA(t)
	raw := "user:pass@tcp(mysql.internal.example:3306)/xiaolanhe?parseTime=true&loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s&tls=xlh-verified"
	cfg, err := parseDSNWithTLS(raw, false, TLSOptions{CAFile: caFile, ServerName: "mysql.internal.example"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSConfig != RequiredTLSProfile || cfg.TLS == nil || cfg.TLS.ServerName != "mysql.internal.example" || cfg.TLS.MinVersion != tls.VersionTLS12 || cfg.TLS.RootCAs == nil || cfg.TLS.InsecureSkipVerify {
		t.Fatalf("TLS config=%+v", cfg.TLS)
	}
	if _, err := parseDSNWithTLS(strings.Replace(raw, "tls=xlh-verified", "tls=true", 1), false, TLSOptions{CAFile: caFile, ServerName: "mysql.internal.example"}); err == nil {
		t.Fatal("expected wrong TLS profile rejection")
	}
	if _, err := parseDSNWithTLS(raw, false, TLSOptions{CAFile: caFile, ServerName: "bad host/name"}); err == nil {
		t.Fatal("expected invalid server name rejection")
	}
	if _, err := parseDSNWithTLS(raw, false, TLSOptions{CAFile: caFile, ServerName: "mysql.internal.example", CertificateFile: "only-cert.pem"}); err == nil {
		t.Fatal("expected incomplete client key pair rejection")
	}
}

func TestProductionTLSFailsClosed(t *testing.T) {
	raw := "user:pass@tcp(mysql.internal.example:3306)/xiaolanhe?parseTime=true&loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s&tls=xlh-verified"
	invalidCA := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string]TLSOptions{
		"missing settings": {},
		"invalid CA":       {CAFile: invalidCA, ServerName: "mysql.internal.example"},
		"missing key":      {CAFile: writeTestCA(t), ServerName: "mysql.internal.example", CertificateFile: invalidCA},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDSNWithTLS(raw, false, options); err == nil {
				t.Fatal("expected TLS configuration rejection")
			}
		})
	}
	for _, profile := range []string{"", "false", "true", "preferred", "skip-verify", "other-profile"} {
		t.Run("profile "+profile, func(t *testing.T) {
			candidate := strings.Replace(raw, "tls=xlh-verified", "tls="+profile, 1)
			if _, err := parseDSNWithTLS(candidate, false, TLSOptions{CAFile: writeTestCA(t), ServerName: "mysql.internal.example"}); err == nil {
				t.Fatal("expected TLS profile rejection")
			}
		})
	}
}

func TestProductionTLSIsPrivateAndConcurrent(t *testing.T) {
	if err := drivermysql.RegisterTLSConfig(RequiredTLSProfile, &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "sentinel.invalid"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { drivermysql.DeregisterTLSConfig(RequiredTLSProfile) })
	raw := "user:pass@tcp(mysql.internal.example:3306)/xiaolanhe?parseTime=true&loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s&tls=xlh-verified"
	caFile := writeTestCA(t)
	serverNames := []string{"mysql-a.internal.example", "mysql-b.internal.example", "127.0.0.1"}
	errorsByIndex := make([]error, len(serverNames))
	configs := make([]*drivermysql.Config, len(serverNames))
	var wait sync.WaitGroup
	for index, serverName := range serverNames {
		wait.Add(1)
		go func(index int, serverName string) {
			defer wait.Done()
			configs[index], errorsByIndex[index] = parseDSNWithTLS(raw, false, TLSOptions{CAFile: caFile, ServerName: serverName})
		}(index, serverName)
	}
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil || configs[index].TLS.ServerName != serverNames[index] {
			t.Fatalf("config[%d]=%+v err=%v", index, configs[index], err)
		}
	}
	sentinel, err := drivermysql.ParseDSN(raw)
	if err != nil || sentinel.TLS.ServerName != "sentinel.invalid" || sentinel.TLS.MinVersion != tls.VersionTLS13 {
		t.Fatalf("global TLS profile changed: config=%+v err=%v", sentinel.TLS, err)
	}
}

func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "XiaoLanHe Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(name, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestInitializingConnector(t *testing.T) {
	state := &fakeState{}
	c := &initializingConnector{base: fakeConnector{state: state}}
	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if len(state.queries) != 2 || state.queries[0] != "SET time_zone = '+00:00'" || state.queries[1] != "SET SESSION sql_mode = '"+RequiredSQLMode+"'" {
		t.Fatalf("queries=%v", state.queries)
	}
}

func TestDriverText(t *testing.T) {
	for _, input := range []driver.Value{"+00:00", []byte(RequiredSQLMode)} {
		if value, ok := driverText(input); !ok || value == "" {
			t.Fatalf("value=%q ok=%t", value, ok)
		}
	}
	if _, ok := driverText(int64(1)); ok {
		t.Fatal("numeric session value must be rejected")
	}
}

func TestMigrationLockResults(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		get, release         *int64
		wantGet, wantRelease bool
	}{{"success", ptr(1), ptr(1), false, false}, {"timeout", ptr(0), ptr(1), true, false}, {"get null", nil, ptr(1), true, false}, {"release zero", ptr(1), ptr(0), false, true}, {"release null", ptr(1), nil, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			db := sql.OpenDB(fakeConnector{state: &fakeState{get: tc.get, release: tc.release}})
			defer db.Close()
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if (acquireLock(context.Background(), c, 1) == nil) == tc.wantGet {
				t.Fatalf("get success mismatch")
			}
			if (releaseLock(context.Background(), c) == nil) == tc.wantRelease {
				t.Fatalf("release success mismatch")
			}
		})
	}
}

func TestReadyObservesFixedOutcomesWithoutLeakingErrors(t *testing.T) {
	registry := platformmetrics.NewRegistry()
	if err := ready(context.Background(), nil, registry); err == nil {
		t.Fatal("nil database must fail")
	}
	output := string(registry.Prometheus())
	if !strings.Contains(output, `xiaolanhe_mysql_operations_total{operation="ready",outcome="error"} 1`) ||
		!strings.Contains(output, `xiaolanhe_mysql_operation_duration_seconds_count{operation="ready",outcome="error"} 1`) {
		t.Fatalf("unexpected readiness metrics:\n%s", output)
	}
}

func TestMySQLOperationOutcomeIsFixedAndPrivate(t *testing.T) {
	const canary = "mysql://user:secret@private/db SELECT token_hash WHERE user_id=42"
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "success"},
		{fmt.Errorf("%s: %w", canary, context.Canceled), "cancelled"},
		{fmt.Errorf("%s: %w", canary, context.DeadlineExceeded), "deadline"},
		{errors.New(canary), "error"},
	} {
		if got := mysqlOperationOutcome(test.err); got != test.want || strings.Contains(got, canary) {
			t.Fatalf("outcome=%q want=%q", got, test.want)
		}
	}
}

func TestMySQLPoolObservationUsesDatabaseStatsSnapshot(t *testing.T) {
	db := sql.OpenDB(fakeConnector{state: &fakeState{}})
	defer db.Close()
	db.SetMaxOpenConns(7)
	db.SetMaxIdleConns(3)
	observation := mysqlPoolObservation(db)
	if observation.MaxOpenConnections != 7 || observation.OpenConnections != 0 || observation.InUse != 0 || observation.Idle != 0 {
		t.Fatalf("pool observation=%+v", observation)
	}
	registry := platformmetrics.NewRegistry()
	bindMySQLPool(registry, db)
	output := string(registry.Prometheus())
	if !strings.Contains(output, `xiaolanhe_mysql_pool_max_open_connections 7`) || !strings.Contains(output, `xiaolanhe_mysql_pool_open_connections 0`) {
		t.Fatalf("pool metrics:\n%s", output)
	}
}

func TestOpenObservesOneFixedFailureWithoutLeakingDSN(t *testing.T) {
	const canary = "secret-canary-user"
	registry := platformmetrics.NewRegistry()
	_, err := Open(context.Background(), canary+":password@tcp(host:3306)/db", Options{Metrics: registry})
	if err == nil {
		t.Fatal("expected invalid DSN failure")
	}
	output := string(registry.Prometheus())
	if strings.Contains(output, canary) || !strings.Contains(output, `xiaolanhe_mysql_operations_total{operation="open",outcome="error"} 1`) || !strings.Contains(output, `xiaolanhe_mysql_operation_duration_seconds_count{operation="open",outcome="error"} 1`) {
		t.Fatalf("unexpected open metrics:\n%s", output)
	}
}

type fakeState struct {
	queries      []string
	get, release *int64
}
type fakeConnector struct{ state *fakeState }

func (f fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{state: f.state}, nil
}
func (f fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type fakeConn struct{ state *fakeState }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.queries = append(c.state.queries, q)
	return driver.RowsAffected(1), nil
}
func (c *fakeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if q == "SELECT @@session.time_zone, @@session.sql_mode" {
		return &fakeRows{values: []driver.Value{"+00:00", RequiredSQLMode}}, nil
	}
	var value *int64
	if q == `SELECT GET_LOCK(?,?)` {
		value = c.state.get
	} else if q == `SELECT RELEASE_LOCK(?)` {
		value = c.state.release
	} else {
		return nil, errors.New("unexpected query")
	}
	var row driver.Value
	if value != nil {
		row = *value
	}
	return &fakeRows{values: []driver.Value{row}}, nil
}

type fakeRows struct {
	done   bool
	values []driver.Value
}

func (r *fakeRows) Columns() []string {
	result := make([]string, len(r.values))
	for i := range result {
		result[i] = "value"
	}
	return result
}
func (*fakeRows) Close() error { return nil }
func (r *fakeRows) Next(v []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(v, r.values)
	return nil
}
func ptr(v int64) *int64 { return &v }
