package importer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

func TestImporterDryRunIsReadOnlyAndResumable(t *testing.T) {
	source := &sourceFake{documents: []LegacyDocument{{ID: 4, Draft: validDraft("four")}, {ID: 5, Draft: validDraft("five")}}}
	importer, err := New(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := importer.Run(context.Background(), Options{AfterID: 3, Limit: 2, PollInterval: time.Millisecond, PerDocumentTimeout: time.Second})
	if err != nil || !report.DryRun || report.Scanned != 2 || report.Submitted != 0 || report.LastID != 5 || source.afterID != 3 || source.limit != 2 || report.Items[0].SourceKey != "xlh-legacy-4.txt" {
		t.Fatalf("report=%+v source=%+v err=%v", report, source, err)
	}
}

func TestImporterExecutePollsAndReportsReplay(t *testing.T) {
	source := &sourceFake{documents: []LegacyDocument{{ID: 8, Draft: validDraft("eight")}}}
	destination := &destinationFake{accepted: entity.AcceptedDocument{TrackID: "track-8", Replayed: true}, tracks: []entity.Track{
		{TrackID: "track-8", Documents: []entity.Document{{Status: "PROCESSING"}}},
		{TrackID: "track-8", Documents: []entity.Document{{DocumentID: "doc-8", Status: "PROCESSED"}}},
	}}
	importer, _ := New(source, destination)
	report, err := importer.Run(context.Background(), Options{Execute: true, Limit: 1, PollInterval: time.Millisecond, PerDocumentTimeout: time.Second})
	if err != nil || report.DryRun || report.Submitted != 1 || report.Replayed != 1 || report.Processed != 1 || report.Failed != 0 || destination.creates != 1 || destination.sourceKey != "xlh-legacy-8.txt" || report.Items[0].DocumentID != "doc-8" {
		t.Fatalf("report=%+v destination=%+v err=%v", report, destination, err)
	}
}

func TestImporterReportsFailuresWithoutBlindRetry(t *testing.T) {
	source := &sourceFake{documents: []LegacyDocument{{ID: 1, Draft: validDraft("one")}, {ID: 2, Draft: entity.DocumentDraft{Title: "invalid"}}}}
	destination := &destinationFake{createErr: entity.ErrUnavailable}
	importer, _ := New(source, destination)
	report, err := importer.Run(context.Background(), Options{Execute: true, Limit: 2, PollInterval: time.Millisecond, PerDocumentTimeout: time.Second})
	if !errors.Is(err, ErrIncomplete) || report.Failed != 2 || destination.creates != 1 || report.Items[0].FailureCode != "dependency_unavailable" || report.Items[1].FailureCode != "invalid_legacy_document" {
		t.Fatalf("report=%+v destination=%+v err=%v", report, destination, err)
	}
}

func TestImporterDeadlineStopsPolling(t *testing.T) {
	source := &sourceFake{documents: []LegacyDocument{{ID: 1, Draft: validDraft("one")}}}
	destination := &destinationFake{accepted: entity.AcceptedDocument{TrackID: "track-1"}, tracks: []entity.Track{{TrackID: "track-1", Documents: []entity.Document{{Status: "PROCESSING"}}}}}
	importer, _ := New(source, destination)
	report, err := importer.Run(context.Background(), Options{Execute: true, Limit: 1, PollInterval: time.Millisecond, PerDocumentTimeout: 5 * time.Millisecond})
	if !errors.Is(err, ErrIncomplete) || report.Failed != 1 || report.Items[0].FailureCode != "deadline" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

type sourceFake struct {
	documents []LegacyDocument
	afterID   int64
	limit     int
}

func (f *sourceFake) ListLegacyKnowledge(_ context.Context, afterID int64, limit int) ([]LegacyDocument, error) {
	f.afterID, f.limit = afterID, limit
	return append([]LegacyDocument(nil), f.documents...), nil
}

type destinationFake struct {
	accepted            entity.AcceptedDocument
	createErr           error
	tracks              []entity.Track
	creates, trackCalls int
	sourceKey           string
}

func (f *destinationFake) Create(_ context.Context, sourceKey, _ string) (entity.AcceptedDocument, error) {
	f.creates++
	f.sourceKey = sourceKey
	return f.accepted, f.createErr
}
func (f *destinationFake) Track(context.Context, string) (entity.Track, error) {
	f.trackCalls++
	if len(f.tracks) == 0 {
		return entity.Track{}, entity.ErrUnavailable
	}
	if len(f.tracks) == 1 {
		return f.tracks[0], nil
	}
	result := f.tracks[0]
	f.tracks = f.tracks[1:]
	return result, nil
}
func validDraft(title string) entity.DocumentDraft {
	return entity.DocumentDraft{SourceType: "guide", Title: title, ContentText: "content"}
}
