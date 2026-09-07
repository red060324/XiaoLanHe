package postgrestomysql

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

func TestRunnerInspectAndCopyDryRunNeverWrite(t *testing.T) {
	for _, mode := range []Mode{ModeInspect, ModeCopy} {
		t.Run(string(mode), func(t *testing.T) {
			harness := newRunnerHarness(t, runnerTable(), [][]any{{int64(1), int64(11)}})
			report, err := harness.runner.Run(context.Background(), harness.options(mode, false))
			if err != nil {
				t.Fatal(err)
			}
			if !report.DryRun || harness.target.mutations != 0 {
				t.Fatalf("report=%+v mutations=%d", report, harness.target.mutations)
			}
			if _, err := os.Stat(filepath.Join(harness.directory, manifestFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dry run created manifest: %v", err)
			}
		})
	}
}

func TestMySQLCutoverObservationUsesFinalReportTotals(t *testing.T) {
	report := RunReport{
		Mode: ModeVerify, SourceRows: 99, CutoverReady: false,
		SourceChecks: []CheckResult{{Match: false}, {Match: true}},
		Reconciliation: &ReconciliationReport{
			DeferredForeignKeys: DeferredComplete,
			Tables:              []TableResult{{SourceRows: 2, TargetRows: 2}, {SourceRows: 3, TargetRows: 1}},
			MismatchCount:       4,
		},
	}
	observation := mysqlCutoverObservation(report, ErrReconciliationMismatch, 2*time.Second)
	if observation.Operation != "verify" || observation.Outcome != "rejected" || observation.Duration != 2*time.Second || !observation.HasReconciliation || observation.SourceRows != 5 || observation.TargetRows != 3 || observation.MismatchCount != 4 || observation.Ready {
		t.Fatalf("observation=%+v", observation)
	}

	withoutReconciliation := mysqlCutoverObservation(RunReport{Mode: ModeInspect, SourceRows: 9, SourceChecks: report.SourceChecks}, errors.New("private SQL and user-id canary"), time.Second)
	if withoutReconciliation.HasReconciliation || withoutReconciliation.SourceRows != 0 || withoutReconciliation.TargetRows != 0 || withoutReconciliation.MismatchCount != 0 || withoutReconciliation.Outcome != "error" {
		t.Fatalf("fallback observation=%+v", withoutReconciliation)
	}

	sourcePreflight := reconciliationFromSourceChecks("sha256:identity", report.SourceChecks)
	preflightObservation := mysqlCutoverObservation(RunReport{Mode: ModeCopy, Reconciliation: &sourcePreflight}, ErrReconciliationMismatch, time.Second)
	if preflightObservation.HasReconciliation {
		t.Fatalf("source-only preflight was marked as a complete reconciliation: %+v", preflightObservation)
	}
}

func TestMySQLCutoverOutcomeIsFixed(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "success"},
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "deadline"},
		{ErrIdentityMismatch, "rejected"},
		{ErrCheckpointMismatch, "rejected"},
		{ErrReconciliationMismatch, "rejected"},
		{ErrTargetLockUnavailable, "rejected"},
		{ErrCheckpointLocked, "rejected"},
		{errors.New("mysql://user:secret@host/db SELECT * FROM users WHERE id=42"), "error"},
	} {
		if got := mysqlCutoverOutcome(test.err); got != test.want {
			t.Errorf("mysqlCutoverOutcome(%v)=%q want=%q", test.err, got, test.want)
		}
	}
}

func TestRunnerObservesExactlyOneBoundedTerminalOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		registry := platformmetrics.NewRegistry()
		harness := newRunnerHarness(t, runnerTable(), [][]any{{int64(1), int64(11)}})
		harness.runner.metrics = registry
		_, err := harness.runner.Run(context.Background(), harness.options(ModeInspect, false))
		if err != nil {
			t.Fatal(err)
		}
		output := string(registry.Prometheus())
		for _, expected := range []string{
			`xiaolanhe_mysql_cutover_runs_total{operation="inspect",outcome="success"} 1`,
			`xiaolanhe_mysql_cutover_run_duration_seconds_count{operation="inspect",outcome="success"} 1`,
		} {
			if !strings.Contains(output, expected) {
				t.Fatalf("missing %q in metrics:\n%s", expected, output)
			}
		}
		for _, absent := range []string{
			`xiaolanhe_mysql_cutover_last_reconciliation_rows{`,
			"\nxiaolanhe_mysql_cutover_last_reconciliation_mismatches ",
			"\nxiaolanhe_mysql_cutover_last_reconciliation_ready ",
		} {
			if strings.Contains(output, absent) {
				t.Fatalf("inspect published reconciliation sample %q:\n%s", absent, output)
			}
		}
	})

	t.Run("private invalid mode", func(t *testing.T) {
		const canary = "mysql://admin:secret@private/db SELECT user_id=4815162342"
		registry := platformmetrics.NewRegistry()
		runner := &Runner{metrics: registry}
		if _, err := runner.Run(context.Background(), Options{Mode: Mode(canary)}); err == nil {
			t.Fatal("expected invalid mode error")
		}
		output := string(registry.Prometheus())
		if strings.Contains(output, canary) || !strings.Contains(output, `xiaolanhe_mysql_cutover_runs_total{operation="unknown",outcome="error"} 1`) || !strings.Contains(output, `xiaolanhe_mysql_cutover_run_duration_seconds_count{operation="unknown",outcome="error"} 1`) {
			t.Fatalf("unexpected metrics:\n%s", output)
		}
		for _, absent := range []string{
			`xiaolanhe_mysql_cutover_last_reconciliation_rows{`,
			"\nxiaolanhe_mysql_cutover_last_reconciliation_mismatches ",
			"\nxiaolanhe_mysql_cutover_last_reconciliation_ready ",
		} {
			if strings.Contains(output, absent) {
				t.Fatalf("early error published reconciliation sample %q:\n%s", absent, output)
			}
		}
	})
}

func TestRunnerResumeReplaysCommittedRowsAndCompletesAllPhases(t *testing.T) {
	table := runnerTable()
	raw := [][]any{{int64(1), int64(11)}, {int64(2), int64(12)}, {int64(3), int64(13)}}
	harness := newRunnerHarness(t, table, raw)
	sourceRows, inventory, sourceIdentity := materializeRunnerSource(t, table, raw)
	targetIdentity, err := inspectTargetIdentityForTables(context.Background(), harness.runner.target.db, targetCoordinates{}, tables())
	if err != nil {
		t.Fatal(err)
	}
	identity := CutoverIdentity{SchemaVersion: ManifestSchemaVersion, ToolVersion: ToolVersion, ToolCommit: harness.runner.toolCommit, Source: sourceIdentity, Target: targetIdentity}
	identitySHA, err := identityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	manifest := newManifest(identity, identitySHA, inventory, []tableSpec{table})
	bindRunnerEvidence(t, harness, &manifest)
	initial := table.initialRows(sourceRows)
	checkpoint, err := makeTableCheckpoint(identitySHA, table, sourceRows, initial, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Checkpoints[table.name] = []TableCheckpoint{checkpoint}
	if err := harness.runner.artifacts.saveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	// Resume may replace the manifest binding with a freshly issued fence for
	// the same target identity and deployment generation before its first write.
	writeRunnerTargetFenceAttestation(t, harness.targetFencePath, harness.targetFencePrivateKey, harness.targetIdentity, "test-target-fence-resume")

	// Row 1 models a deferred update committed before its checkpoint. Row 2
	// models an initial insert committed before its checkpoint. Resume must
	// accept both exact states, write only row 3, and reconstruct checkpoints.
	harness.target.rows["1"] = cloneRow(sourceRows[0])
	harness.target.rows["2"] = cloneRow(initial[1])
	report, err := harness.runner.Run(context.Background(), harness.options(ModeResume, true))
	if err != nil {
		t.Fatal(err)
	}
	if !report.CutoverReady || harness.target.inserts != 1 || harness.target.deferredUpdates != 3 {
		t.Fatalf("ready=%t inserts=%d deferred=%d report=%+v", report.CutoverReady, harness.target.inserts, harness.target.deferredUpdates, report)
	}
	if harness.target.autoIncrement != 4 {
		t.Fatalf("AUTO_INCREMENT=%d", harness.target.autoIncrement)
	}
	if harness.target.targetLocks != 1 || harness.target.targetUnlocks != 1 || harness.target.readOnlySnapshots < 2 {
		t.Fatalf("target lock/snapshot lifecycle: locks=%d unlocks=%d readOnlySnapshots=%d", harness.target.targetLocks, harness.target.targetUnlocks, harness.target.readOnlySnapshots)
	}
	loaded, err := harness.runner.artifacts.loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Completed || loaded.DeferredForeignKeys != DeferredComplete || !loaded.AutoIncrementCompleted[table.name] || completedRows(loaded.Checkpoints[table.name]) != 3 || completedDeferredRows(loaded.DeferredCheckpoints[table.name]) != 3 {
		t.Fatalf("manifest did not complete all resumable phases: %+v", loaded)
	}
	if loaded.ReconciliationSHA256 == "" {
		t.Fatal("completed manifest did not bind its immutable reconciliation report")
	}
	if loaded.TargetWriterFence.Attestation.AttestationID != "test-target-fence-resume" {
		t.Fatalf("resume did not bind freshly issued target fence: %+v", loaded.TargetWriterFence)
	}
}

func TestRunnerResumeRejectsSourceInventoryDriftBeforeWrite(t *testing.T) {
	table := runnerTable()
	original := [][]any{{int64(1), int64(11)}}
	harness := newRunnerHarness(t, table, [][]any{{int64(1), int64(99)}})
	_, inventory, sourceIdentity := materializeRunnerSource(t, table, original)
	targetIdentity, err := inspectTargetIdentityForTables(context.Background(), harness.runner.target.db, targetCoordinates{}, tables())
	if err != nil {
		t.Fatal(err)
	}
	identity := CutoverIdentity{SchemaVersion: ManifestSchemaVersion, ToolVersion: ToolVersion, ToolCommit: harness.runner.toolCommit, Source: sourceIdentity, Target: targetIdentity}
	identitySHA, _ := identityDigest(identity)
	manifest := newManifest(identity, identitySHA, inventory, []tableSpec{table})
	bindRunnerEvidence(t, harness, &manifest)
	if err := harness.runner.artifacts.saveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.runner.Run(context.Background(), harness.options(ModeResume, true)); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("drift err=%v", err)
	}
	if harness.target.mutations != 0 {
		t.Fatalf("source drift changed target %d times", harness.target.mutations)
	}
}

func TestRunnerPreflightAndReconciliationMismatchBlockCutover(t *testing.T) {
	table := runnerTable()
	raw := [][]any{{int64(1), int64(11)}}
	t.Run("source preflight", func(t *testing.T) {
		harness := newRunnerHarness(t, table, raw)
		harness.sourceCounts["FROM user_session c LEFT JOIN"] = 1
		report, err := harness.runner.Run(context.Background(), harness.options(ModeCopy, true))
		if !errors.Is(err, ErrReconciliationMismatch) || report.Message == "" || report.Reconciliation == nil {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		failedSourceChecks := invalidChecks(report.SourceChecks)
		if failedSourceChecks != 1 || report.Reconciliation.MismatchCount != failedSourceChecks || report.Reconciliation.CutoverReady || report.Reconciliation.DeferredForeignKeys != DeferredPending {
			t.Fatalf("preflight totals=%+v", report.Reconciliation)
		}
		if len(report.Reconciliation.Tables) != 0 || len(report.Reconciliation.Metrics) != 0 || len(report.Reconciliation.Checks) != len(report.SourceChecks) {
			t.Fatalf("source-only preflight schema=%+v", report.Reconciliation)
		}
		if err := validateReportDigest(*report.Reconciliation); err != nil {
			t.Fatalf("source-only preflight digest: %v", err)
		}
		if harness.target.mutations != 0 {
			t.Fatalf("preflight failure changed target %d times", harness.target.mutations)
		}
		if _, err := os.Stat(filepath.Join(harness.directory, manifestFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preflight failure created manifest: %v", err)
		}
	})
	t.Run("target reconciliation", func(t *testing.T) {
		harness := newRunnerHarness(t, table, raw)
		harness.target.queryCounts["FROM user_session c LEFT JOIN"] = 1
		report, err := harness.runner.Run(context.Background(), harness.options(ModeCopy, true))
		if !errors.Is(err, ErrReconciliationMismatch) || report.Reconciliation == nil || report.Reconciliation.CutoverReady {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		matches, err := filepath.Glob(filepath.Join(harness.directory, "reconciliation-*.json"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("reconciliation artifacts=%v err=%v", matches, err)
		}
		loaded, err := harness.runner.artifacts.loadManifest()
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Completed || loaded.DeferredForeignKeys != DeferredComplete {
			t.Fatalf("mismatch must block completion after deferred phase: %+v", loaded)
		}
	})
}

func TestRunnerExecuteRequiresValidFreezeAndFinalSourceRecheck(t *testing.T) {
	table := runnerTable()
	raw := [][]any{{int64(1), int64(11)}}
	t.Run("missing attestation", func(t *testing.T) {
		harness := newRunnerHarness(t, table, raw)
		options := harness.options(ModeCopy, true)
		options.FreezeAttestationPath = ""
		if _, err := harness.runner.Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "attestation") {
			t.Fatalf("err=%v", err)
		}
		if harness.target.mutations != 0 {
			t.Fatalf("missing attestation changed target %d times", harness.target.mutations)
		}
	})
	t.Run("source changes before finalization", func(t *testing.T) {
		harness := newRunnerHarness(t, table, raw)
		calls := 0
		harness.runner.openSource = func(context.Context) (*sourceSnapshot, error) {
			calls++
			values := raw
			if calls > 1 {
				values = [][]any{{int64(1), int64(99)}}
			}
			identity := testIdentity().Source
			identity.SnapshotID = ""
			return &sourceSnapshot{identity: identity, tx: &cutoverFakeSourceTx{queryRows: map[string][][]any{table.name: values}, defaultRow: []any{int64(0)}}}, nil
		}
		report, err := harness.runner.Run(context.Background(), harness.options(ModeCopy, true))
		if err == nil || report.CutoverReady || !strings.Contains(report.Message, "changed during copy") {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})
}

func TestRunnerTargetWriterFenceRevalidatesBeforeWritesAndCompletion(t *testing.T) {
	table := runnerTable()
	raw := [][]any{{int64(1), int64(11)}, {int64(2), int64(12)}}

	t.Run("missing target fence fails before write", func(t *testing.T) {
		harness := newRunnerHarness(t, table, raw)
		options := harness.options(ModeCopy, true)
		options.TargetWriterFencePath = ""
		if _, err := harness.runner.Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "target-writer-fence") {
			t.Fatalf("err=%v", err)
		}
		if harness.target.mutations != 0 {
			t.Fatalf("missing target fence changed target %d times", harness.target.mutations)
		}
	})

	t.Run("fence replacement between batches stops the next write", func(t *testing.T) {
		harness := newRunnerHarness(t, table, raw)
		harness.target.afterMutation = func(count int) {
			if count == 1 {
				writeRunnerTargetFenceAttestation(t, harness.targetFencePath, harness.targetFencePrivateKey, harness.targetIdentity, "replacement-fence")
			}
		}
		report, err := harness.runner.Run(context.Background(), harness.options(ModeCopy, true))
		if err == nil || report.CutoverReady || !strings.Contains(report.Message, "before copied batch") {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		if harness.target.mutations != 1 {
			t.Fatalf("expected exactly one committed mutation before fence replacement, got %d", harness.target.mutations)
		}
	})

	for _, publication := range []struct {
		name          string
		identityCheck int
		message       string
	}{
		{name: "successful report", identityCheck: 7, message: "successful reconciliation publication"},
		{name: "completed manifest", identityCheck: 8, message: "Completed/CutoverReady publication"},
	} {
		t.Run("fence replacement before "+publication.name+" blocks completion", func(t *testing.T) {
			harness := newRunnerHarness(t, table, [][]any{{int64(1), int64(11)}})
			// Harness setup, target-lock coordinates and initial inspection consume
			// the first three checks; copied/deferred/auto-increment guards consume
			// checks four through six. Checks seven and eight guard the two success
			// publication points independently.
			harness.target.replaceFenceAtIdentityCheck = publication.identityCheck
			harness.target.replaceFence = func() {
				writeRunnerTargetFenceAttestation(t, harness.targetFencePath, harness.targetFencePrivateKey, harness.targetIdentity, "replacement-before-publication")
			}
			report, err := harness.runner.Run(context.Background(), harness.options(ModeCopy, true))
			if err == nil || report.CutoverReady || !strings.Contains(report.Message, publication.message) {
				t.Fatalf("report=%+v err=%v identityChecks=%d", report, err, harness.target.identityChecks)
			}
			manifest, loadErr := harness.runner.artifacts.loadManifest()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if manifest.Completed || manifest.ReconciliationSHA256 != "" {
				t.Fatalf("publication fence failure marked manifest complete: %+v", manifest)
			}
		})
	}
}

func TestRunnerVerifyIsStrictlyReadOnly(t *testing.T) {
	table := runnerTable()
	raw := [][]any{{int64(1), int64(11)}}
	harness := newRunnerHarness(t, table, raw)
	sourceRows, inventory, sourceIdentity := materializeRunnerSource(t, table, raw)
	targetIdentity, err := inspectTargetIdentityForTables(context.Background(), harness.runner.target.db, targetCoordinates{}, tables())
	if err != nil {
		t.Fatal(err)
	}
	identity := CutoverIdentity{SchemaVersion: ManifestSchemaVersion, ToolVersion: ToolVersion, ToolCommit: harness.runner.toolCommit, Source: sourceIdentity, Target: targetIdentity}
	identitySHA, _ := identityDigest(identity)
	manifest := newManifest(identity, identitySHA, inventory, []tableSpec{table})
	checkpoint, _ := makeTableCheckpoint(identitySHA, table, sourceRows, table.initialRows(sourceRows), 0, 1)
	checkpoint.DeferredForeignKeyState = DeferredComplete
	manifest.Checkpoints[table.name] = []TableCheckpoint{checkpoint}
	deferredCheckpoint, _ := makeDeferredCheckpoint(table, sourceRows, 0, 1)
	manifest.DeferredCheckpoints[table.name] = []DeferredCheckpoint{deferredCheckpoint}
	manifest.DeferredForeignKeys = DeferredComplete
	manifest.AutoIncrementCompleted[table.name] = true
	bindRunnerEvidence(t, harness, &manifest)
	stored := successfulRunnerReconciliation(identitySHA, table, inventory[table.name])
	manifest.ReconciliationSHA256 = stored.DigestSHA256
	manifest.Completed = true
	lock, err := harness.runner.artifacts.acquireExclusiveLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := harness.runner.artifacts.saveReport(stored); err != nil {
		t.Fatal(err)
	}
	if err := harness.runner.artifacts.saveManifest(manifest); err != nil {
		t.Fatal(err)
	}
	harness.target.rows["1"] = cloneRow(sourceRows[0])
	harness.target.autoIncrement = 2
	manifestBefore, err := os.ReadFile(filepath.Join(harness.directory, manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	entriesBefore, err := snapshotDirectory(harness.directory)
	if err != nil {
		t.Fatal(err)
	}
	report, err := harness.runner.Run(context.Background(), harness.options(ModeVerify, false))
	if err != nil || !report.CutoverReady || harness.target.mutations != 0 {
		t.Fatalf("report=%+v mutations=%d err=%v", report, harness.target.mutations, err)
	}
	if harness.target.targetLocks != 0 || harness.target.targetUnlocks != 0 || harness.target.readOnlySnapshots != 1 {
		t.Fatalf("verify target lifecycle: locks=%d unlocks=%d readOnlySnapshots=%d", harness.target.targetLocks, harness.target.targetUnlocks, harness.target.readOnlySnapshots)
	}
	manifestAfter, err := os.ReadFile(filepath.Join(harness.directory, manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestBefore) != string(manifestAfter) {
		t.Fatal("verify modified the manifest")
	}
	entriesAfter, err := snapshotDirectory(harness.directory)
	if err != nil || !reflect.DeepEqual(entriesBefore, entriesAfter) {
		t.Fatalf("verify changed checkpoint directory: before=%v after=%v err=%v", entriesBefore, entriesAfter, err)
	}
}

func TestRunnerVerifyRejectsUnauthenticatedOrIncompleteEvidenceWithoutWrites(t *testing.T) {
	table := runnerTable()
	raw := [][]any{{int64(1), int64(11)}}
	for _, test := range []struct {
		name   string
		mutate func(*runnerHarness, *Manifest, *ReconciliationReport)
	}{
		{name: "wrong trusted key", mutate: func(h *runnerHarness, _ *Manifest, _ *ReconciliationReport) {
			h.freezePublicKey = make([]byte, ed25519.PublicKeySize)
		}},
		{name: "wrong target trusted key", mutate: func(h *runnerHarness, _ *Manifest, _ *ReconciliationReport) {
			h.targetFencePublicKey = make([]byte, ed25519.PublicKeySize)
		}},
		{name: "tampered target canonical payload digest", mutate: func(_ *runnerHarness, m *Manifest, _ *ReconciliationReport) {
			m.TargetWriterFence.PayloadSHA256 = "sha256:tampered"
		}},
		{name: "incomplete manifest", mutate: func(_ *runnerHarness, m *Manifest, _ *ReconciliationReport) { m.Completed = false }},
		{name: "missing report digest", mutate: func(_ *runnerHarness, m *Manifest, _ *ReconciliationReport) { m.ReconciliationSHA256 = "" }},
		{name: "report identity mismatch", mutate: func(_ *runnerHarness, m *Manifest, report *ReconciliationReport) {
			report.IdentitySHA256 = "sha256:other"
			finalizeReport(report)
			m.ReconciliationSHA256 = report.DigestSHA256
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunnerHarness(t, table, raw)
			sourceRows, inventory, sourceIdentity := materializeRunnerSource(t, table, raw)
			targetIdentity, err := inspectTargetIdentityForTables(context.Background(), harness.runner.target.db, targetCoordinates{}, tables())
			if err != nil {
				t.Fatal(err)
			}
			identity := CutoverIdentity{SchemaVersion: ManifestSchemaVersion, ToolVersion: ToolVersion, ToolCommit: harness.runner.toolCommit, Source: sourceIdentity, Target: targetIdentity}
			identitySHA, _ := identityDigest(identity)
			manifest := newManifest(identity, identitySHA, inventory, []tableSpec{table})
			checkpoint, _ := makeTableCheckpoint(identitySHA, table, sourceRows, table.initialRows(sourceRows), 0, 1)
			checkpoint.DeferredForeignKeyState = DeferredComplete
			manifest.Checkpoints[table.name] = []TableCheckpoint{checkpoint}
			deferredCheckpoint, _ := makeDeferredCheckpoint(table, sourceRows, 0, 1)
			manifest.DeferredCheckpoints[table.name] = []DeferredCheckpoint{deferredCheckpoint}
			manifest.DeferredForeignKeys = DeferredComplete
			manifest.AutoIncrementCompleted[table.name] = true
			bindRunnerEvidence(t, harness, &manifest)
			stored := successfulRunnerReconciliation(identitySHA, table, inventory[table.name])
			manifest.ReconciliationSHA256 = stored.DigestSHA256
			manifest.Completed = true
			test.mutate(&harness, &manifest, &stored)
			lock, err := harness.runner.artifacts.acquireExclusiveLock()
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if manifest.ReconciliationSHA256 == stored.DigestSHA256 {
				if err := harness.runner.artifacts.saveReport(stored); err != nil {
					t.Fatal(err)
				}
			}
			if err := harness.runner.artifacts.saveManifest(manifest); err != nil {
				t.Fatal(err)
			}
			before, err := snapshotDirectory(harness.directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := harness.runner.Run(context.Background(), harness.options(ModeVerify, false)); err == nil {
				t.Fatal("invalid verification evidence was accepted")
			}
			after, err := snapshotDirectory(harness.directory)
			if err != nil || !reflect.DeepEqual(before, after) || harness.target.mutations != 0 {
				t.Fatalf("verify failure changed state: before=%v after=%v mutations=%d err=%v", before, after, harness.target.mutations, err)
			}
		})
	}
}

func snapshotDirectory(directory string) (map[string]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		contents, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		result[entry.Name()] = string(contents)
	}
	return result, nil
}

func successfulRunnerReconciliation(identitySHA string, table tableSpec, inventory TableInventory) ReconciliationReport {
	report := ReconciliationReport{
		SchemaVersion: ReportSchemaVersion, IdentitySHA256: identitySHA, GeneratedAt: nowUTC(), DeferredForeignKeys: DeferredComplete,
		Tables: []TableResult{{Table: table.name, SourceRows: inventory.Rows, TargetRows: inventory.Rows, SourceDigest: inventory.DigestSHA256, TargetDigest: inventory.DigestSHA256, Match: true}},
	}
	for _, check := range invariants() {
		report.Checks = append(report.Checks, CheckResult{Category: check.category, Name: check.name, Match: true})
	}
	if table.autoIncrement {
		report.Checks = append(report.Checks, CheckResult{Category: "auto_increment", Name: table.name, Match: true})
	}
	for _, metric := range metrics() {
		report.Metrics = append(report.Metrics, MetricResult{Category: metric.category, Name: metric.name, Match: true})
	}
	finalizeReport(&report)
	return report
}

func runnerTable() tableSpec {
	return tableSpec{name: "widget", sourceFrom: "widget", columns: []columnSpec{col("id", kindInt), deferred("parent_id", kindInt)}, keyColumns: []int{0}, autoIncrement: true}
}

func (h runnerHarness) options(mode Mode, execute bool) Options {
	attestationPath := h.attestationPath
	targetFencePath := h.targetFencePath
	if mode == ModeVerify {
		attestationPath = ""
		targetFencePath = ""
	}
	return Options{
		Mode: mode, Execute: execute, BatchSize: 1, MaxSourceRows: 100, MaxSourceBytes: 1_000_000,
		FreezeAttestationPath: attestationPath, FreezePublicKey: h.freezePublicKey, FreezeKeyID: "test-freeze-key",
		TargetWriterFencePath: targetFencePath, TargetWriterFencePublicKey: h.targetFencePublicKey,
		TargetWriterFenceKeyID: "test-target-fence-key", TargetDeploymentGeneration: "test-target-generation",
	}
}

type runnerHarness struct {
	runner                *Runner
	target                *runnerTargetState
	directory             string
	sourceCounts          map[string]int64
	attestationPath       string
	freezePublicKey       []byte
	targetFencePath       string
	targetFencePublicKey  []byte
	targetFencePrivateKey ed25519.PrivateKey
	targetIdentity        TargetIdentity
}

func newRunnerHarness(t *testing.T, table tableSpec, raw [][]any) runnerHarness {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newArtifactStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state := &runnerTargetState{table: table, rows: map[string]Row{}, queryCounts: map[string]int64{}}
	db := sql.OpenDB(runnerTargetConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	sourceCounts := map[string]int64{}
	sequence := 0
	runner := &Runner{target: &targetDatabase{db: db}, artifacts: store, toolCommit: "tool-commit", specs: []tableSpec{table}}
	runner.openSource = func(context.Context) (*sourceSnapshot, error) {
		sequence++
		identity := testIdentity().Source
		identity.SnapshotID = ""
		identity.ExportedSnapshot = fmt.Sprintf("audit-export-%d", sequence)
		identity.TransactionSnapshot = fmt.Sprintf("audit-tx-%d", sequence)
		identity.WALLSN = fmt.Sprintf("0/%X", sequence)
		return &sourceSnapshot{identity: identity, tx: &cutoverFakeSourceTx{queryRows: map[string][][]any{table.name: raw}, rowValues: sourceCountRows(sourceCounts), defaultRow: []any{int64(0)}}}, nil
	}
	_, _, sourceIdentity := materializeRunnerSource(t, table, raw)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	attestationPath := filepath.Join(directory, "source-freeze.json")
	writeRunnerFreezeAttestation(t, attestationPath, privateKey, sourceIdentity)
	targetIdentity, err := inspectTargetIdentityForTables(context.Background(), db, targetCoordinates{}, tables())
	if err != nil {
		t.Fatal(err)
	}
	targetPublicKey, targetPrivateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	targetFencePath := filepath.Join(directory, "target-writer-fence.json")
	writeRunnerTargetFenceAttestation(t, targetFencePath, targetPrivateKey, targetIdentity, "test-target-fence")
	return runnerHarness{
		runner: runner, target: state, directory: directory, sourceCounts: sourceCounts,
		attestationPath: attestationPath, freezePublicKey: publicKey,
		targetFencePath: targetFencePath, targetFencePublicKey: targetPublicKey,
		targetFencePrivateKey: targetPrivateKey, targetIdentity: targetIdentity,
	}
}

func writeRunnerFreezeAttestation(t *testing.T, path string, privateKey ed25519.PrivateKey, source SourceIdentity) {
	t.Helper()
	attestation := SourceFreezeAttestation{
		SchemaVersion: freezeAttestationSchemaVersion, AttestationID: "test-freeze",
		IssuedAt: nowUTC().Add(-time.Minute), ExpiresAt: nowUTC().Add(time.Hour),
		Source:       SourceFreezeBinding{OperatorClusterID: source.ClusterID, DatabaseID: source.DatabaseID, SchemaSHA256: source.SchemaSHA256, SnapshotID: source.SnapshotID},
		Fence:        SourceFreezeFence{ApplicationWritesStopped: true, BackgroundWorkersStopped: true, CDCOrOutboxDrained: true, ActiveBusinessWriters: 0},
		AuthorizedBy: "test-freeze-orchestrator", KeyID: "test-freeze-key",
	}
	payload, err := canonicalJSONBytes(attestation)
	if err != nil {
		t.Fatal(err)
	}
	attestation.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	if err := writeAtomicJSON(path, attestation); err != nil {
		t.Fatal(err)
	}
}

func writeRunnerTargetFenceAttestation(t *testing.T, path string, privateKey ed25519.PrivateKey, target TargetIdentity, attestationID string) {
	t.Helper()
	targetDigest, err := targetIdentityDigest(target)
	if err != nil {
		t.Fatal(err)
	}
	attestation := TargetWriterFenceAttestation{
		SchemaVersion: targetWriterFenceSchemaVersion, AttestationID: attestationID,
		IssuedAt: nowUTC().Add(-time.Minute), ExpiresAt: nowUTC().Add(time.Hour),
		DeploymentGeneration: "test-target-generation", Target: target, TargetIdentitySHA256: targetDigest,
		Fence:        TargetWriterFence{AutomaticRestartDisabled: true, WriteTrafficDisabled: true},
		AuthorizedBy: "test-target-fence-controller", KeyID: "test-target-fence-key",
	}
	payload, err := canonicalTargetWriterFencePayload(attestation)
	if err != nil {
		t.Fatal(err)
	}
	attestation.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	if err := writeAtomicJSON(path, attestation); err != nil {
		t.Fatal(err)
	}
}

func bindRunnerEvidence(t *testing.T, harness runnerHarness, manifest *Manifest) {
	t.Helper()
	var source SourceFreezeAttestation
	if err := readStrictJSON(harness.attestationPath, &source); err != nil {
		t.Fatal(err)
	}
	manifest.SourceFreezeAttestation = source
	manifest.SourceFreezeSHA256, _ = freezeAttestationDigest(source)
	target, err := loadAndVerifyTargetWriterFence(
		harness.targetFencePath, harness.targetFencePublicKey, "test-target-fence-key",
		"test-target-generation", manifest.Identity.Target, nowUTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.TargetWriterFence = target
}

func sourceCountRows(counts map[string]int64) map[string][]any {
	result := make(map[string][]any, len(counts))
	for fragment, count := range counts {
		result[fragment] = []any{count}
	}
	return result
}

func materializeRunnerSource(t *testing.T, table tableSpec, raw [][]any) ([]Row, map[string]TableInventory, SourceIdentity) {
	t.Helper()
	identity := testIdentity().Source
	identity.SnapshotID = ""
	source := &sourceSnapshot{identity: identity, tx: &cutoverFakeSourceTx{queryRows: map[string][][]any{table.name: raw}}}
	allRows, inventory, _, _, err := inspectSource(context.Background(), source, []tableSpec{table}, 100, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	return allRows[table.name], inventory, source.identity
}

func cloneRow(row Row) Row { return append(Row(nil), row...) }

type runnerTargetState struct {
	mu                          sync.Mutex
	table                       tableSpec
	rows                        map[string]Row
	queryCounts                 map[string]int64
	autoIncrement               int64
	mutations                   int
	inserts                     int
	deferredUpdates             int
	targetLocks                 int
	targetUnlocks               int
	readOnlySnapshots           int
	identityChecks              int
	afterMutation               func(int)
	replaceFenceAtIdentityCheck int
	replaceFence                func()
}

type runnerTargetConnector struct{ state *runnerTargetState }

func (c runnerTargetConnector) Connect(context.Context) (driver.Conn, error) {
	return &runnerTargetConn{state: c.state}, nil
}
func (runnerTargetConnector) Driver() driver.Driver { return runnerTargetDriver{} }

type runnerTargetDriver struct{}

func (runnerTargetDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type runnerTargetConn struct {
	state   *runnerTargetState
	pending []func()
}

func (*runnerTargetConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*runnerTargetConn) Close() error                        { return nil }
func (c *runnerTargetConn) Begin() (driver.Tx, error)         { return &runnerTargetTx{conn: c}, nil }
func (c *runnerTargetConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	if options.ReadOnly && options.Isolation == driver.IsolationLevel(sql.LevelRepeatableRead) {
		c.state.mu.Lock()
		c.state.readOnlySnapshots++
		c.state.mu.Unlock()
	}
	return c.Begin()
}

func (c *runnerTargetConn) ExecContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	table := c.state.table
	if strings.HasPrefix(query, "INSERT INTO `"+table.name+"`") {
		row, err := runnerRowFromArguments(table.columns, arguments[:len(table.columns)])
		if err != nil {
			return nil, err
		}
		key := row[table.keyColumns[0]].Value
		c.pending = append(c.pending, func() {
			c.state.rows[key] = cloneRow(row)
			c.state.inserts++
			c.state.mutations++
			if c.state.afterMutation != nil {
				c.state.afterMutation(c.state.mutations)
			}
		})
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "UPDATE `"+table.name+"` SET ") {
		deferredColumns := table.deferredColumns()
		key := fmt.Sprint(arguments[len(deferredColumns)].Value)
		updates, err := runnerRowFromArguments(deferredColumnSpecs(table), arguments[:len(deferredColumns)])
		if err != nil {
			return nil, err
		}
		c.pending = append(c.pending, func() {
			row := cloneRow(c.state.rows[key])
			for i, column := range deferredColumns {
				row[column] = updates[i]
			}
			c.state.rows[key] = row
			c.state.deferredUpdates++
			c.state.mutations++
			if c.state.afterMutation != nil {
				c.state.afterMutation(c.state.mutations)
			}
		})
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "ALTER TABLE `"+table.name+"` AUTO_INCREMENT = ") {
		var next int64
		if _, err := fmt.Sscanf(query, "ALTER TABLE `"+table.name+"` AUTO_INCREMENT = %d", &next); err != nil {
			return nil, err
		}
		c.state.autoIncrement = next
		c.state.mutations++
		if c.state.afterMutation != nil {
			c.state.afterMutation(c.state.mutations)
		}
		return driver.RowsAffected(0), nil
	}
	return nil, fmt.Errorf("unexpected runner target exec: %s", query)
}

func (c *runnerTargetConn) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	table := c.state.table
	switch {
	case query == `SELECT @@server_uuid,DATABASE()`:
		c.state.identityChecks++
		if c.state.replaceFenceAtIdentityCheck == c.state.identityChecks && c.state.replaceFence != nil {
			c.state.replaceFence()
		}
		return &cutoverRows{columns: []string{"server_uuid", "database"}, rows: [][]driver.Value{{"instance", "target"}}}, nil
	case query == `SELECT GET_LOCK(?,0)`:
		c.state.targetLocks++
		return oneRunnerValue(int64(1)), nil
	case query == `SELECT RELEASE_LOCK(?)`:
		c.state.targetUnlocks++
		return oneRunnerValue(int64(1)), nil
	case strings.HasPrefix(query, `SELECT version,name,checksum_sha256,dirty FROM schema_migration`):
		return runnerMigrationRows()
	case strings.HasPrefix(query, targetSchemaSQLPrefix):
		return runnerSchemaRows(c.state.table)
	case query == "SELECT COUNT(*) FROM `"+table.name+"`":
		return oneRunnerValue(int64(len(c.state.rows))), nil
	case strings.HasPrefix(query, "SELECT "+table.targetProjection()+" FROM `"+table.name+"` WHERE "):
		key := fmt.Sprint(arguments[0].Value)
		row, ok := c.state.rows[key]
		if !ok {
			return &cutoverRows{columns: runnerColumnNames(table)}, nil
		}
		return &cutoverRows{columns: runnerColumnNames(table), rows: [][]driver.Value{runnerDriverRow(row)}}, nil
	case strings.HasPrefix(query, "SELECT "+table.targetProjection()+" FROM `"+table.name+"` ORDER BY "):
		keys := make([]string, 0, len(c.state.rows))
		for key := range c.state.rows {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left, _ := exactInt64(keys[i])
			right, _ := exactInt64(keys[j])
			return left < right
		})
		rows := make([][]driver.Value, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, runnerDriverRow(c.state.rows[key]))
		}
		return &cutoverRows{columns: runnerColumnNames(table), rows: rows}, nil
	case query == "SELECT COALESCE(MAX(id),0) FROM `"+table.name+"`":
		maximum := int64(0)
		for key := range c.state.rows {
			value, _ := exactInt64(key)
			if value > maximum {
				maximum = value
			}
		}
		return oneRunnerValue(maximum), nil
	case strings.HasPrefix(query, `SELECT AUTO_INCREMENT FROM information_schema.tables`):
		return oneRunnerValue(c.state.autoIncrement), nil
	default:
		for fragment, count := range c.state.queryCounts {
			if strings.Contains(query, fragment) {
				return oneRunnerValue(count), nil
			}
		}
		if strings.HasPrefix(strings.TrimSpace(query), "SELECT") {
			return oneRunnerValue(int64(0)), nil
		}
	}
	return nil, fmt.Errorf("unexpected runner target query: %s", query)
}

func runnerRowFromArguments(columns []columnSpec, arguments []driver.NamedValue) (Row, error) {
	if len(columns) != len(arguments) {
		return nil, errors.New("argument width mismatch")
	}
	row := make(Row, len(columns))
	for i, column := range columns {
		cell, err := normalizeCell(column.kind, arguments[i].Value)
		if err != nil {
			return nil, err
		}
		row[i] = cell
	}
	return row, nil
}

func deferredColumnSpecs(table tableSpec) []columnSpec {
	result := make([]columnSpec, 0, len(table.deferredColumns()))
	for _, index := range table.deferredColumns() {
		result = append(result, table.columns[index])
	}
	return result
}

func runnerColumnNames(table tableSpec) []string {
	result := make([]string, len(table.columns))
	for i, column := range table.columns {
		result[i] = column.name
	}
	return result
}

func runnerDriverRow(row Row) []driver.Value {
	result := make([]driver.Value, len(row))
	for i, cell := range row {
		value, _ := cell.driverValue()
		result[i] = value
	}
	return result
}

func oneRunnerValue(value driver.Value) driver.Rows {
	return &cutoverRows{columns: []string{"value"}, rows: [][]driver.Value{{value}}}
}

func runnerMigrationRows() (driver.Rows, error) {
	migrations, err := expectedTargetMigrations()
	if err != nil {
		return nil, err
	}
	rows := make([][]driver.Value, 0, len(migrations))
	for _, migration := range migrations {
		checksum, err := hex.DecodeString(migration.Checksum)
		if err != nil {
			return nil, err
		}
		rows = append(rows, []driver.Value{migration.Version, migration.Name, checksum, false})
	}
	return &cutoverRows{columns: []string{"version", "name", "checksum_sha256", "dirty"}, rows: rows}, nil
}

func runnerSchemaRows(_ tableSpec) (driver.Rows, error) {
	contract, err := expectedTargetSchemaContract(tables())
	if err != nil {
		return nil, err
	}
	objects := targetSchemaObjectsForContract(contract)
	rows := make([][]driver.Value, len(objects))
	for i, object := range objects {
		rows[i] = []driver.Value{object.Type, object.Table, object.Name, object.Definition}
	}
	return &cutoverRows{columns: []string{"object_type", "table_name", "object_name", "definition"}, rows: rows}, nil
}

type runnerTargetTx struct{ conn *runnerTargetConn }

func (t *runnerTargetTx) Commit() error {
	t.conn.state.mu.Lock()
	defer t.conn.state.mu.Unlock()
	for _, apply := range t.conn.pending {
		apply()
	}
	t.conn.pending = nil
	return nil
}
func (t *runnerTargetTx) Rollback() error {
	t.conn.pending = nil
	return nil
}

var _ driver.ConnBeginTx = (*runnerTargetConn)(nil)
var _ driver.ExecerContext = (*runnerTargetConn)(nil)
var _ driver.QueryerContext = (*runnerTargetConn)(nil)
var _ io.Closer = (*runnerTargetConn)(nil)
