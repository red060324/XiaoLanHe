package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/red060324/XiaoLanHe/internal/migration/postgrestomysql"
)

func TestRunAndLogEmitsFixedCompletionRecord(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runAndLog(&stderr, func(summary *cutoverRunSummary) error {
		*summary = cutoverRunSummary{Mode: string(postgrestomysql.ModeVerify), DryRun: true, CutoverReady: true}
		return nil
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	record := decodeCutoverLogRecord(t, stderr.Bytes())
	want := cutoverLogRecord{
		Event: cutoverLogEvent, Operation: cutoverLogOperation, Mode: "verify", Outcome: "success",
		ErrorClass: "none", DryRun: true, CutoverReady: true, LatencyMS: record.LatencyMS,
	}
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("record = %#v, want %#v", record, want)
	}
}

func TestRunAndLogFailureRecordsAreBoundedAndPrivate(t *testing.T) {
	tests := []struct {
		name       string
		canary     string
		err        error
		outcome    string
		errorClass string
	}{
		{name: "secret", canary: "password=secret-canary", err: postgrestomysql.ErrIdentityMismatch, outcome: "rejected", errorClass: "identity_mismatch"},
		{name: "DSN", canary: "mysql://canary-user:canary-password@private-host/canary", err: errors.New("dial failed"), outcome: "error", errorClass: "internal"},
		{name: "key", canary: "private-key-canary-ABC123", err: errors.New("verification failed"), outcome: "error", errorClass: "internal"},
		{name: "SQL", canary: "SELECT email FROM user_account", err: postgrestomysql.ErrCheckpointMismatch, outcome: "rejected", errorClass: "checkpoint_mismatch"},
		{name: "user ID and primary key", canary: "user_id=92837465 primary_key=184467", err: postgrestomysql.ErrReconciliationMismatch, outcome: "rejected", errorClass: "reconciliation_mismatch"},
		{name: "path", canary: "/private/cutover/attestation.json", err: postgrestomysql.ErrCheckpointLocked, outcome: "rejected", errorClass: "lock_unavailable"},
		{name: "hash", canary: "sha256:4b1d-canary-digest", err: errors.New("driver failure"), outcome: "error", errorClass: "internal"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rawErr := fmt.Errorf("%s: %w", test.canary, test.err)
			var stderr bytes.Buffer
			exitCode := runAndLog(&stderr, func(summary *cutoverRunSummary) error {
				*summary = cutoverRunSummary{Mode: test.canary, DryRun: false, CutoverReady: true}
				return rawErr
			})
			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1", exitCode)
			}
			if strings.Contains(stderr.String(), test.canary) || strings.Contains(stderr.String(), rawErr.Error()) {
				t.Fatalf("stderr leaked private failure data: %s", stderr.String())
			}
			record := decodeCutoverLogRecord(t, stderr.Bytes())
			want := cutoverLogRecord{
				Event: cutoverLogEvent, Operation: cutoverLogOperation, Mode: "unknown", Outcome: test.outcome,
				ErrorClass: test.errorClass, DryRun: false, CutoverReady: true, LatencyMS: record.LatencyMS,
			}
			if !reflect.DeepEqual(record, want) {
				t.Fatalf("record = %#v, want %#v", record, want)
			}
		})
	}
}

func TestRunAndLogSuppressesRawFlagErrors(t *testing.T) {
	const canary = "secret-flag-canary"
	originalArgs := os.Args
	os.Args = []string{"migrate-postgres-to-mysql", "--" + canary}
	t.Cleanup(func() { os.Args = originalArgs })

	var stderr bytes.Buffer
	if exitCode := runAndLog(&stderr, runCommand); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if strings.Contains(stderr.String(), canary) {
		t.Fatalf("stderr leaked rejected flag: %s", stderr.String())
	}
	record := decodeCutoverLogRecord(t, stderr.Bytes())
	if record.Outcome != "rejected" || record.ErrorClass != "invalid_input" || record.Mode != "inspect" {
		t.Fatalf("record = %#v", record)
	}
}

func TestParseCutoverCommandClassifiesSyntaxErrors(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantErr     bool
		wantMode    string
		wantExecute bool
	}{
		{name: "defaults", wantMode: "inspect"},
		{name: "valid options", args: []string{"--mode=copy", "--execute", "--batch-size=1"}, wantMode: "copy", wantExecute: true},
		{name: "unknown flag", args: []string{"--private-flag-canary"}, wantErr: true, wantMode: "inspect"},
		{name: "invalid integer", args: []string{"--batch-size=private-integer-canary"}, wantErr: true, wantMode: "inspect"},
		{name: "positional argument", args: []string{"private-positional-canary"}, wantErr: true, wantMode: "inspect"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseCutoverCommand(test.args)
			if test.wantErr {
				if !errors.Is(err, errInvalidCommandLine) || cutoverErrorClass(err) != "invalid_input" {
					t.Fatalf("error = %v, class = %q, want invalid command input", err, cutoverErrorClass(err))
				}
			} else if err != nil {
				t.Fatalf("parse error = %v", err)
			}
			if options.mode != test.wantMode || options.execute != test.wantExecute {
				t.Fatalf("options mode=%q execute=%t, want mode=%q execute=%t", options.mode, options.execute, test.wantMode, test.wantExecute)
			}
		})
	}
}

func TestRunCommandClassifiesOperatorInputAsRejected(t *testing.T) {
	validKey := base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	tests := []struct {
		name       string
		args       func(string) []string
		configure  func(*testing.T)
		wantMode   string
		wantDryRun bool
		canary     string
	}{
		{
			name:       "invalid mode",
			args:       func(directory string) []string { return validCommandArgs(directory, "--mode=bogus-mode-canary") },
			wantMode:   "unknown",
			wantDryRun: true,
			canary:     "bogus-mode-canary",
		},
		{
			name:       "missing checkpoint",
			args:       func(string) []string { return []string{"migrate-postgres-to-mysql", "--tool-commit=test-commit"} },
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "source-password-canary",
		},
		{
			name: "relative checkpoint",
			args: func(string) []string {
				return []string{"migrate-postgres-to-mysql", "--checkpoint-dir=private-path-canary", "--tool-commit=test-commit"}
			},
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "private-path-canary",
		},
		{
			name: "unversioned tool commit",
			args: func(directory string) []string {
				return []string{"migrate-postgres-to-mysql", "--checkpoint-dir=" + directory, "--tool-commit=unversioned"}
			},
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "source-password-canary",
		},
		{
			name: "missing DSN environment",
			args: func(directory string) []string { return validCommandArgs(directory) },
			configure: func(t *testing.T) {
				t.Setenv("XLH_DATABASE_URL", "")
			},
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "source-password-canary",
		},
		{
			name: "same source and target DSN",
			args: func(directory string) []string { return validCommandArgs(directory) },
			configure: func(t *testing.T) {
				const sameDSN = "same-dsn-canary"
				t.Setenv("XLH_LEGACY_POSTGRES_URL", sameDSN)
				t.Setenv("XLH_DATABASE_URL", sameDSN)
			},
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "same-dsn-canary",
		},
		{
			name:       "read-only mode with execute",
			args:       func(directory string) []string { return validCommandArgs(directory, "--mode=verify", "--execute") },
			wantMode:   "verify",
			wantDryRun: false,
			canary:     "source-password-canary",
		},
		{
			name:       "invalid batch size",
			args:       func(directory string) []string { return validCommandArgs(directory, "--batch-size=10001") },
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "source-password-canary",
		},
		{
			name:       "invalid source row limit",
			args:       func(directory string) []string { return validCommandArgs(directory, "--max-source-rows=0") },
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "source-password-canary",
		},
		{
			name:       "invalid source byte limit",
			args:       func(directory string) []string { return validCommandArgs(directory, "--max-source-bytes=-1") },
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "source-password-canary",
		},
		{
			name: "invalid PostgreSQL DSN",
			args: func(directory string) []string { return validCommandArgs(directory) },
			configure: func(t *testing.T) {
				t.Setenv("XLH_LEGACY_POSTGRES_URL", "postgres://source-password-canary@%zz")
			},
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "source-password-canary",
		},
		{
			name: "invalid MySQL DSN",
			args: func(directory string) []string { return validCommandArgs(directory) },
			configure: func(t *testing.T) {
				t.Setenv("XLH_DATABASE_URL", "target-password-canary")
			},
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "target-password-canary",
		},
		{
			name: "invalid database environment",
			args: func(directory string) []string { return validCommandArgs(directory) },
			configure: func(t *testing.T) {
				t.Setenv("XLH_DATABASE_MAX_OPEN_CONNECTIONS", "not-an-integer-canary")
			},
			wantMode:   "inspect",
			wantDryRun: true,
			canary:     "not-an-integer-canary",
		},
		{
			name: "invalid trust material",
			args: func(directory string) []string {
				return validCommandArgs(directory, "--mode=verify", "--source-freeze-key-id=source-key", "--target-writer-fence-key-id=target-key", "--target-deployment-generation=generation-1")
			},
			configure: func(t *testing.T) {
				t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", "invalid-key-canary")
				t.Setenv("XLH_TARGET_WRITER_FENCE_PUBLIC_KEY", validKey)
			},
			wantMode:   "verify",
			wantDryRun: true,
			canary:     "invalid-key-canary",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidCutoverCommandEnvironment(t)
			if test.configure != nil {
				test.configure(t)
			}
			originalArgs := os.Args
			os.Args = test.args(t.TempDir())
			t.Cleanup(func() { os.Args = originalArgs })

			var stderr bytes.Buffer
			if exitCode := runAndLog(&stderr, runCommand); exitCode != 1 {
				t.Fatalf("exit code = %d, want 1", exitCode)
			}
			if strings.Contains(stderr.String(), test.canary) {
				t.Fatalf("stderr leaked private operator input %q: %s", test.canary, stderr.String())
			}
			record := decodeCutoverLogRecord(t, stderr.Bytes())
			want := cutoverLogRecord{
				Event: cutoverLogEvent, Operation: cutoverLogOperation, Mode: test.wantMode, Outcome: "rejected",
				ErrorClass: "invalid_input", DryRun: test.wantDryRun, CutoverReady: false, LatencyMS: record.LatencyMS,
			}
			if !reflect.DeepEqual(record, want) {
				t.Fatalf("record = %#v, want %#v", record, want)
			}
		})
	}
}

func TestRunCommandKeepsDatabaseFailureInternalAndPrivate(t *testing.T) {
	setValidCutoverCommandEnvironment(t)
	originalArgs := os.Args
	os.Args = validCommandArgs(t.TempDir())
	t.Cleanup(func() { os.Args = originalArgs })

	var stderr bytes.Buffer
	if exitCode := runAndLog(&stderr, runCommand); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	for _, canary := range []string{"source-password-canary", "target-password-canary"} {
		if strings.Contains(stderr.String(), canary) {
			t.Fatalf("stderr leaked private DSN data %q: %s", canary, stderr.String())
		}
	}
	record := decodeCutoverLogRecord(t, stderr.Bytes())
	want := cutoverLogRecord{
		Event: cutoverLogEvent, Operation: cutoverLogOperation, Mode: "inspect", Outcome: "error",
		ErrorClass: "internal", DryRun: true, CutoverReady: false, LatencyMS: record.LatencyMS,
	}
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("record = %#v, want %#v", record, want)
	}
}

func TestRunCommandKeepsKeyFileIOFailureInternalAndPrivate(t *testing.T) {
	setValidCutoverCommandEnvironment(t)
	validKey := base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", "")
	t.Setenv("XLH_TARGET_WRITER_FENCE_PUBLIC_KEY", validKey)
	missingPath := filepath.Join(t.TempDir(), "private-key-path-canary")
	originalArgs := os.Args
	os.Args = validCommandArgs(t.TempDir(), "--mode=verify", "--source-freeze-key-id=source-key", "--source-freeze-public-key-file="+missingPath, "--target-writer-fence-key-id=target-key", "--target-deployment-generation=generation-1")
	t.Cleanup(func() { os.Args = originalArgs })

	var stderr bytes.Buffer
	if exitCode := runAndLog(&stderr, runCommand); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if strings.Contains(stderr.String(), missingPath) {
		t.Fatalf("stderr leaked private key path: %s", stderr.String())
	}
	record := decodeCutoverLogRecord(t, stderr.Bytes())
	want := cutoverLogRecord{
		Event: cutoverLogEvent, Operation: cutoverLogOperation, Mode: "verify", Outcome: "error",
		ErrorClass: "internal", DryRun: true, CutoverReady: false, LatencyMS: record.LatencyMS,
	}
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("record = %#v, want %#v", record, want)
	}
}

func TestCutoverErrorClassIsFixedVocabulary(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: nil, want: "none"},
		{err: context.Canceled, want: "cancelled"},
		{err: context.DeadlineExceeded, want: "deadline"},
		{err: postgrestomysql.ErrIdentityMismatch, want: "identity_mismatch"},
		{err: postgrestomysql.ErrCheckpointMismatch, want: "checkpoint_mismatch"},
		{err: postgrestomysql.ErrReconciliationMismatch, want: "reconciliation_mismatch"},
		{err: postgrestomysql.ErrTargetLockUnavailable, want: "lock_unavailable"},
		{err: postgrestomysql.ErrCheckpointLocked, want: "lock_unavailable"},
		{err: errInvalidCommandLine, want: "invalid_input"},
		{err: fmt.Errorf("private canary: %w", errInvalidCommandLine), want: "invalid_input"},
		{err: errors.New("unknown private error"), want: "internal"},
	}
	for _, test := range tests {
		if got := cutoverErrorClass(test.err); got != test.want {
			t.Errorf("cutoverErrorClass(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func validCommandArgs(directory string, extra ...string) []string {
	args := []string{"migrate-postgres-to-mysql", "--checkpoint-dir=" + directory, "--tool-commit=test-commit"}
	return append(args, extra...)
}

func setValidCutoverCommandEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("XLH_LEGACY_POSTGRES_URL", "postgres://source-user:source-password-canary@127.0.0.1:1/source?sslmode=disable")
	t.Setenv("XLH_DATABASE_URL", "target-user:target-password-canary@tcp(127.0.0.1:1)/target?parseTime=true&loc=UTC&timeout=1ms&readTimeout=1ms&writeTimeout=1ms")
	t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", "")
	t.Setenv("XLH_TARGET_WRITER_FENCE_PUBLIC_KEY", "")
	t.Setenv("XLH_DATABASE_MAX_OPEN_CONNECTIONS", "25")
	t.Setenv("XLH_DATABASE_MAX_IDLE_CONNECTIONS", "10")
	t.Setenv("XLH_DATABASE_CONNECTION_MAX_LIFETIME", "30m")
	t.Setenv("XLH_DATABASE_CONNECTION_MAX_IDLE_TIME", "5m")
	t.Setenv("XLH_DATABASE_MIGRATION_LOCK_TIMEOUT", "30s")
	t.Setenv("XLH_DATABASE_ALLOW_INSECURE", "true")
	t.Setenv("XLH_DATABASE_TLS_CA_FILE", "")
	t.Setenv("XLH_DATABASE_TLS_SERVER_NAME", "")
	t.Setenv("XLH_DATABASE_TLS_CERT_FILE", "")
	t.Setenv("XLH_DATABASE_TLS_KEY_FILE", "")
}

func TestCutoverOutcomeMatchesRegistryVocabulary(t *testing.T) {
	tests := map[string]string{
		"none":                    "success",
		"cancelled":               "cancelled",
		"deadline":                "deadline",
		"identity_mismatch":       "rejected",
		"checkpoint_mismatch":     "rejected",
		"reconciliation_mismatch": "rejected",
		"lock_unavailable":        "rejected",
		"invalid_input":           "rejected",
		"internal":                "error",
	}
	for errorClass, want := range tests {
		if got := cutoverOutcome(errorClass); got != want {
			t.Errorf("cutoverOutcome(%q) = %q, want %q", errorClass, got, want)
		}
	}
}

func decodeCutoverLogRecord(t *testing.T, encoded []byte) cutoverLogRecord {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode fields: %v; stderr=%q", err, encoded)
	}
	wantFields := []string{"event", "operation", "mode", "outcome", "error_class", "dry_run", "cutover_ready", "latency_ms"}
	if len(fields) != len(wantFields) {
		t.Fatalf("log fields = %v, want exactly %v", fields, wantFields)
	}
	for _, field := range wantFields {
		if _, ok := fields[field]; !ok {
			t.Errorf("log is missing field %q: %s", field, encoded)
		}
	}
	var record cutoverLogRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if record.LatencyMS < 0 {
		t.Errorf("latency_ms = %d, want non-negative", record.LatencyMS)
	}
	return record
}

func TestCommandRequiresExplicitSourceTargetAndTrustedFreezeInputs(t *testing.T) {
	contents, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"XLH_LEGACY_POSTGRES_URL", "XLH_DATABASE_URL", "--checkpoint-dir",
		"--tool-commit", `"execute"`, `"max-source-rows"`, `"max-source-bytes"`,
		`"source-freeze-attestation"`, "XLH_SOURCE_FREEZE_PUBLIC_KEY",
		`"source-freeze-public-key-file"`, `"source-freeze-key-id"`,
		`"target-writer-fence-attestation"`, "XLH_TARGET_WRITER_FENCE_PUBLIC_KEY",
		`"target-writer-fence-public-key-file"`, `"target-writer-fence-key-id"`,
		`"target-deployment-generation"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("command contract missing %s", required)
		}
	}
	if strings.Contains(text, "target-migration-commit") || strings.Contains(text, "TargetMigrationCommit") {
		t.Fatal("command must derive target migration provenance instead of accepting an operator-supplied commit")
	}
}

func TestLoadTargetWriterFenceTrustIsIndependentAndModeSpecific(t *testing.T) {
	encoded := base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	t.Setenv("XLH_TARGET_WRITER_FENCE_PUBLIC_KEY", encoded)

	for _, mode := range []postgrestomysql.Mode{postgrestomysql.ModeInspect, postgrestomysql.ModeCopy, postgrestomysql.ModeResume} {
		key, err := loadTargetWriterFenceTrust(mode, false, "", "", "", "")
		if err != nil || key != nil {
			t.Fatalf("dry-run mode=%s key=%x err=%v", mode, key, err)
		}
	}
	key, err := loadTargetWriterFenceTrust(postgrestomysql.ModeVerify, false, "", "", "target-key", "generation-1")
	if err != nil || len(key) != ed25519.PublicKeySize {
		t.Fatalf("verify key=%x err=%v", key, err)
	}
	if _, err := loadTargetWriterFenceTrust(postgrestomysql.ModeVerify, false, "/tmp/external.json", "", "target-key", "generation-1"); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("verify external attestation err=%v", err)
	}
	if _, err := loadTargetWriterFenceTrust(postgrestomysql.ModeCopy, true, "", "", "target-key", "generation-1"); err == nil || !strings.Contains(err.Error(), "--target-writer-fence-attestation") {
		t.Fatalf("execute missing attestation err=%v", err)
	}
	if _, err := loadTargetWriterFenceTrust(postgrestomysql.ModeCopy, true, "/secure/fence.json", "", "target-key", ""); err == nil || !strings.Contains(err.Error(), "target-deployment-generation") {
		t.Fatalf("missing generation err=%v", err)
	}
	if err := validateIndependentTrust(postgrestomysql.ModeVerify, false, key, "source-key", key, "target-key"); err == nil {
		t.Fatal("same source and target public key was accepted")
	}
	otherKey := append([]byte(nil), key...)
	otherKey[0]++
	if err := validateIndependentTrust(postgrestomysql.ModeCopy, true, key, "same-key-id", otherKey, "same-key-id"); err == nil {
		t.Fatal("same source and target key ID was accepted")
	}
}

func TestLoadFreezeTrustIsModeSpecific(t *testing.T) {
	encoded := base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))

	t.Run("inspect and dry-run copy need no key", func(t *testing.T) {
		t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", "")
		for _, mode := range []postgrestomysql.Mode{postgrestomysql.ModeInspect, postgrestomysql.ModeCopy, postgrestomysql.ModeResume} {
			key, err := loadFreezeTrust(mode, false, "", "", "")
			if err != nil || key != nil {
				t.Fatalf("mode=%s key=%x err=%v", mode, key, err)
			}
		}
	})

	t.Run("verify requires trusted key and key id but no external path", func(t *testing.T) {
		t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", encoded)
		key, err := loadFreezeTrust(postgrestomysql.ModeVerify, false, "", "", "freeze-key-2026-09")
		if err != nil || len(key) != ed25519.PublicKeySize {
			t.Fatalf("key=%x err=%v", key, err)
		}
		if _, err := loadFreezeTrust(postgrestomysql.ModeVerify, false, "/tmp/external.json", "", "freeze-key-2026-09"); err == nil || !strings.Contains(err.Error(), "does not accept") {
			t.Fatalf("external attestation err=%v", err)
		}
	})

	t.Run("verify rejects missing trust", func(t *testing.T) {
		t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", "")
		if _, err := loadFreezeTrust(postgrestomysql.ModeVerify, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "--mode verify requires") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("execute additionally requires external attestation path", func(t *testing.T) {
		t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", encoded)
		if _, err := loadFreezeTrust(postgrestomysql.ModeCopy, true, "", "", "freeze-key-2026-09"); err == nil || !strings.Contains(err.Error(), "--source-freeze-attestation") {
			t.Fatalf("err=%v", err)
		}
		key, err := loadFreezeTrust(postgrestomysql.ModeResume, true, "/secure/freeze.json", "", "freeze-key-2026-09")
		if err != nil || len(key) != ed25519.PublicKeySize {
			t.Fatalf("key=%x err=%v", key, err)
		}
	})

	t.Run("decoded key length is exact", func(t *testing.T) {
		t.Setenv("XLH_SOURCE_FREEZE_PUBLIC_KEY", base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize-1)))
		if _, err := loadFreezeTrust(postgrestomysql.ModeVerify, false, "", "", "key"); err == nil || !strings.Contains(err.Error(), "expected 32") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestReadSecureFreezePublicKeyFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := []byte(base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)) + "\n")
	path := filepath.Join(directory, "freeze-public-key")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := readSecureFreezePublicKeyFile(path)
	if err != nil || string(loaded) != string(contents) {
		t.Fatalf("loaded=%q err=%v", loaded, err)
	}

	t.Run("owner read only accepted", func(t *testing.T) {
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		if _, err := readSecureFreezePublicKeyFile(path); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("excess permissions rejected", func(t *testing.T) {
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := readSecureFreezePublicKeyFile(path); err == nil || !strings.Contains(err.Error(), "0400 or 0600") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("nonregular rejected", func(t *testing.T) {
		if _, err := readSecureFreezePublicKeyFile(directory); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("symlink rejected", func(t *testing.T) {
		link := filepath.Join(directory, "freeze-public-key-link")
		if err := os.Symlink(path, link); err != nil {
			if errors.Is(err, os.ErrPermission) {
				t.Skip(err)
			}
			t.Fatal(err)
		}
		if _, err := readSecureFreezePublicKeyFile(link); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("oversized rejected", func(t *testing.T) {
		large := filepath.Join(directory, "large-key")
		if err := os.WriteFile(large, make([]byte, maxFreezePublicKeyFileBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSecureFreezePublicKeyFile(large); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestValidateFreezePublicKeyFileStatRejectsWrongOwner(t *testing.T) {
	stat := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: uint32(os.Geteuid()) + 1}
	if err := validateFreezePublicKeyFileStat(&stat); err == nil || !strings.Contains(err.Error(), "owned by effective uid") {
		t.Fatalf("err=%v", err)
	}
}

func TestCommandDoesNotContainAutomaticSwitchOrDestructiveSQL(t *testing.T) {
	contents, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(string(contents))
	for _, forbidden := range []string{"DROP TABLE", "TRUNCATE TABLE", "DELETE FROM", "FOREIGN_KEY_CHECKS", "DUAL WRITE"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("operator command contains forbidden automatic/destructive behavior %s", forbidden)
		}
	}
}
