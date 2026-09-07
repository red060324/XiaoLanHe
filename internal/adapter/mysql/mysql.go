package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

const (
	RequiredSQLMode    = "STRICT_TRANS_TABLES,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE,NO_ENGINE_SUBSTITUTION"
	RequiredTLSProfile = "xlh-verified"
	maxTLSFileBytes    = 2 << 20
)

var dnsNamePattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*\.?)$`)

type TLSOptions struct {
	CAFile, ServerName, CertificateFile, KeyFile string
}

type Options struct {
	MaxOpenConnections, MaxIdleConnections       int
	ConnectionMaxLifetime, ConnectionMaxIdleTime time.Duration
	AllowInsecure                                bool
	TLS                                          TLSOptions
	Metrics                                      *platformmetrics.Registry
}

func ParseDSN(raw string, allowInsecure bool) (*drivermysql.Config, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("invalid MySQL DSN")
	}
	cfg, err := drivermysql.ParseDSN(raw)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL DSN: %w", err)
	}
	return validateDSNConfig(cfg, allowInsecure)
}

func parseDSNWithTLS(raw string, allowInsecure bool, options TLSOptions) (*drivermysql.Config, error) {
	if allowInsecure {
		return ParseDSN(raw, true)
	}
	configured := options.CAFile != "" || options.ServerName != "" || options.CertificateFile != "" || options.KeyFile != ""
	if !configured {
		return nil, errors.New("production MySQL TLS requires CA file and server name configuration")
	}
	tlsConfig, err := loadTLSConfig(options)
	if err != nil {
		return nil, err
	}
	rewritten, err := rewriteRequiredTLSProfile(raw)
	if err != nil {
		return nil, err
	}
	cfg, err := drivermysql.ParseDSN(rewritten)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL DSN: %w", err)
	}
	// TLS takes precedence over TLSConfig in go-sql-driver/mysql. Keeping the
	// profile name records the required public DSN contract while avoiding a
	// mutable process-global TLS registry.
	cfg.TLSConfig = RequiredTLSProfile
	cfg.TLS = tlsConfig
	return validateDSNConfig(cfg, false)
}

func validateDSNConfig(cfg *drivermysql.Config, allowInsecure bool) (*drivermysql.Config, error) {
	if cfg.Net != "tcp" || cfg.Addr == "" || cfg.User == "" || cfg.DBName == "" {
		return nil, errors.New("MySQL DSN requires tcp address, user and database")
	}
	if !cfg.ParseTime || cfg.Loc == nil || cfg.Loc.String() != "UTC" {
		return nil, errors.New("MySQL DSN requires parseTime=true and loc=UTC")
	}
	if cfg.Timeout <= 0 || cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 || cfg.Timeout > time.Minute || cfg.ReadTimeout > time.Minute || cfg.WriteTimeout > time.Minute {
		return nil, errors.New("MySQL DSN requires bounded positive dial/read/write timeouts not exceeding 1m")
	}
	if cfg.MultiStatements || cfg.InterpolateParams || cfg.ClientFoundRows {
		return nil, errors.New("unsafe MySQL DSN option enabled")
	}
	if cfg.AllowFallbackToPlaintext || cfg.AllowAllFiles || cfg.AllowOldPasswords || cfg.AllowCleartextPasswords {
		return nil, errors.New("unsafe MySQL DSN compatibility option enabled")
	}
	switch cfg.TLSConfig {
	case "", "false", "skip-verify", "preferred":
		if !allowInsecure {
			return nil, errors.New("production MySQL DSN requires a registered verified TLS profile")
		}
	case "true":
		return nil, errors.New("production MySQL DSN requires a named registered TLS profile")
	}
	if !allowInsecure && (cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || cfg.TLS.ServerName == "" || cfg.TLS.MinVersion < tls.VersionTLS12) {
		return nil, errors.New("production MySQL TLS profile must verify a server name with TLS 1.2 or newer")
	}
	return cfg, nil
}

func rewriteRequiredTLSProfile(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("invalid MySQL DSN")
	}
	slash := strings.LastIndexByte(raw, '/')
	if slash < 0 {
		return "", errors.New("MySQL DSN is missing a database")
	}
	queryOffset := strings.IndexByte(raw[slash+1:], '?')
	if queryOffset < 0 {
		return "", fmt.Errorf("production MySQL DSN requires tls=%s", RequiredTLSProfile)
	}
	queryOffset += slash + 1
	values, err := url.ParseQuery(raw[queryOffset+1:])
	if err != nil {
		return "", errors.New("invalid MySQL DSN query parameters")
	}
	profiles, present := values["tls"]
	if !present || len(profiles) != 1 || profiles[0] != RequiredTLSProfile {
		return "", fmt.Errorf("production MySQL DSN requires tls=%s", RequiredTLSProfile)
	}
	values.Set("tls", "true")
	return raw[:queryOffset+1] + values.Encode(), nil
}

func loadTLSConfig(options TLSOptions) (*tls.Config, error) {
	if strings.TrimSpace(options.CAFile) != options.CAFile || options.CAFile == "" || strings.TrimSpace(options.ServerName) != options.ServerName || !validServerName(options.ServerName) {
		return nil, errors.New("MySQL TLS requires a CA file and valid server name")
	}
	if (options.CertificateFile == "") != (options.KeyFile == "") {
		return nil, errors.New("MySQL TLS client certificate and key must be configured together")
	}
	caPEM, err := readTLSFile(options.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read MySQL TLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("MySQL TLS CA file contains no valid certificate")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: options.ServerName, RootCAs: roots}
	if options.CertificateFile != "" {
		certificatePEM, err := readTLSFile(options.CertificateFile)
		if err != nil {
			return nil, fmt.Errorf("read MySQL TLS client certificate: %w", err)
		}
		keyPEM, err := readTLSFile(options.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read MySQL TLS client key: %w", err)
		}
		certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse MySQL TLS client key pair: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func readTLSFile(name string) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTLSFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxTLSFileBytes {
		return nil, errors.New("TLS file is empty or exceeds 2 MiB")
	}
	return data, nil
}

func validServerName(value string) bool {
	if strings.ContainsAny(value, "\x00\r\n /\\") {
		return false
	}
	return net.ParseIP(value) != nil || (len(value) <= 253 && dnsNamePattern.MatchString(value))
}

type initializingConnector struct{ base driver.Connector }

func (c *initializingConnector) Driver() driver.Driver { return c.base.Driver() }
func (c *initializingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if err = execDriver(ctx, conn, "SET time_zone = '+00:00'"); err == nil {
		err = execDriver(ctx, conn, "SET SESSION sql_mode = '"+RequiredSQLMode+"'")
	}
	if err == nil {
		err = verifyDriverSession(ctx, conn)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("initialize MySQL session: %w", err)
	}
	return conn, nil
}

func verifyDriverSession(ctx context.Context, conn driver.Conn) error {
	queryer, ok := conn.(driver.QueryerContext)
	if !ok {
		return errors.New("MySQL driver connection cannot verify session state")
	}
	rows, err := queryer.QueryContext(ctx, "SELECT @@session.time_zone, @@session.sql_mode", nil)
	if err != nil {
		return err
	}
	defer rows.Close()
	values := make([]driver.Value, 2)
	if err := rows.Next(values); err != nil {
		return err
	}
	timezone, okTime := driverText(values[0])
	mode, okMode := driverText(values[1])
	if !okTime || !okMode || timezone != "+00:00" || !hasModes(mode, RequiredSQLMode) {
		return errors.New("MySQL session invariant mismatch")
	}
	return nil
}

func driverText(value driver.Value) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case []byte:
		return string(value), true
	default:
		return "", false
	}
}

func execDriver(ctx context.Context, conn driver.Conn, query string) error {
	if execer, ok := conn.(driver.ExecerContext); ok {
		_, err := execer.ExecContext(ctx, query, nil)
		if !errors.Is(err, driver.ErrSkip) {
			return err
		}
	}
	stmt, err := conn.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	_, err = stmt.Exec(nil)
	return err
}

func Open(ctx context.Context, rawDSN string, options Options) (*sql.DB, error) {
	started := time.Now()
	var retErr error
	registry := options.Metrics
	if registry == nil {
		registry = platformmetrics.Default()
	}
	defer func() {
		registry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{
			Operation: "open", Outcome: mysqlOperationOutcome(retErr), Duration: time.Since(started),
		})
	}()
	cfg, err := parseDSNWithTLS(rawDSN, options.AllowInsecure, options.TLS)
	if err != nil {
		retErr = err
		return nil, retErr
	}
	if options.MaxOpenConnections <= 0 || options.MaxIdleConnections < 0 || options.MaxIdleConnections > options.MaxOpenConnections || options.ConnectionMaxLifetime <= 0 || options.ConnectionMaxIdleTime <= 0 || options.ConnectionMaxIdleTime > options.ConnectionMaxLifetime {
		retErr = errors.New("invalid MySQL pool configuration")
		return nil, retErr
	}
	// ParseDSN has already resolved a named TLS profile to cfg.TLS. Re-encoding
	// and reparsing the DSN would require process-global registration to remain
	// present and would throw away the validated clone. NewConnector preserves
	// that TLS configuration while still normalizing a private copy.
	base, err := drivermysql.NewConnector(cfg)
	if err != nil {
		retErr = fmt.Errorf("create MySQL connector: %w", err)
		return nil, retErr
	}
	db := sql.OpenDB(&initializingConnector{base: base})
	db.SetMaxOpenConns(options.MaxOpenConnections)
	db.SetMaxIdleConns(options.MaxIdleConnections)
	db.SetConnMaxLifetime(options.ConnectionMaxLifetime)
	db.SetConnMaxIdleTime(options.ConnectionMaxIdleTime)
	pingStarted := time.Now()
	if err := db.PingContext(ctx); err != nil {
		registry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{Operation: "ping", Outcome: mysqlOperationOutcome(err), Duration: time.Since(pingStarted)})
		db.Close()
		retErr = fmt.Errorf("ping MySQL: %w", err)
		return nil, retErr
	}
	registry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{Operation: "ping", Outcome: "success", Duration: time.Since(pingStarted)})
	if err := ready(ctx, db, registry); err != nil {
		db.Close()
		retErr = err
		return nil, retErr
	}
	bindMySQLPool(registry, db)
	return db, nil
}

func Ready(ctx context.Context, db *sql.DB) error {
	return ready(ctx, db, platformmetrics.Default())
}

func ready(ctx context.Context, db *sql.DB, registry *platformmetrics.Registry) (retErr error) {
	if registry == nil {
		registry = platformmetrics.Default()
	}
	started := time.Now()
	defer func() {
		registry.ObserveMySQLOperation(platformmetrics.MySQLOperationObservation{
			Operation: "ready", Outcome: mysqlOperationOutcome(retErr), Duration: time.Since(started),
		})
	}()
	if db == nil {
		return errors.New("nil MySQL database")
	}
	var version, timezone, mode, engine string
	if err := db.QueryRowContext(ctx, "SELECT VERSION(), @@session.time_zone, @@session.sql_mode, @@default_storage_engine").Scan(&version, &timezone, &mode, &engine); err != nil {
		return fmt.Errorf("query MySQL readiness: %w", err)
	}
	if !strings.HasPrefix(version, "8.4.") || timezone != "+00:00" || !strings.EqualFold(engine, "InnoDB") || !hasModes(mode, RequiredSQLMode) {
		return fmt.Errorf("unsupported MySQL readiness state: version=%q timezone=%q engine=%q sql_mode=%q", version, timezone, engine, mode)
	}
	return nil
}

func mysqlPoolObservation(db *sql.DB) platformmetrics.MySQLPoolObservation {
	if db == nil {
		return platformmetrics.MySQLPoolObservation{}
	}
	stats := db.Stats()
	return platformmetrics.MySQLPoolObservation{
		MaxOpenConnections: int64(stats.MaxOpenConnections), OpenConnections: int64(stats.OpenConnections),
		InUse: int64(stats.InUse), Idle: int64(stats.Idle), WaitCount: stats.WaitCount, WaitDuration: stats.WaitDuration,
		MaxIdleClosed: stats.MaxIdleClosed, MaxIdleTimeClosed: stats.MaxIdleTimeClosed, MaxLifetimeClosed: stats.MaxLifetimeClosed,
	}
}

func bindMySQLPool(registry *platformmetrics.Registry, db *sql.DB) {
	if registry == nil {
		registry = platformmetrics.Default()
	}
	registry.BindMySQLPool(func() platformmetrics.MySQLPoolObservation { return mysqlPoolObservation(db) })
}

func mysqlOperationOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "error"
	}
}

func hasModes(actual, required string) bool {
	set := map[string]bool{}
	for _, v := range strings.Split(actual, ",") {
		set[strings.ToUpper(strings.TrimSpace(v))] = true
	}
	for _, v := range strings.Split(required, ",") {
		if !set[v] {
			return false
		}
	}
	return true
}
