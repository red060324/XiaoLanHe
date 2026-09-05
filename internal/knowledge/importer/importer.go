package importer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

var (
	ErrInvalidOptions = errors.New("invalid legacy knowledge import options")
	ErrIncomplete     = errors.New("legacy knowledge import incomplete")
)

type LegacyDocument struct {
	ID    int64
	Draft entity.DocumentDraft
}

type Source interface {
	ListLegacyKnowledge(context.Context, int64, int) ([]LegacyDocument, error)
}

type Destination interface {
	Create(context.Context, string, string) (entity.AcceptedDocument, error)
	Track(context.Context, string) (entity.Track, error)
}

type Options struct {
	Execute            bool
	AfterID            int64
	Limit              int
	PollInterval       time.Duration
	PerDocumentTimeout time.Duration
}

type Item struct {
	LegacyID    int64  `json:"legacyId"`
	SourceKey   string `json:"sourceKey,omitempty"`
	TrackID     string `json:"trackId,omitempty"`
	DocumentID  string `json:"documentId,omitempty"`
	Status      string `json:"status"`
	FailureCode string `json:"failureCode,omitempty"`
	Replayed    bool   `json:"replayed,omitempty"`
}

type Report struct {
	DryRun    bool   `json:"dryRun"`
	AfterID   int64  `json:"afterId"`
	LastID    int64  `json:"lastId"`
	Scanned   int    `json:"scanned"`
	Submitted int    `json:"submitted"`
	Replayed  int    `json:"replayed"`
	Processed int    `json:"processed"`
	Failed    int    `json:"failed"`
	Items     []Item `json:"items"`
}

type Importer struct {
	source      Source
	destination Destination
}

func New(source Source, destination Destination) (*Importer, error) {
	if source == nil {
		return nil, ErrInvalidOptions
	}
	return &Importer{source: source, destination: destination}, nil
}

func (i *Importer) Run(ctx context.Context, options Options) (Report, error) {
	if options.AfterID < 0 || options.Limit < 1 || options.Limit > 100 || options.PollInterval <= 0 || options.PerDocumentTimeout <= 0 || options.Execute && i.destination == nil {
		return Report{}, ErrInvalidOptions
	}
	report := Report{DryRun: !options.Execute, AfterID: options.AfterID, LastID: options.AfterID, Items: []Item{}}
	documents, err := i.source.ListLegacyKnowledge(ctx, options.AfterID, options.Limit)
	if err != nil {
		return report, err
	}
	for _, document := range documents {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Scanned++
		report.LastID = document.ID
		_, sourceKey, envelope, normalizeErr := entity.NormalizeLegacyDraft(document.ID, document.Draft)
		item := Item{LegacyID: document.ID, SourceKey: sourceKey, Status: "validated"}
		if normalizeErr != nil {
			item.Status, item.FailureCode = "failed", "invalid_legacy_document"
			report.Failed++
			report.Items = append(report.Items, item)
			continue
		}
		if !options.Execute {
			report.Items = append(report.Items, item)
			continue
		}
		documentCtx, cancel := context.WithTimeout(ctx, options.PerDocumentTimeout)
		accepted, createErr := i.destination.Create(documentCtx, sourceKey, envelope)
		if createErr != nil {
			cancel()
			item.Status, item.FailureCode = "failed", classifyFailure(createErr)
			report.Failed++
			report.Items = append(report.Items, item)
			continue
		}
		report.Submitted++
		item.TrackID, item.Replayed = accepted.TrackID, accepted.Replayed
		if accepted.Replayed {
			report.Replayed++
		}
		tracked, trackErr := waitForTerminal(documentCtx, i.destination, accepted.TrackID, options.PollInterval)
		cancel()
		if trackErr != nil {
			item.Status, item.FailureCode = "failed", classifyFailure(trackErr)
			report.Failed++
		} else {
			item.Status, item.DocumentID = "processed", tracked.DocumentID
			report.Processed++
		}
		report.Items = append(report.Items, item)
	}
	if report.Failed > 0 {
		return report, ErrIncomplete
	}
	return report, nil
}

func waitForTerminal(ctx context.Context, destination Destination, trackID string, interval time.Duration) (entity.Document, error) {
	for {
		track, err := destination.Track(ctx, trackID)
		if err != nil {
			return entity.Document{}, err
		}
		for _, document := range track.Documents {
			switch strings.ToUpper(document.Status) {
			case "PROCESSED":
				return document, nil
			case "FAILED":
				return entity.Document{}, ErrIncomplete
			}
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

func classifyFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
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
