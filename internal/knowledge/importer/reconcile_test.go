package importer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

func TestReconcilePaginatesBeyondFourThousandDocuments(t *testing.T) {
	documents := make([]LegacyDocument, 0, 4001)
	for id := int64(1); id <= 4001; id++ {
		documents = append(documents, legacyDocument(id, fmt.Sprintf("doc %d", id), 1))
	}
	_, manifest, _ := fixture(t, documents)
	checkpoint := completeCheckpoint(t, manifest)
	target := &reconciliationFake{health: entity.Health{}, documents: targetDocuments(manifest)}
	report, err := Reconcile(context.Background(), manifest, checkpoint, target, 200, fixedTime.Add(time.Hour))
	if err != nil || !report.ImportGateComplete || report.TargetDocuments != 4001 || report.TargetChunks != 4001 || target.calls != 21 || !validSHA256(report.ReportSHA256) {
		t.Fatalf("complete=%v docs=%d chunks=%d calls=%d err=%v", report.ImportGateComplete, report.TargetDocuments, report.TargetChunks, target.calls, err)
	}
}

func TestReconcileReportsSetLengthStatusAndPipelineFailures(t *testing.T) {
	_, manifest, _ := fixture(t, []LegacyDocument{legacyDocument(1, "one", 1), legacyDocument(2, "two", 2)})
	checkpoint := completeCheckpoint(t, manifest)
	documents := targetDocuments(manifest)
	documents[0].ContentLength++
	documents[0].Status = "FAILED"
	documents[0].FailureCode = "embedding_failed"
	documents[1].SourceKey = "xlh-legacy-999.txt"
	target := &reconciliationFake{health: entity.Health{PipelineActive: true, RecoveryRequired: true}, documents: documents}
	report, err := Reconcile(context.Background(), manifest, checkpoint, target, 1, fixedTime.Add(time.Hour))
	if !errors.Is(err, ErrIncomplete) || report.ImportGateComplete || report.ExactSourceKeySet || report.AllProcessed || report.PipelineIdle || !report.RecoveryRequired ||
		len(report.MissingSourceKeys) != 1 || report.MissingSourceKeys[0] != "xlh-legacy-2.txt" || len(report.UnexpectedSourceKeys) != 1 ||
		len(report.LengthMismatches) != 1 || len(report.FailedTargets) != 1 || report.FailedTargets[0].FailureCode != "embedding_failed" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestReconcileRejectsDuplicateTargetIdentity(t *testing.T) {
	_, manifest, _ := fixture(t, []LegacyDocument{legacyDocument(1, "one", 0), legacyDocument(2, "two", 0)})
	checkpoint := completeCheckpoint(t, manifest)
	documents := targetDocuments(manifest)
	documents[1].DocumentID = documents[0].DocumentID
	target := &reconciliationFake{documents: documents}
	report, err := Reconcile(context.Background(), manifest, checkpoint, target, 10, fixedTime.Add(time.Hour))
	if !errors.Is(err, ErrIncomplete) || report.UniqueTargets || report.ImportGateComplete {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

type reconciliationFake struct {
	health    entity.Health
	documents []entity.Document
	calls     int
}

func (fake *reconciliationFake) Health(context.Context) (entity.Health, error) {
	return fake.health, nil
}
func (fake *reconciliationFake) ListPage(_ context.Context, page, pageSize int) (TargetPage, error) {
	fake.calls++
	start := (page - 1) * pageSize
	if start > len(fake.documents) {
		start = len(fake.documents)
	}
	end := start + pageSize
	if end > len(fake.documents) {
		end = len(fake.documents)
	}
	return TargetPage{Documents: append([]entity.Document(nil), fake.documents[start:end]...), Page: page, PageSize: pageSize, RawCount: end - start, RawTotal: len(fake.documents), HasNext: end < len(fake.documents)}, nil
}

func targetDocuments(manifest Manifest) []entity.Document {
	result := make([]entity.Document, 0, len(manifest.Documents))
	for _, entry := range manifest.Documents {
		result = append(result, entity.Document{DocumentID: fmt.Sprintf("document-%d", entry.LegacyID), SourceKey: entry.SourceKey, Status: "PROCESSED", ContentLength: entry.EnvelopeRuneLength, ChunksCount: int(entry.LegacyChunkCount)})
	}
	return result
}

func completeCheckpoint(t *testing.T, manifest Manifest) Checkpoint {
	t.Helper()
	checkpoint, err := NewCheckpoint(manifest, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Documents {
		checkpoint, err = checkpoint.WithSuccess(manifest, successProof(entry, fmt.Sprintf("track-%d", entry.LegacyID), fmt.Sprintf("document-%d", entry.LegacyID)))
		if err != nil {
			t.Fatal(err)
		}
	}
	return checkpoint
}
