package importer

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"
)

type SuccessProof struct {
	LegacyID       int64     `json:"legacyId"`
	SourceKey      string    `json:"sourceKey"`
	EnvelopeSHA256 string    `json:"envelopeSha256"`
	TrackID        string    `json:"trackId"`
	DocumentID     string    `json:"documentId"`
	Replayed       bool      `json:"replayed"`
	VerifiedAt     time.Time `json:"verifiedAt"`
}

type FailureRecord struct {
	LegacyID       int64     `json:"legacyId"`
	SourceKey      string    `json:"sourceKey"`
	EnvelopeSHA256 string    `json:"envelopeSha256"`
	TrackID        string    `json:"trackId,omitempty"`
	DocumentID     string    `json:"documentId,omitempty"`
	FailureCode    string    `json:"failureCode"`
	FailedAt       time.Time `json:"failedAt"`
}

type Checkpoint struct {
	SchemaVersion              int             `json:"schemaVersion"`
	ToolVersion                string          `json:"toolVersion"`
	ManifestSHA256             string          `json:"manifestSha256"`
	SourceIdentitySHA256       string          `json:"sourceIdentitySha256"`
	SourceSnapshotID           string          `json:"sourceSnapshotId"`
	SourceSchemaSHA256         string          `json:"sourceSchemaSha256"`
	ContinuousSuccessWatermark int64           `json:"continuousSuccessWatermark"`
	Successes                  []SuccessProof  `json:"successes"`
	Failures                   []FailureRecord `json:"failures"`
	UpdatedAt                  time.Time       `json:"updatedAt"`
	CheckpointSHA256           string          `json:"checkpointSha256"`
}

func NewCheckpoint(manifest Manifest, now time.Time) (Checkpoint, error) {
	if err := ValidateManifest(manifest); err != nil || now.IsZero() {
		return Checkpoint{}, ErrCheckpointMismatch
	}
	checkpoint := Checkpoint{
		SchemaVersion: artifactSchemaVersion, ToolVersion: ToolVersion, ManifestSHA256: manifest.ManifestSHA256,
		SourceIdentitySHA256: manifest.Source.IdentitySHA256, SourceSnapshotID: manifest.Source.SnapshotID,
		SourceSchemaSHA256: manifest.Source.SchemaSHA256, Successes: []SuccessProof{}, Failures: []FailureRecord{}, UpdatedAt: now.UTC(),
	}
	checkpoint.CheckpointSHA256 = checkpointDigest(checkpoint)
	return checkpoint, nil
}

func ValidateCheckpoint(checkpoint Checkpoint, manifest Manifest) error {
	if checkpoint.SchemaVersion != artifactSchemaVersion || checkpoint.ToolVersion != ToolVersion || checkpoint.ManifestSHA256 != manifest.ManifestSHA256 ||
		checkpoint.SourceIdentitySHA256 != manifest.Source.IdentitySHA256 || checkpoint.SourceSnapshotID != manifest.Source.SnapshotID ||
		checkpoint.SourceSchemaSHA256 != manifest.Source.SchemaSHA256 || checkpoint.UpdatedAt.IsZero() || checkpoint.ContinuousSuccessWatermark < 0 ||
		checkpoint.CheckpointSHA256 != checkpointDigest(checkpoint) {
		return ErrCheckpointMismatch
	}
	manifestByID := make(map[int64]ManifestDocument, len(manifest.Documents))
	for _, document := range manifest.Documents {
		manifestByID[document.LegacyID] = document
	}
	successes := make(map[int64]SuccessProof, len(checkpoint.Successes))
	for _, proof := range checkpoint.Successes {
		entry, found := manifestByID[proof.LegacyID]
		if !found || proof.SourceKey != entry.SourceKey || proof.EnvelopeSHA256 != entry.EnvelopeSHA256 || proof.TrackID == "" || proof.DocumentID == "" || proof.VerifiedAt.IsZero() {
			return ErrCheckpointMismatch
		}
		if _, duplicate := successes[proof.LegacyID]; duplicate {
			return ErrCheckpointMismatch
		}
		successes[proof.LegacyID] = proof
	}
	failures := make(map[int64]bool, len(checkpoint.Failures))
	for _, failure := range checkpoint.Failures {
		entry, found := manifestByID[failure.LegacyID]
		if !found || failure.SourceKey != entry.SourceKey || failure.EnvelopeSHA256 != entry.EnvelopeSHA256 || failure.FailureCode == "" || failure.FailedAt.IsZero() || failures[failure.LegacyID] ||
			(failure.FailureCode == "checkpoint_write" && (failure.TrackID == "" || failure.DocumentID == "")) {
			return ErrCheckpointMismatch
		}
		failures[failure.LegacyID] = true
	}
	for legacyID := range failures {
		if legacyID <= checkpoint.ContinuousSuccessWatermark {
			return ErrCheckpointMismatch
		}
	}
	watermark := int64(0)
	for _, entry := range manifest.Documents {
		if entry.LegacyID > checkpoint.ContinuousSuccessWatermark {
			break
		}
		if _, ok := successes[entry.LegacyID]; !ok || failures[entry.LegacyID] {
			return ErrCheckpointMismatch
		}
		watermark = entry.LegacyID
	}
	if watermark != checkpoint.ContinuousSuccessWatermark {
		return ErrCheckpointMismatch
	}
	return nil
}

func (checkpoint Checkpoint) ReplayProof(entry ManifestDocument) (SuccessProof, bool) {
	for _, proof := range checkpoint.Successes {
		if proof.LegacyID == entry.LegacyID && proof.SourceKey == entry.SourceKey && proof.EnvelopeSHA256 == entry.EnvelopeSHA256 && proof.TrackID != "" && proof.DocumentID != "" && !proof.VerifiedAt.IsZero() {
			return proof, true
		}
	}
	for _, failure := range checkpoint.Failures {
		if failure.LegacyID == entry.LegacyID && failure.SourceKey == entry.SourceKey && failure.EnvelopeSHA256 == entry.EnvelopeSHA256 && failure.FailureCode == "checkpoint_write" && failure.TrackID != "" && failure.DocumentID != "" && !failure.FailedAt.IsZero() {
			return SuccessProof{LegacyID: failure.LegacyID, SourceKey: failure.SourceKey, EnvelopeSHA256: failure.EnvelopeSHA256, TrackID: failure.TrackID, DocumentID: failure.DocumentID, VerifiedAt: failure.FailedAt}, true
		}
	}
	return SuccessProof{}, false
}

func (checkpoint Checkpoint) WithSuccess(manifest Manifest, proof SuccessProof) (Checkpoint, error) {
	if err := ValidateCheckpoint(checkpoint, manifest); err != nil {
		return Checkpoint{}, err
	}
	next := manifestDocumentsAfter(manifest, checkpoint.ContinuousSuccessWatermark, 1)
	if len(next) != 1 || proof.LegacyID != next[0].LegacyID || proof.SourceKey != next[0].SourceKey || proof.EnvelopeSHA256 != next[0].EnvelopeSHA256 || proof.TrackID == "" || proof.DocumentID == "" || proof.VerifiedAt.IsZero() {
		return Checkpoint{}, ErrCheckpointMismatch
	}
	checkpoint.Successes = appendOrReplaceSuccess(checkpoint.Successes, proof)
	checkpoint.Failures = removeFailure(checkpoint.Failures, proof.LegacyID)
	checkpoint.ContinuousSuccessWatermark = proof.LegacyID
	checkpoint.UpdatedAt = proof.VerifiedAt.UTC()
	checkpoint.CheckpointSHA256 = checkpointDigest(checkpoint)
	return checkpoint, ValidateCheckpoint(checkpoint, manifest)
}

func (checkpoint Checkpoint) WithFailure(failure FailureRecord) (Checkpoint, error) {
	if failure.LegacyID <= checkpoint.ContinuousSuccessWatermark || failure.SourceKey == "" || !validSHA256(failure.EnvelopeSHA256) || failure.FailureCode == "" || failure.FailedAt.IsZero() ||
		(failure.FailureCode == "checkpoint_write" && (failure.TrackID == "" || failure.DocumentID == "")) {
		return Checkpoint{}, ErrCheckpointMismatch
	}
	checkpoint.Failures = appendOrReplaceFailure(checkpoint.Failures, failure)
	checkpoint.UpdatedAt = failure.FailedAt.UTC()
	checkpoint.CheckpointSHA256 = checkpointDigest(checkpoint)
	return checkpoint, nil
}

func appendOrReplaceSuccess(values []SuccessProof, value SuccessProof) []SuccessProof {
	result := append([]SuccessProof(nil), values...)
	for index := range result {
		if result[index].LegacyID == value.LegacyID {
			result[index] = value
			sort.Slice(result, func(left, right int) bool { return result[left].LegacyID < result[right].LegacyID })
			return result
		}
	}
	result = append(result, value)
	sort.Slice(result, func(left, right int) bool { return result[left].LegacyID < result[right].LegacyID })
	return result
}

func appendOrReplaceFailure(values []FailureRecord, value FailureRecord) []FailureRecord {
	result := append([]FailureRecord(nil), values...)
	for index := range result {
		if result[index].LegacyID == value.LegacyID {
			result[index] = value
			sort.Slice(result, func(left, right int) bool { return result[left].LegacyID < result[right].LegacyID })
			return result
		}
	}
	result = append(result, value)
	sort.Slice(result, func(left, right int) bool { return result[left].LegacyID < result[right].LegacyID })
	return result
}

func removeFailure(values []FailureRecord, legacyID int64) []FailureRecord {
	result := make([]FailureRecord, 0, len(values))
	for _, value := range values {
		if value.LegacyID != legacyID {
			result = append(result, value)
		}
	}
	return result
}

func checkpointDigest(checkpoint Checkpoint) string {
	checkpoint.CheckpointSHA256 = ""
	data, _ := json.Marshal(checkpoint)
	return digest(data)
}

func SaveCheckpoint(path string, checkpoint Checkpoint, manifest Manifest) error {
	if err := ValidateCheckpoint(checkpoint, manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func LoadOrCreateCheckpoint(path string, manifest Manifest, now time.Time) (Checkpoint, error) {
	var checkpoint Checkpoint
	if err := readStrictJSON(path, &checkpoint); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Checkpoint{}, err
		}
		return NewCheckpoint(manifest, now)
	}
	if err := ValidateCheckpoint(checkpoint, manifest); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}
