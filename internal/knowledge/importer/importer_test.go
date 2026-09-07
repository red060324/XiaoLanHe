package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

var fixedTime = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

func TestManifestRecordsCanonicalDigestsDispositionsAndChunks(t *testing.T) {
	source := &sourceFake{documents: []LegacyDocument{legacyDocument(1, "one", 2), legacyDocument(4, "四", 3)}}
	manifest, err := BuildManifest(context.Background(), source, 1, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourceDocumentCount != 2 || manifest.SourceChunkCount != 5 || !validSHA256(manifest.ManifestSHA256) {
		t.Fatalf("manifest=%+v", manifest)
	}
	entry := manifest.Documents[1]
	if entry.SourceKey != "xlh-legacy-4.txt" || entry.EnvelopeByteLength <= entry.EnvelopeRuneLength || entry.DiscardedFields.Metadata != discardedDisposition || !validSHA256(entry.SourceRowSHA256) || !validSHA256(entry.EnvelopeSHA256) {
		t.Fatalf("entry=%+v", entry)
	}
	path := t.TempDir() + "/manifest.json"
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil || loaded.ManifestSHA256 != manifest.ManifestSHA256 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	changed := manifest
	changed.Documents = append([]ManifestDocument(nil), manifest.Documents...)
	changed.Documents[0].EnvelopeByteLength++
	changed.ManifestSHA256 = manifestDigest(changed)
	if err := WriteManifest(path, changed); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("overwrite err=%v", err)
	}
}

func TestManifestRejectsMalformedAndChangingSource(t *testing.T) {
	malformed := legacyDocument(1, "one", 0)
	malformed.Metadata = json.RawMessage(`{`)
	if _, err := BuildManifest(context.Background(), &sourceFake{documents: []LegacyDocument{malformed}}, 10, fixedTime); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("err=%v", err)
	}
	source := &sourceFake{documents: []LegacyDocument{legacyDocument(2, "two", 0), legacyDocument(1, "one", 0)}}
	if _, err := BuildManifest(context.Background(), source, 10, fixedTime); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("non-monotonic err=%v", err)
	}
}

func TestImporterDryRunUsesManifestAndWritesNothing(t *testing.T) {
	documents := []LegacyDocument{legacyDocument(4, "four", 1), legacyDocument(5, "five", 1)}
	source, manifest, checkpoint := fixture(t, documents)
	runner, err := New(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), runOptions(manifest, checkpoint, false, nil))
	if err != nil || !report.DryRun || report.Scanned != 2 || report.Submitted != 0 || report.LastScannedID != 5 || report.ContinuousSuccessWatermark != 0 || report.Items[0].SourceKey != "xlh-legacy-4.txt" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestImporterPersistsOnlyContinuousProcessedWatermark(t *testing.T) {
	documents := []LegacyDocument{legacyDocument(1, "one", 1), legacyDocument(2, "two", 1)}
	source, manifest, checkpoint := fixture(t, documents)
	destination := &destinationFake{
		accept: func(sourceKey string, call int) (entity.AcceptedDocument, error) {
			return entity.AcceptedDocument{TrackID: fmt.Sprintf("track-%d", call), SourceKey: sourceKey}, nil
		},
		track: func(trackID string, call int) (entity.Track, error) {
			if call == 2 {
				return entity.Track{TrackID: trackID, TotalCount: 1, Documents: []entity.Document{{TrackID: trackID, SourceKey: "xlh-legacy-2.txt", Status: "FAILED"}}}, nil
			}
			return processedTrack(trackID, "xlh-legacy-1.txt", manifest.Documents[0].EnvelopeRuneLength), nil
		},
	}
	saved := []Checkpoint{}
	runner, _ := New(source, destination)
	options := runOptions(manifest, checkpoint, true, func(value Checkpoint) error { saved = append(saved, value); return nil })
	report, err := runner.Run(context.Background(), options)
	if !errors.Is(err, ErrIncomplete) || report.ContinuousSuccessWatermark != 1 || report.LastScannedID != 2 || report.Processed != 1 || report.Failed != 1 || destination.creates != 2 || len(saved) != 2 || saved[len(saved)-1].ContinuousSuccessWatermark != 1 || len(saved[len(saved)-1].Failures) != 1 || saved[len(saved)-1].Failures[0].LegacyID != 2 {
		t.Fatalf("report=%+v saves=%+v err=%v", report, saved, err)
	}
}

func TestImporterResumeRetriesFailureWithoutSkippingIt(t *testing.T) {
	documents := []LegacyDocument{legacyDocument(1, "one", 0), legacyDocument(2, "two", 0), legacyDocument(3, "three", 0)}
	source, manifest, checkpoint := fixture(t, documents)
	checkpoint, _ = checkpoint.WithSuccess(manifest, successProof(manifest.Documents[0], "track-1", "doc-1"))
	failed := FailureRecord{LegacyID: 2, SourceKey: manifest.Documents[1].SourceKey, EnvelopeSHA256: manifest.Documents[1].EnvelopeSHA256, FailureCode: "target_failed", FailedAt: fixedTime.Add(time.Minute)}
	checkpoint, _ = checkpoint.WithFailure(failed)
	destination := &destinationFake{
		accept: func(sourceKey string, call int) (entity.AcceptedDocument, error) {
			return entity.AcceptedDocument{TrackID: fmt.Sprintf("track-%d", call+1), SourceKey: sourceKey}, nil
		},
		track: func(trackID string, call int) (entity.Track, error) {
			entry := manifest.Documents[call]
			return processedTrack(trackID, entry.SourceKey, entry.EnvelopeRuneLength), nil
		},
	}
	var saved Checkpoint
	runner, _ := New(source, destination)
	report, err := runner.Run(context.Background(), runOptions(manifest, checkpoint, true, func(value Checkpoint) error { saved = value; return nil }))
	if err != nil || source.afterIDs[len(source.afterIDs)-1] != 1 || destination.firstSourceKey != "xlh-legacy-2.txt" || report.ContinuousSuccessWatermark != 3 || len(saved.Failures) != 0 {
		t.Fatalf("report=%+v saved=%+v source=%+v err=%v", report, saved, source, err)
	}
}

func TestImporterRejectsUnprovedReplayAndTrackDrift(t *testing.T) {
	for name, mutate := range map[string]func(*destinationFake, Manifest){
		"unproved replay": func(destination *destinationFake, manifest Manifest) {
			destination.accept = func(sourceKey string, _ int) (entity.AcceptedDocument, error) {
				return entity.AcceptedDocument{TrackID: "track-1", SourceKey: sourceKey, Replayed: true}, nil
			}
		},
		"wrong source": func(destination *destinationFake, manifest Manifest) {
			destination.track = func(trackID string, _ int) (entity.Track, error) {
				return processedTrack(trackID, "xlh-legacy-999.txt", manifest.Documents[0].EnvelopeRuneLength), nil
			}
		},
		"multiple documents": func(destination *destinationFake, manifest Manifest) {
			destination.track = func(trackID string, _ int) (entity.Track, error) {
				track := processedTrack(trackID, manifest.Documents[0].SourceKey, manifest.Documents[0].EnvelopeRuneLength)
				track.Documents = append(track.Documents, track.Documents[0])
				track.TotalCount = 2
				return track, nil
			}
		},
		"length mismatch": func(destination *destinationFake, manifest Manifest) {
			destination.track = func(trackID string, _ int) (entity.Track, error) {
				return processedTrack(trackID, manifest.Documents[0].SourceKey, manifest.Documents[0].EnvelopeRuneLength+1), nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			source, manifest, checkpoint := fixture(t, []LegacyDocument{legacyDocument(1, "one", 0)})
			destination := &destinationFake{
				accept: func(sourceKey string, _ int) (entity.AcceptedDocument, error) {
					return entity.AcceptedDocument{TrackID: "track-1", SourceKey: sourceKey}, nil
				},
				track: func(trackID string, _ int) (entity.Track, error) {
					return processedTrack(trackID, manifest.Documents[0].SourceKey, manifest.Documents[0].EnvelopeRuneLength), nil
				},
			}
			mutate(destination, manifest)
			runner, _ := New(source, destination)
			report, err := runner.Run(context.Background(), runOptions(manifest, checkpoint, true, func(Checkpoint) error { return nil }))
			if !errors.Is(err, ErrIncomplete) || report.ContinuousSuccessWatermark != 0 || report.Failed != 1 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestImporterVerifiedReplayCanAdvance(t *testing.T) {
	documents := []LegacyDocument{legacyDocument(1, "one", 0), legacyDocument(2, "two", 0)}
	source, manifest, checkpoint := fixture(t, documents)
	proof := successProof(manifest.Documents[1], "track-2", "doc-2")
	checkpoint.Successes = append(checkpoint.Successes, proof)
	checkpoint.CheckpointSHA256 = checkpointDigest(checkpoint)
	destination := &destinationFake{
		accept: func(sourceKey string, _ int) (entity.AcceptedDocument, error) {
			return entity.AcceptedDocument{TrackID: "track-1", SourceKey: sourceKey}, nil
		},
		track: func(trackID string, call int) (entity.Track, error) {
			if call == 1 {
				return processedTrack(trackID, manifest.Documents[0].SourceKey, manifest.Documents[0].EnvelopeRuneLength), nil
			}
			track := processedTrack(trackID, manifest.Documents[1].SourceKey, manifest.Documents[1].EnvelopeRuneLength)
			track.Documents[0].DocumentID = "doc-2"
			return track, nil
		},
	}
	destination.accept = func(sourceKey string, call int) (entity.AcceptedDocument, error) {
		if call == 2 {
			return entity.AcceptedDocument{TrackID: "track-2", SourceKey: sourceKey, Replayed: true}, nil
		}
		return entity.AcceptedDocument{TrackID: "track-1", SourceKey: sourceKey}, nil
	}
	runner, _ := New(source, destination)
	report, err := runner.Run(context.Background(), runOptions(manifest, checkpoint, true, func(Checkpoint) error { return nil }))
	if err != nil || report.ContinuousSuccessWatermark != 2 || report.Replayed != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

type sourceFake struct {
	documents  []LegacyDocument
	descriptor SourceDescriptor
	afterIDs   []int64
}

func (source *sourceFake) Descriptor() SourceDescriptor {
	if source.descriptor.IdentitySHA256 == "" {
		return testDescriptor()
	}
	return source.descriptor
}
func (source *sourceFake) ListLegacyKnowledge(_ context.Context, afterID int64, limit int) ([]LegacyDocument, error) {
	source.afterIDs = append(source.afterIDs, afterID)
	result := make([]LegacyDocument, 0, limit)
	for _, document := range source.documents {
		if document.ID > afterID {
			result = append(result, document)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

type destinationFake struct {
	accept              func(string, int) (entity.AcceptedDocument, error)
	track               func(string, int) (entity.Track, error)
	creates, trackCalls int
	firstSourceKey      string
}

func (destination *destinationFake) Create(_ context.Context, sourceKey, _ string) (entity.AcceptedDocument, error) {
	destination.creates++
	if destination.firstSourceKey == "" {
		destination.firstSourceKey = sourceKey
	}
	return destination.accept(sourceKey, destination.creates)
}
func (destination *destinationFake) Track(_ context.Context, trackID string) (entity.Track, error) {
	destination.trackCalls++
	return destination.track(trackID, destination.trackCalls)
}

func fixture(t *testing.T, documents []LegacyDocument) (*sourceFake, Manifest, Checkpoint) {
	t.Helper()
	source := &sourceFake{documents: documents}
	manifest, err := BuildManifest(context.Background(), source, 100, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := NewCheckpoint(manifest, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	return source, manifest, checkpoint
}
func legacyDocument(id int64, title string, chunks int64) LegacyDocument {
	return LegacyDocument{ID: id, Draft: entity.DocumentDraft{SourceType: "guide", Title: title, ContentText: "content " + title}, Metadata: json.RawMessage(`{"retained":true}`), CreatedAt: fixedTime, UpdatedAt: fixedTime.Add(time.Second), LegacyChunkCount: chunks}
}
func testDescriptor() SourceDescriptor {
	return SourceDescriptor{IdentitySHA256: "sha256:" + repeat("a", 64), SnapshotID: "snapshot-1", SchemaSHA256: "sha256:" + repeat("b", 64), Isolation: "repeatable_read", ReadOnly: true}
}
func runOptions(manifest Manifest, checkpoint Checkpoint, execute bool, save func(Checkpoint) error) Options {
	return Options{Execute: execute, Limit: 100, PollInterval: time.Millisecond, PerDocumentTimeout: time.Second, Manifest: manifest, Checkpoint: checkpoint, SaveCheckpoint: save}
}
func processedTrack(trackID, sourceKey string, length int) entity.Track {
	return entity.Track{TrackID: trackID, TotalCount: 1, Documents: []entity.Document{{DocumentID: "doc-" + sourceKey, TrackID: trackID, SourceKey: sourceKey, Status: "PROCESSED", ContentLength: length}}}
}
func successProof(entry ManifestDocument, trackID, documentID string) SuccessProof {
	return SuccessProof{LegacyID: entry.LegacyID, SourceKey: entry.SourceKey, EnvelopeSHA256: entry.EnvelopeSHA256, TrackID: trackID, DocumentID: documentID, VerifiedAt: fixedTime}
}
func repeat(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}
