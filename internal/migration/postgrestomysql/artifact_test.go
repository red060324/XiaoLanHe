package postgrestomysql

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSaveReportAcceptsExactValidExistingReport(t *testing.T) {
	store := newReportArtifactStore(t)
	report := testReconciliationReport()

	if err := store.saveReport(report); err != nil {
		t.Fatal(err)
	}
	if err := store.saveReport(report); err != nil {
		t.Fatalf("save exact existing report: %v", err)
	}
}

func TestSaveReportRejectsTamperedExistingReportBody(t *testing.T) {
	store := newReportArtifactStore(t)
	report := testReconciliationReport()
	if err := store.saveReport(report); err != nil {
		t.Fatal(err)
	}

	path := reconciliationReportPath(store, report)
	var tampered ReconciliationReport
	if err := readStrictJSON(path, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.IdentitySHA256 = "sha256:tampered"
	// Retain the original digest field and content-addressed filename. The body
	// no longer hashes to either of them and must not be accepted as immutable.
	if err := writeAtomicJSON(path, tampered); err != nil {
		t.Fatal(err)
	}

	if err := store.saveReport(report); err == nil || !strings.Contains(err.Error(), "immutable reconciliation report conflicts") {
		t.Fatalf("tampered existing report error = %v", err)
	}
}

func TestSaveReportPropagatesStatErrors(t *testing.T) {
	report := testReconciliationReport()
	notDirectory := filepath.Join(t.TempDir(), "checkpoint-file")
	if err := os.WriteFile(notDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, openErr := newArtifactStore(notDirectory)
	if !errors.Is(openErr, syscall.ENOTDIR) {
		t.Fatalf("newArtifactStore error = %v, want ENOTDIR", openErr)
	}
	err := store.saveReport(report)
	if !errors.Is(err, syscall.ENOTDIR) {
		if err == nil || !strings.Contains(err.Error(), "not initialized") {
			t.Fatalf("saveReport error = %v, want closed/uninitialized store", err)
		}
	}
}

func newReportArtifactStore(t *testing.T) artifactStore {
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
	return store
}

func testReconciliationReport() ReconciliationReport {
	report := ReconciliationReport{
		SchemaVersion:       ReportSchemaVersion,
		IdentitySHA256:      "sha256:identity",
		GeneratedAt:         time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		DeferredForeignKeys: DeferredComplete,
		Tables:              []TableResult{},
		Checks:              []CheckResult{},
		Metrics:             []MetricResult{},
	}
	finalizeReport(&report)
	return report
}

func reconciliationReportPath(store artifactStore, report ReconciliationReport) string {
	return filepath.Join(store.directory, "reconciliation-"+strings.TrimPrefix(report.DigestSHA256, "sha256:")+".json")
}
