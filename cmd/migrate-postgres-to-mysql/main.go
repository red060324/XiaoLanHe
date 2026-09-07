package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"

	mysqladapter "github.com/red060324/XiaoLanHe/internal/adapter/mysql"
	"github.com/red060324/XiaoLanHe/internal/config"
	"github.com/red060324/XiaoLanHe/internal/migration/postgrestomysql"
)

var toolCommit = "unversioned"

const (
	maxFreezePublicKeyFileBytes = 4096
	cutoverLogEvent             = "mysql.cutover"
	cutoverLogOperation         = "postgres_to_mysql"
)

var errInvalidCommandLine = errors.New("invalid command-line arguments")

type operatorInputError struct {
	cause error
}

func (e *operatorInputError) Error() string { return e.cause.Error() }
func (e *operatorInputError) Unwrap() error { return e.cause }
func (e *operatorInputError) Is(target error) bool {
	return target == errInvalidCommandLine
}

func invalidOperatorInput(err error) error {
	if err == nil || errors.Is(err, errInvalidCommandLine) {
		return err
	}
	return &operatorInputError{cause: err}
}

type cutoverCommandOptions struct {
	mode                           string
	execute                        bool
	checkpointDirectory            string
	operatorToolCommit             string
	freezeAttestationPath          string
	freezePublicKeyFile            string
	freezeKeyID                    string
	targetWriterFencePath          string
	targetWriterFencePublicKeyFile string
	targetWriterFenceKeyID         string
	targetDeploymentGeneration     string
	batchSize                      int
	maxSourceRows                  int64
	maxSourceBytes                 int64
}

type cutoverRunSummary struct {
	Mode         string
	DryRun       bool
	CutoverReady bool
}

type cutoverLogRecord struct {
	Event        string `json:"event"`
	Operation    string `json:"operation"`
	Mode         string `json:"mode"`
	Outcome      string `json:"outcome"`
	ErrorClass   string `json:"error_class"`
	DryRun       bool   `json:"dry_run"`
	CutoverReady bool   `json:"cutover_ready"`
	LatencyMS    int64  `json:"latency_ms"`
}

func main() {
	os.Exit(runAndLog(os.Stderr, runCommand))
}

func run() error {
	return runCommand(nil)
}

func runAndLog(stderr io.Writer, command func(*cutoverRunSummary) error) int {
	started := time.Now()
	summary := cutoverRunSummary{Mode: "unknown", DryRun: true}
	err := command(&summary)
	logErr := writeCutoverLog(stderr, summary, err, time.Since(started))
	if err != nil || logErr != nil {
		return 1
	}
	return 0
}

func writeCutoverLog(output io.Writer, summary cutoverRunSummary, runErr error, elapsed time.Duration) error {
	errorClass := cutoverErrorClass(runErr)
	latencyMillis := elapsed.Milliseconds()
	if latencyMillis < 0 {
		latencyMillis = 0
	}
	record := cutoverLogRecord{
		Event:        cutoverLogEvent,
		Operation:    cutoverLogOperation,
		Mode:         boundedCutoverMode(summary.Mode),
		Outcome:      cutoverOutcome(errorClass),
		ErrorClass:   errorClass,
		DryRun:       summary.DryRun,
		CutoverReady: summary.CutoverReady,
		LatencyMS:    latencyMillis,
	}
	return json.NewEncoder(output).Encode(record)
}

func cutoverOutcome(errorClass string) string {
	switch errorClass {
	case "none":
		return "success"
	case "cancelled", "deadline":
		return errorClass
	case "identity_mismatch", "checkpoint_mismatch", "reconciliation_mismatch", "lock_unavailable", "invalid_input":
		return "rejected"
	default:
		return "error"
	}
}

func boundedCutoverMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch postgrestomysql.Mode(mode) {
	case postgrestomysql.ModeInspect, postgrestomysql.ModeCopy, postgrestomysql.ModeResume, postgrestomysql.ModeVerify:
		return mode
	default:
		return "unknown"
	}
}

func cutoverErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, postgrestomysql.ErrIdentityMismatch):
		return "identity_mismatch"
	case errors.Is(err, postgrestomysql.ErrCheckpointMismatch):
		return "checkpoint_mismatch"
	case errors.Is(err, postgrestomysql.ErrReconciliationMismatch):
		return "reconciliation_mismatch"
	case errors.Is(err, postgrestomysql.ErrTargetLockUnavailable), errors.Is(err, postgrestomysql.ErrCheckpointLocked):
		return "lock_unavailable"
	case errors.Is(err, errInvalidCommandLine):
		return "invalid_input"
	default:
		return "internal"
	}
}

func runCommand(summary *cutoverRunSummary) error {
	if summary == nil {
		summary = &cutoverRunSummary{}
	}
	*summary = cutoverRunSummary{Mode: "unknown", DryRun: true}

	options, parseErr := parseCutoverCommand(os.Args[1:])
	summary.Mode = boundedCutoverMode(options.mode)
	summary.DryRun = !options.execute
	if parseErr != nil {
		return parseErr
	}

	modeValue := postgrestomysql.Mode(strings.ToLower(strings.TrimSpace(options.mode)))
	if !modeValue.Valid() {
		return invalidOperatorInput(fmt.Errorf("invalid mode %q", options.mode))
	}
	if options.execute && modeValue != postgrestomysql.ModeCopy && modeValue != postgrestomysql.ModeResume {
		return invalidOperatorInput(errors.New("only copy and resume accept --execute"))
	}
	if options.batchSize < 1 || options.batchSize > 10_000 {
		return invalidOperatorInput(errors.New("--batch-size must be between 1 and 10000"))
	}
	if options.maxSourceRows < 1 || options.maxSourceBytes < 1 {
		return invalidOperatorInput(errors.New("--max-source-rows and --max-source-bytes must be positive"))
	}

	sourceURL := strings.TrimSpace(os.Getenv("XLH_LEGACY_POSTGRES_URL"))
	targetURL := strings.TrimSpace(os.Getenv("XLH_DATABASE_URL"))
	if sourceURL == "" || targetURL == "" {
		return invalidOperatorInput(errors.New("XLH_LEGACY_POSTGRES_URL and XLH_DATABASE_URL are both required"))
	}
	if sourceURL == targetURL {
		return invalidOperatorInput(errors.New("source and target URLs must be different"))
	}
	if options.checkpointDirectory == "" || !filepath.IsAbs(options.checkpointDirectory) || filepath.Clean(options.checkpointDirectory) != options.checkpointDirectory {
		return invalidOperatorInput(errors.New("--checkpoint-dir must be an absolute normalized path"))
	}
	trimmedToolCommit := strings.TrimSpace(options.operatorToolCommit)
	if trimmedToolCommit == "" || trimmedToolCommit == "unversioned" || trimmedToolCommit != options.operatorToolCommit {
		return invalidOperatorInput(errors.New("an exact --tool-commit is required"))
	}

	freezePublicKey, err := loadFreezeTrust(modeValue, options.execute, options.freezeAttestationPath, options.freezePublicKeyFile, options.freezeKeyID)
	if err != nil {
		return err
	}
	targetWriterFencePublicKey, err := loadTargetWriterFenceTrust(modeValue, options.execute, options.targetWriterFencePath, options.targetWriterFencePublicKeyFile, options.targetWriterFenceKeyID, options.targetDeploymentGeneration)
	if err != nil {
		return err
	}
	if err := validateIndependentTrust(modeValue, options.execute, freezePublicKey, options.freezeKeyID, targetWriterFencePublicKey, options.targetWriterFenceKeyID); err != nil {
		return err
	}

	sourceConfig, err := pgxpool.ParseConfig(sourceURL)
	if err != nil {
		return invalidOperatorInput(fmt.Errorf("parse XLH_LEGACY_POSTGRES_URL: %w", err))
	}
	databaseConfig, err := config.LoadDatabaseConfig()
	if err != nil {
		return invalidOperatorInput(err)
	}
	if err := validateTargetDatabaseURL(targetURL, databaseConfig.AllowInsecure); err != nil {
		return invalidOperatorInput(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sourceConfig.MaxConns = 1
	sourceConfig.MinConns = 0
	sourceConfig.MaxConnLifetime = 30 * time.Minute
	source, err := pgxpool.NewWithConfig(ctx, sourceConfig)
	if err != nil {
		return fmt.Errorf("connect PostgreSQL source: %w", err)
	}
	defer source.Close()

	target, err := mysqladapter.Open(ctx, targetURL, mysqladapter.Options{
		MaxOpenConnections: databaseConfig.MaxOpenConnections, MaxIdleConnections: databaseConfig.MaxIdleConnections,
		ConnectionMaxLifetime: databaseConfig.ConnectionMaxLifetime, ConnectionMaxIdleTime: databaseConfig.ConnectionMaxIdleTime,
		AllowInsecure: databaseConfig.AllowInsecure, TLS: mysqladapter.TLSOptions{
			CAFile: databaseConfig.TLSCAFile, ServerName: databaseConfig.TLSServerName,
			CertificateFile: databaseConfig.TLSCertificateFile, KeyFile: databaseConfig.TLSKeyFile,
		},
	})
	if err != nil {
		return fmt.Errorf("connect MySQL target: %w", err)
	}
	defer target.Close()

	runner, err := postgrestomysql.New(postgrestomysql.Config{
		Source: source, Target: target, CheckpointDirectory: options.checkpointDirectory,
		ToolCommit: options.operatorToolCommit,
	})
	if err != nil {
		return err
	}
	defer runner.Close()
	report, runErr := runner.Run(ctx, postgrestomysql.Options{
		Mode: modeValue, Execute: options.execute, BatchSize: options.batchSize, MaxSourceRows: options.maxSourceRows, MaxSourceBytes: options.maxSourceBytes,
		FreezeAttestationPath: options.freezeAttestationPath, FreezePublicKey: freezePublicKey, FreezeKeyID: options.freezeKeyID,
		TargetWriterFencePath: options.targetWriterFencePath, TargetWriterFencePublicKey: targetWriterFencePublicKey,
		TargetWriterFenceKeyID: options.targetWriterFenceKeyID, TargetDeploymentGeneration: options.targetDeploymentGeneration,
	})
	summary.Mode = boundedCutoverMode(string(report.Mode))
	summary.DryRun = report.DryRun
	summary.CutoverReady = report.CutoverReady
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return runErr
}

func parseCutoverCommand(args []string) (cutoverCommandOptions, error) {
	options := cutoverCommandOptions{}
	flags := flag.NewFlagSet("migrate-postgres-to-mysql", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.mode, "mode", string(postgrestomysql.ModeInspect), "inspect, copy, resume, or verify")
	flags.BoolVar(&options.execute, "execute", false, "permit MySQL writes for copy/resume; default is dry run")
	flags.IntVar(&options.batchSize, "batch-size", 500, "rows committed per target transaction (1-10000)")
	flags.Int64Var(&options.maxSourceRows, "max-source-rows", 5_000_000, "hard in-memory source row limit")
	flags.Int64Var(&options.maxSourceBytes, "max-source-bytes", 1<<30, "hard in-memory canonical source byte limit")
	flags.StringVar(&options.checkpointDirectory, "checkpoint-dir", "", "absolute private directory for immutable manifest/checkpoints/reports")
	flags.StringVar(&options.operatorToolCommit, "tool-commit", toolCommit, "exact source commit used to build this migration tool")
	flags.StringVar(&options.freezeAttestationPath, "source-freeze-attestation", "", "signed write-freeze attestation from the external freeze orchestrator; required only by --execute")
	flags.StringVar(&options.freezePublicKeyFile, "source-freeze-public-key-file", "", "owner-only regular non-symlink file containing the base64 Ed25519 verifier key; required by --execute and verify unless the environment key is set")
	flags.StringVar(&options.freezeKeyID, "source-freeze-key-id", "", "trusted key identifier required by --execute and verify")
	flags.StringVar(&options.targetWriterFencePath, "target-writer-fence-attestation", "", "signed MySQL writer-fence attestation from the external deployment controller; required only by --execute")
	flags.StringVar(&options.targetWriterFencePublicKeyFile, "target-writer-fence-public-key-file", "", "owner-only regular non-symlink file containing the independent base64 Ed25519 target-fence verifier key; required by --execute and verify unless the environment key is set")
	flags.StringVar(&options.targetWriterFenceKeyID, "target-writer-fence-key-id", "", "independently trusted target-fence key identifier required by --execute and verify")
	flags.StringVar(&options.targetDeploymentGeneration, "target-deployment-generation", "", "out-of-band expected target deployment generation required by --execute and verify")
	if err := flags.Parse(args); err != nil {
		return options, invalidOperatorInput(err)
	}
	if flags.NArg() != 0 {
		return options, invalidOperatorInput(errors.New("positional arguments are not accepted"))
	}
	return options, nil
}

func validateTargetDatabaseURL(raw string, allowInsecure bool) error {
	candidate := raw
	if !allowInsecure {
		var err error
		candidate, err = targetDatabaseURLForValidation(raw)
		if err != nil {
			return err
		}
	}
	if _, err := mysqladapter.ParseDSN(candidate, true); err != nil {
		return fmt.Errorf("validate XLH_DATABASE_URL: %w", err)
	}
	return nil
}

func targetDatabaseURLForValidation(raw string) (string, error) {
	slash := strings.LastIndexByte(raw, '/')
	if slash < 0 {
		return "", errors.New("XLH_DATABASE_URL is missing a database")
	}
	queryOffset := strings.IndexByte(raw[slash+1:], '?')
	if queryOffset < 0 {
		return "", fmt.Errorf("XLH_DATABASE_URL requires tls=%s", mysqladapter.RequiredTLSProfile)
	}
	queryOffset += slash + 1
	values, err := url.ParseQuery(raw[queryOffset+1:])
	if err != nil {
		return "", errors.New("XLH_DATABASE_URL has invalid query parameters")
	}
	profiles, present := values["tls"]
	if !present || len(profiles) != 1 || profiles[0] != mysqladapter.RequiredTLSProfile {
		return "", fmt.Errorf("XLH_DATABASE_URL requires tls=%s", mysqladapter.RequiredTLSProfile)
	}
	values.Set("tls", "false")
	return raw[:queryOffset+1] + values.Encode(), nil
}

func loadFreezeTrust(mode postgrestomysql.Mode, execute bool, attestationPath, publicKeyFile, keyID string) ([]byte, error) {
	if !execute && mode != postgrestomysql.ModeVerify {
		return nil, nil
	}
	if mode == postgrestomysql.ModeVerify && strings.TrimSpace(attestationPath) != "" {
		return nil, invalidOperatorInput(errors.New("--mode verify does not accept --source-freeze-attestation; it verifies the manifest-embedded evidence"))
	}
	requirement := "--execute"
	if mode == postgrestomysql.ModeVerify && !execute {
		requirement = "--mode verify"
	}
	encodedKey := strings.TrimSpace(os.Getenv("XLH_SOURCE_FREEZE_PUBLIC_KEY"))
	publicKeyFile = strings.TrimSpace(publicKeyFile)
	if encodedKey != "" && publicKeyFile != "" {
		return nil, invalidOperatorInput(errors.New("provide the source-freeze public key by environment or file, not both"))
	}
	if publicKeyFile != "" {
		keyBytes, err := readSecureFreezePublicKeyFile(publicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read source-freeze public key: %w", err)
		}
		encodedKey = strings.TrimSpace(string(keyBytes))
	}
	if encodedKey == "" || strings.TrimSpace(keyID) == "" {
		return nil, invalidOperatorInput(fmt.Errorf("%s requires --source-freeze-key-id and XLH_SOURCE_FREEZE_PUBLIC_KEY or --source-freeze-public-key-file", requirement))
	}
	if execute && strings.TrimSpace(attestationPath) == "" {
		return nil, invalidOperatorInput(errors.New("--execute requires --source-freeze-attestation"))
	}
	decodedKey, err := base64.RawStdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, invalidOperatorInput(fmt.Errorf("decode source-freeze public key: %w", err))
	}
	if len(decodedKey) != ed25519.PublicKeySize {
		return nil, invalidOperatorInput(fmt.Errorf("source-freeze public key has %d bytes, expected %d", len(decodedKey), ed25519.PublicKeySize))
	}
	return decodedKey, nil
}

func loadTargetWriterFenceTrust(mode postgrestomysql.Mode, execute bool, attestationPath, publicKeyFile, keyID, generation string) ([]byte, error) {
	if !execute && mode != postgrestomysql.ModeVerify {
		return nil, nil
	}
	if mode == postgrestomysql.ModeVerify && strings.TrimSpace(attestationPath) != "" {
		return nil, invalidOperatorInput(errors.New("--mode verify does not accept --target-writer-fence-attestation; it verifies the manifest-embedded evidence"))
	}
	requirement := "--execute"
	if mode == postgrestomysql.ModeVerify && !execute {
		requirement = "--mode verify"
	}
	encodedKey := strings.TrimSpace(os.Getenv("XLH_TARGET_WRITER_FENCE_PUBLIC_KEY"))
	publicKeyFile = strings.TrimSpace(publicKeyFile)
	if encodedKey != "" && publicKeyFile != "" {
		return nil, invalidOperatorInput(errors.New("provide the target-writer-fence public key by environment or file, not both"))
	}
	if publicKeyFile != "" {
		keyBytes, err := readSecureTargetWriterFencePublicKeyFile(publicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read target-writer-fence public key: %w", err)
		}
		encodedKey = strings.TrimSpace(string(keyBytes))
	}
	if encodedKey == "" || strings.TrimSpace(keyID) == "" || strings.TrimSpace(generation) == "" {
		return nil, invalidOperatorInput(fmt.Errorf("%s requires --target-writer-fence-key-id, --target-deployment-generation and XLH_TARGET_WRITER_FENCE_PUBLIC_KEY or --target-writer-fence-public-key-file", requirement))
	}
	if execute && strings.TrimSpace(attestationPath) == "" {
		return nil, invalidOperatorInput(errors.New("--execute requires --target-writer-fence-attestation"))
	}
	decodedKey, err := base64.RawStdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, invalidOperatorInput(fmt.Errorf("decode target-writer-fence public key: %w", err))
	}
	if len(decodedKey) != ed25519.PublicKeySize {
		return nil, invalidOperatorInput(fmt.Errorf("target-writer-fence public key has %d bytes, expected %d", len(decodedKey), ed25519.PublicKeySize))
	}
	return decodedKey, nil
}

func validateIndependentTrust(mode postgrestomysql.Mode, execute bool, sourceKey []byte, sourceKeyID string, targetKey []byte, targetKeyID string) error {
	if !execute && mode != postgrestomysql.ModeVerify {
		return nil
	}
	if strings.TrimSpace(sourceKeyID) == strings.TrimSpace(targetKeyID) || bytes.Equal(sourceKey, targetKey) {
		return invalidOperatorInput(errors.New("source-freeze and target-writer-fence keys and key IDs must be independent"))
	}
	return nil
}

// readSecureFreezePublicKeyFile validates and reads one already-open inode.
// O_NOFOLLOW rejects a final symlink, and both security checks use Fstat on the
// same descriptor that is read, so replacing the path cannot swap in key bytes.
func readSecureFreezePublicKeyFile(path string) (_ []byte, returnErr error) {
	return readSecurePublicKeyFile(path, "source-freeze")
}

func readSecureTargetWriterFencePublicKeyFile(path string) (_ []byte, returnErr error) {
	return readSecurePublicKeyFile(path, "target-writer-fence")
}

func readSecurePublicKeyFile(path, label string) (_ []byte, returnErr error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, invalidOperatorInput(errors.New("public key file must be a real non-symlink regular file"))
		}
		return nil, fmt.Errorf("open owner-only regular non-symlink file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close %s public key file: %w", label, err)
		}
	}()

	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, fmt.Errorf("inspect %s public key file: %w", label, err)
	}
	if err := validatePublicKeyFileStat(&before, label); err != nil {
		return nil, err
	}
	if before.Size < 1 || before.Size > maxFreezePublicKeyFileBytes {
		return nil, invalidOperatorInput(fmt.Errorf("%s public key file size %d is outside 1..%d bytes", label, before.Size, maxFreezePublicKeyFileBytes))
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxFreezePublicKeyFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s public key file: %w", label, err)
	}
	if len(contents) > maxFreezePublicKeyFileBytes {
		return nil, invalidOperatorInput(fmt.Errorf("%s public key file exceeds %d bytes", label, maxFreezePublicKeyFileBytes))
	}

	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, fmt.Errorf("reinspect %s public key file: %w", label, err)
	}
	if err := validatePublicKeyFileStat(&after, label); err != nil {
		return nil, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Size != after.Size || int64(len(contents)) != before.Size {
		return nil, fmt.Errorf("%s public key file changed while it was read", label)
	}
	return contents, nil
}

func validateFreezePublicKeyFileStat(stat *unix.Stat_t) error {
	return validatePublicKeyFileStat(stat, "source-freeze")
}

func validatePublicKeyFileStat(stat *unix.Stat_t, label string) error {
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFREG {
		return invalidOperatorInput(fmt.Errorf("%s public key file must be a real non-symlink regular file", label))
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return invalidOperatorInput(fmt.Errorf("%s public key file must be owned by effective uid %d", label, os.Geteuid()))
	}
	permissions := mode & 0o7777
	if permissions&0o400 == 0 || permissions&^uint32(0o600) != 0 {
		return invalidOperatorInput(fmt.Errorf("%s public key file permissions %04o must be 0400 or 0600", label, permissions))
	}
	return nil
}
