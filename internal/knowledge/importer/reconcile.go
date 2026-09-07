package importer

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

type TargetPage struct {
	Documents []entity.Document
	Page      int
	PageSize  int
	RawCount  int
	RawTotal  int
	HasNext   bool
}

// ReconciliationDestination is implemented by the migration-only LightRAG API
// adapter. Page is deliberately exposed so verification is not limited by the
// normal application's bounded managed-document listing.
type ReconciliationDestination interface {
	ListPage(context.Context, int, int) (TargetPage, error)
	Health(context.Context) (entity.Health, error)
}

type FailedTarget struct {
	SourceKey   string `json:"sourceKey"`
	FailureCode string `json:"failureCode"`
}

type ReconciliationReport struct {
	SchemaVersion              int            `json:"schemaVersion"`
	ToolVersion                string         `json:"toolVersion"`
	CreatedAt                  time.Time      `json:"createdAt"`
	ManifestSHA256             string         `json:"manifestSha256"`
	CheckpointSHA256           string         `json:"checkpointSha256"`
	ContinuousSuccessWatermark int64          `json:"continuousSuccessWatermark"`
	SourceDocuments            int            `json:"sourceDocuments"`
	SourceChunks               int64          `json:"sourceChunks"`
	Submitted                  int            `json:"submitted"`
	Replayed                   int            `json:"replayed"`
	Processed                  int            `json:"processed"`
	Failed                     int            `json:"failed"`
	TargetDocuments            int            `json:"targetDocuments"`
	TargetChunks               int64          `json:"targetChunks"`
	StatusDistribution         map[string]int `json:"statusDistribution"`
	MissingSourceKeys          []string       `json:"missingSourceKeys"`
	UnexpectedSourceKeys       []string       `json:"unexpectedSourceKeys"`
	LengthMismatches           []string       `json:"lengthMismatches"`
	FailedTargets              []FailedTarget `json:"failedTargets"`
	PipelineIdle               bool           `json:"pipelineIdle"`
	RecoveryRequired           bool           `json:"recoveryRequired"`
	ExactSourceKeySet          bool           `json:"exactSourceKeySet"`
	UniqueTargets              bool           `json:"uniqueTargets"`
	AllProcessed               bool           `json:"allProcessed"`
	DigestProofsComplete       bool           `json:"digestProofsComplete"`
	ImportGateComplete         bool           `json:"importGateComplete"`
	ReportSHA256               string         `json:"reportSha256"`
}

func Reconcile(ctx context.Context, manifest Manifest, checkpoint Checkpoint, destination ReconciliationDestination, pageSize int, createdAt time.Time) (ReconciliationReport, error) {
	report := ReconciliationReport{
		SchemaVersion: artifactSchemaVersion, ToolVersion: ToolVersion, CreatedAt: createdAt.UTC(), ManifestSHA256: manifest.ManifestSHA256, CheckpointSHA256: checkpoint.CheckpointSHA256,
		ContinuousSuccessWatermark: checkpoint.ContinuousSuccessWatermark, SourceDocuments: manifest.SourceDocumentCount, SourceChunks: manifest.SourceChunkCount,
		StatusDistribution: map[string]int{}, MissingSourceKeys: []string{}, UnexpectedSourceKeys: []string{}, LengthMismatches: []string{}, FailedTargets: []FailedTarget{}, UniqueTargets: true,
	}
	if err := ValidateManifest(manifest); err != nil || ValidateCheckpoint(checkpoint, manifest) != nil || destination == nil || pageSize < 1 || pageSize > 1000 || createdAt.IsZero() {
		return report, ErrInvalidOptions
	}
	for _, proof := range checkpoint.Successes {
		report.Processed++
		if proof.Replayed {
			report.Replayed++
		} else {
			report.Submitted++
		}
	}
	report.Failed = len(checkpoint.Failures)
	health, err := destination.Health(ctx)
	if err != nil {
		return report, err
	}
	report.PipelineIdle = !health.PipelineActive
	report.RecoveryRequired = health.RecoveryRequired
	expected := make(map[string]ManifestDocument, len(manifest.Documents))
	for _, entry := range manifest.Documents {
		expected[entry.SourceKey] = entry
	}
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	expectedRawTotal, rawSeen := -1, 0
	for page := 1; ; page++ {
		result, listErr := destination.ListPage(ctx, page, pageSize)
		if listErr != nil {
			return report, listErr
		}
		if result.Page != page || result.PageSize != pageSize || result.RawTotal < 0 || result.RawCount < 0 || result.RawCount > pageSize || len(result.Documents) > result.RawCount || result.HasNext != (page*pageSize < result.RawTotal) {
			return report, entity.ErrContract
		}
		if expectedRawTotal < 0 {
			expectedRawTotal = result.RawTotal
		} else if result.RawTotal != expectedRawTotal {
			return report, entity.ErrConflict
		}
		rawSeen += result.RawCount
		for _, document := range result.Documents {
			report.TargetDocuments++
			report.TargetChunks += int64(document.ChunksCount)
			status := strings.ToUpper(document.Status)
			report.StatusDistribution[status]++
			if document.DocumentID == "" || document.SourceKey == "" || seenIDs[document.DocumentID] || seenKeys[document.SourceKey] {
				report.UniqueTargets = false
			}
			seenIDs[document.DocumentID], seenKeys[document.SourceKey] = true, true
			entry, found := expected[document.SourceKey]
			if !found {
				report.UnexpectedSourceKeys = append(report.UnexpectedSourceKeys, document.SourceKey)
			} else if document.ContentLength != entry.EnvelopeRuneLength {
				report.LengthMismatches = append(report.LengthMismatches, document.SourceKey)
			}
			if status != "PROCESSED" {
				report.FailedTargets = append(report.FailedTargets, FailedTarget{SourceKey: document.SourceKey, FailureCode: targetFailureCode(document)})
			}
		}
		if !result.HasNext {
			if rawSeen != expectedRawTotal {
				return report, entity.ErrContract
			}
			break
		}
	}
	for sourceKey := range expected {
		if !seenKeys[sourceKey] {
			report.MissingSourceKeys = append(report.MissingSourceKeys, sourceKey)
		}
	}
	sort.Strings(report.MissingSourceKeys)
	sort.Strings(report.UnexpectedSourceKeys)
	sort.Strings(report.LengthMismatches)
	sort.Slice(report.FailedTargets, func(left, right int) bool {
		return report.FailedTargets[left].SourceKey < report.FailedTargets[right].SourceKey
	})
	report.ExactSourceKeySet = len(report.MissingSourceKeys) == 0 && len(report.UnexpectedSourceKeys) == 0 && report.TargetDocuments == manifest.SourceDocumentCount
	report.AllProcessed = report.StatusDistribution["PROCESSED"] == report.TargetDocuments && len(report.FailedTargets) == 0
	lastManifestID := int64(0)
	if len(manifest.Documents) > 0 {
		lastManifestID = manifest.Documents[len(manifest.Documents)-1].LegacyID
	}
	report.DigestProofsComplete = checkpoint.ContinuousSuccessWatermark == lastManifestID && report.Processed == manifest.SourceDocumentCount && report.Failed == 0
	report.ImportGateComplete = report.ExactSourceKeySet && report.UniqueTargets && report.AllProcessed && report.DigestProofsComplete && len(report.LengthMismatches) == 0 && report.PipelineIdle && !report.RecoveryRequired
	report.ReportSHA256 = reconciliationDigest(report)
	if !report.ImportGateComplete {
		return report, ErrIncomplete
	}
	return report, nil
}

func reconciliationDigest(report ReconciliationReport) string {
	report.ReportSHA256 = ""
	data, _ := json.Marshal(report)
	return digest(data)
}

func WriteReconciliationReport(path string, report ReconciliationReport) error {
	if report.SchemaVersion != artifactSchemaVersion || report.ToolVersion != ToolVersion || report.CreatedAt.IsZero() || !validSHA256(report.ManifestSHA256) || !validSHA256(report.CheckpointSHA256) || report.ReportSHA256 != reconciliationDigest(report) {
		return ErrManifestMismatch
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return writeExclusive(path, append(data, '\n'))
}

func targetFailureCode(document entity.Document) string {
	if document.FailureCode != "" {
		return document.FailureCode
	}
	status := strings.ToLower(strings.TrimSpace(document.Status))
	if status == "" {
		return "unknown_status"
	}
	return status
}
