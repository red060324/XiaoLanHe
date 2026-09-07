package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

var (
	ErrInvalidOptions     = errors.New("invalid legacy knowledge import options")
	ErrIncomplete         = errors.New("legacy knowledge import incomplete")
	ErrManifestMismatch   = errors.New("legacy knowledge manifest mismatch")
	ErrCheckpointMismatch = errors.New("legacy knowledge checkpoint mismatch")
)

const ToolVersion = "xlh-legacy-knowledge-import-v1"

// SourceDescriptor contains no DSN or credentials. It binds artifacts to a database,
// schema, and the read-only snapshot used to produce the original manifest.
type SourceDescriptor struct {
	IdentitySHA256 string `json:"identitySha256"`
	SnapshotID     string `json:"snapshotId"`
	SchemaSHA256   string `json:"schemaSha256"`
	Isolation      string `json:"isolation"`
	ReadOnly       bool   `json:"readOnly"`
}

type LegacyDocument struct {
	ID               int64
	Draft            entity.DocumentDraft
	Metadata         json.RawMessage
	PublishedAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LegacyChunkCount int64
}

type Source interface {
	ListLegacyKnowledge(context.Context, int64, int) ([]LegacyDocument, error)
}

type SourceDescriber interface {
	Descriptor() SourceDescriptor
}

type ManifestSource interface {
	Source
	SourceDescriber
}

type Destination interface {
	Create(context.Context, string, string) (entity.AcceptedDocument, error)
	Track(context.Context, string) (entity.Track, error)
}

type Options struct {
	Execute            bool
	Limit              int
	PollInterval       time.Duration
	PerDocumentTimeout time.Duration
	Manifest           Manifest
	Checkpoint         Checkpoint
	SaveCheckpoint     func(Checkpoint) error
}

type Item struct {
	LegacyID       int64  `json:"legacyId"`
	SourceKey      string `json:"sourceKey,omitempty"`
	EnvelopeSHA256 string `json:"envelopeSha256,omitempty"`
	TrackID        string `json:"trackId,omitempty"`
	DocumentID     string `json:"documentId,omitempty"`
	Status         string `json:"status"`
	FailureCode    string `json:"failureCode,omitempty"`
	Replayed       bool   `json:"replayed,omitempty"`
}

type Report struct {
	DryRun                     bool   `json:"dryRun"`
	ManifestSHA256             string `json:"manifestSha256"`
	AfterID                    int64  `json:"afterId"`
	LastScannedID              int64  `json:"lastScannedId"`
	ContinuousSuccessWatermark int64  `json:"continuousSuccessWatermark"`
	SourceDocuments            int    `json:"sourceDocuments"`
	SourceChunks               int64  `json:"sourceChunks"`
	Scanned                    int    `json:"scanned"`
	Submitted                  int    `json:"submitted"`
	Replayed                   int    `json:"replayed"`
	Processed                  int    `json:"processed"`
	Failed                     int    `json:"failed"`
	Items                      []Item `json:"items"`
}

type Importer struct {
	source      ManifestSource
	destination Destination
}

func New(source ManifestSource, destination Destination) (*Importer, error) {
	if source == nil {
		return nil, ErrInvalidOptions
	}
	return &Importer{source: source, destination: destination}, nil
}

func (i *Importer) Run(ctx context.Context, options Options) (Report, error) {
	report := Report{
		DryRun: !options.Execute, ManifestSHA256: options.Manifest.ManifestSHA256,
		AfterID: options.Checkpoint.ContinuousSuccessWatermark, LastScannedID: options.Checkpoint.ContinuousSuccessWatermark,
		ContinuousSuccessWatermark: options.Checkpoint.ContinuousSuccessWatermark,
		SourceDocuments:            options.Manifest.SourceDocumentCount, SourceChunks: options.Manifest.SourceChunkCount, Items: []Item{},
	}
	if options.Limit < 1 || options.Limit > 100 || options.PollInterval <= 0 || options.PerDocumentTimeout <= 0 ||
		options.Execute && (i.destination == nil || options.SaveCheckpoint == nil) {
		return report, ErrInvalidOptions
	}
	if err := ValidateManifest(options.Manifest); err != nil {
		return report, err
	}
	if err := ValidateCheckpoint(options.Checkpoint, options.Manifest); err != nil {
		return report, err
	}
	descriptor := i.source.Descriptor()
	if descriptor.IdentitySHA256 != options.Manifest.Source.IdentitySHA256 || descriptor.SchemaSHA256 != options.Manifest.Source.SchemaSHA256 || !descriptor.ReadOnly || descriptor.Isolation != "repeatable_read" {
		return report, ErrManifestMismatch
	}

	afterID := options.Checkpoint.ContinuousSuccessWatermark
	expected := manifestDocumentsAfter(options.Manifest, afterID, options.Limit)
	documents, err := i.source.ListLegacyKnowledge(ctx, afterID, options.Limit)
	if err != nil {
		return report, err
	}
	if len(documents) != len(expected) {
		return report, fmt.Errorf("%w: source page length changed", ErrManifestMismatch)
	}
	checkpoint := options.Checkpoint
	for index, document := range documents {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Scanned++
		report.LastScannedID = document.ID
		entry, envelope, manifestErr := manifestEntry(document)
		item := Item{LegacyID: document.ID, SourceKey: entry.SourceKey, EnvelopeSHA256: entry.EnvelopeSHA256, Status: "validated"}
		if manifestErr != nil || !equalManifestDocument(entry, expected[index]) {
			item.Status, item.FailureCode = "failed", "manifest_mismatch"
			return i.fail(report, checkpoint, options, item, ErrManifestMismatch)
		}
		if !options.Execute {
			report.Items = append(report.Items, item)
			continue
		}

		documentCtx, cancel := context.WithTimeout(ctx, options.PerDocumentTimeout)
		accepted, createErr := i.destination.Create(documentCtx, entry.SourceKey, envelope)
		if createErr != nil {
			cancel()
			item.Status, item.FailureCode = "failed", classifyFailure(createErr)
			return i.fail(report, checkpoint, options, item, createErr)
		}
		if accepted.TrackID == "" || accepted.SourceKey != "" && accepted.SourceKey != entry.SourceKey {
			cancel()
			item.Status, item.FailureCode = "failed", "dependency_contract"
			return i.fail(report, checkpoint, options, item, entity.ErrContract)
		}
		item.TrackID, item.Replayed = accepted.TrackID, accepted.Replayed
		replayProof, replayProved := checkpoint.ReplayProof(entry)
		if accepted.Replayed && !replayProved {
			cancel()
			item.Status, item.FailureCode = "failed", "unverified_replay"
			return i.fail(report, checkpoint, options, item, ErrCheckpointMismatch)
		}
		if accepted.Replayed && replayProof.TrackID != accepted.TrackID {
			cancel()
			item.Status, item.FailureCode = "failed", "unverified_replay"
			return i.fail(report, checkpoint, options, item, ErrCheckpointMismatch)
		}
		if accepted.Replayed {
			report.Replayed++
		} else {
			report.Submitted++
		}
		tracked, trackErr := waitForTerminal(documentCtx, i.destination, accepted.TrackID, entry.SourceKey, entry.EnvelopeRuneLength, options.PollInterval)
		cancel()
		if trackErr != nil {
			item.Status, item.FailureCode = "failed", classifyFailure(trackErr)
			return i.fail(report, checkpoint, options, item, trackErr)
		}
		item.Status, item.DocumentID = "processed", tracked.DocumentID
		if accepted.Replayed && replayProof.DocumentID != tracked.DocumentID {
			item.Status, item.FailureCode = "failed", "unverified_replay"
			return i.fail(report, checkpoint, options, item, ErrCheckpointMismatch)
		}
		proof := SuccessProof{LegacyID: entry.LegacyID, SourceKey: entry.SourceKey, EnvelopeSHA256: entry.EnvelopeSHA256, TrackID: accepted.TrackID, DocumentID: tracked.DocumentID, Replayed: accepted.Replayed, VerifiedAt: time.Now().UTC()}
		candidate, checkpointErr := checkpoint.WithSuccess(options.Manifest, proof)
		if checkpointErr != nil {
			item.Status, item.FailureCode = "failed", "checkpoint_mismatch"
			return i.fail(report, checkpoint, options, item, checkpointErr)
		}
		if err := options.SaveCheckpoint(candidate); err != nil {
			item.Status, item.FailureCode = "failed", "checkpoint_write"
			failureCheckpoint, failureErr := checkpoint.WithFailure(FailureRecord{LegacyID: entry.LegacyID, SourceKey: entry.SourceKey, EnvelopeSHA256: entry.EnvelopeSHA256, TrackID: accepted.TrackID, DocumentID: tracked.DocumentID, FailureCode: item.FailureCode, FailedAt: time.Now().UTC()})
			if failureErr == nil {
				_ = options.SaveCheckpoint(failureCheckpoint)
			}
			report.Failed++
			report.Items = append(report.Items, item)
			return report, fmt.Errorf("%w: save success checkpoint: %v", ErrIncomplete, err)
		}
		checkpoint = candidate
		report.Processed++
		report.ContinuousSuccessWatermark = checkpoint.ContinuousSuccessWatermark
		report.Items = append(report.Items, item)
	}
	return report, nil
}

func (i *Importer) fail(report Report, checkpoint Checkpoint, options Options, item Item, cause error) (Report, error) {
	report.Failed++
	report.Items = append(report.Items, item)
	if options.SaveCheckpoint != nil {
		candidate, err := checkpoint.WithFailure(FailureRecord{LegacyID: item.LegacyID, SourceKey: item.SourceKey, EnvelopeSHA256: item.EnvelopeSHA256, FailureCode: item.FailureCode, FailedAt: time.Now().UTC()})
		if err == nil {
			if saveErr := options.SaveCheckpoint(candidate); saveErr != nil {
				return report, fmt.Errorf("save failed checkpoint: %w", saveErr)
			}
		}
	}
	return report, fmt.Errorf("%w: %v", ErrIncomplete, cause)
}

func waitForTerminal(ctx context.Context, destination Destination, trackID, sourceKey string, contentLength int, interval time.Duration) (entity.Document, error) {
	for {
		track, err := destination.Track(ctx, trackID)
		if err != nil {
			return entity.Document{}, err
		}
		if track.TrackID != trackID || track.TotalCount != 1 || len(track.Documents) != 1 {
			return entity.Document{}, entity.ErrContract
		}
		document := track.Documents[0]
		if document.TrackID != "" && document.TrackID != trackID || document.SourceKey != sourceKey {
			return entity.Document{}, entity.ErrContract
		}
		switch strings.ToUpper(document.Status) {
		case "PROCESSED":
			if document.DocumentID == "" || document.ContentLength != contentLength {
				return entity.Document{}, entity.ErrContract
			}
			return document, nil
		case "FAILED":
			return entity.Document{}, targetFailedError{}
		case "PENDING", "PARSING", "ANALYZING", "PREPROCESSED", "PROCESSING":
		default:
			return entity.Document{}, entity.ErrContract
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return entity.Document{}, ctx.Err()
		case <-timer.C:
		}
	}
}

type targetFailedError struct{}

func (targetFailedError) Error() string { return "lightrag document failed" }

func classifyFailure(err error) string {
	var targetFailed targetFailedError
	switch {
	case errors.As(err, &targetFailed):
		return "target_failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrManifestMismatch):
		return "manifest_mismatch"
	case errors.Is(err, ErrCheckpointMismatch):
		return "checkpoint_mismatch"
	case errors.Is(err, entity.ErrConflict):
		return "conflict"
	case errors.Is(err, entity.ErrCapacity):
		return "capacity"
	case errors.Is(err, entity.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, entity.ErrContract):
		return "dependency_contract"
	default:
		return "dependency_unavailable"
	}
}
