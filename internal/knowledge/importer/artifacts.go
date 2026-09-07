package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

const (
	artifactSchemaVersion = 1
	maxArtifactBytes      = 128 << 20
	discardedDisposition  = "discarded_not_in_XiaoLanHe-Knowledge-v1"
)

type FieldDispositions struct {
	Metadata    string `json:"metadata"`
	PublishedAt string `json:"publishedAt"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type ManifestDocument struct {
	LegacyID           int64             `json:"legacyId"`
	SourceKey          string            `json:"sourceKey"`
	SourceRowSHA256    string            `json:"sourceRowSha256"`
	EnvelopeSHA256     string            `json:"envelopeSha256"`
	EnvelopeRuneLength int               `json:"envelopeRuneLength"`
	EnvelopeByteLength int               `json:"envelopeByteLength"`
	LegacyChunkCount   int64             `json:"legacyChunkCount"`
	DiscardedFields    FieldDispositions `json:"fieldDispositions"`
}

type Manifest struct {
	SchemaVersion       int                `json:"schemaVersion"`
	ToolVersion         string             `json:"toolVersion"`
	CreatedAt           time.Time          `json:"createdAt"`
	Source              SourceDescriptor   `json:"source"`
	SourceDocumentCount int                `json:"sourceDocumentCount"`
	SourceChunkCount    int64              `json:"sourceChunkCount"`
	Documents           []ManifestDocument `json:"documents"`
	ManifestSHA256      string             `json:"manifestSha256"`
}

func BuildManifest(ctx context.Context, source ManifestSource, pageSize int, createdAt time.Time) (Manifest, error) {
	if source == nil || pageSize < 1 || pageSize > 1000 || createdAt.IsZero() {
		return Manifest{}, ErrInvalidOptions
	}
	descriptor := source.Descriptor()
	if err := validateSourceDescriptor(descriptor); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		SchemaVersion: artifactSchemaVersion, ToolVersion: ToolVersion, CreatedAt: createdAt.UTC(),
		Source: descriptor, Documents: []ManifestDocument{},
	}
	var afterID int64
	for {
		documents, err := source.ListLegacyKnowledge(ctx, afterID, pageSize)
		if err != nil {
			return Manifest{}, err
		}
		if len(documents) > pageSize {
			return Manifest{}, fmt.Errorf("%w: source exceeded requested page size", ErrManifestMismatch)
		}
		if len(documents) == 0 {
			break
		}
		for _, document := range documents {
			if document.ID <= afterID {
				return Manifest{}, fmt.Errorf("%w: source IDs are not strictly increasing", ErrManifestMismatch)
			}
			entry, _, entryErr := manifestEntry(document)
			if entryErr != nil {
				return Manifest{}, fmt.Errorf("legacy document %d: %w", document.ID, entryErr)
			}
			manifest.Documents = append(manifest.Documents, entry)
			manifest.SourceChunkCount += entry.LegacyChunkCount
			afterID = document.ID
		}
	}
	manifest.SourceDocumentCount = len(manifest.Documents)
	manifest.ManifestSHA256 = manifestDigest(manifest)
	return manifest, nil
}

func ValidateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != artifactSchemaVersion || manifest.ToolVersion != ToolVersion || manifest.CreatedAt.IsZero() ||
		manifest.SourceDocumentCount != len(manifest.Documents) || manifest.SourceDocumentCount < 0 || manifest.SourceChunkCount < 0 {
		return ErrManifestMismatch
	}
	if err := validateSourceDescriptor(manifest.Source); err != nil {
		return ErrManifestMismatch
	}
	var lastID, chunks int64
	for _, entry := range manifest.Documents {
		if entry.LegacyID <= lastID || entry.SourceKey != fmt.Sprintf("xlh-legacy-%d.txt", entry.LegacyID) ||
			!validSHA256(entry.SourceRowSHA256) || !validSHA256(entry.EnvelopeSHA256) || entry.EnvelopeRuneLength < 1 || entry.EnvelopeByteLength < 1 || entry.LegacyChunkCount < 0 ||
			entry.DiscardedFields.Metadata != discardedDisposition || entry.DiscardedFields.PublishedAt != discardedDisposition ||
			entry.DiscardedFields.CreatedAt != discardedDisposition || entry.DiscardedFields.UpdatedAt != discardedDisposition {
			return ErrManifestMismatch
		}
		lastID = entry.LegacyID
		chunks += entry.LegacyChunkCount
	}
	if chunks != manifest.SourceChunkCount || manifest.ManifestSHA256 != manifestDigest(manifest) {
		return ErrManifestMismatch
	}
	return nil
}

func VerifySource(ctx context.Context, source ManifestSource, manifest Manifest, pageSize int) error {
	if source == nil || pageSize < 1 || pageSize > 1000 {
		return ErrInvalidOptions
	}
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	descriptor := source.Descriptor()
	if descriptor.IdentitySHA256 != manifest.Source.IdentitySHA256 || descriptor.SchemaSHA256 != manifest.Source.SchemaSHA256 || !descriptor.ReadOnly || descriptor.Isolation != "repeatable_read" {
		return ErrManifestMismatch
	}
	var afterID int64
	position := 0
	for {
		documents, err := source.ListLegacyKnowledge(ctx, afterID, pageSize)
		if err != nil {
			return err
		}
		if len(documents) > pageSize {
			return ErrManifestMismatch
		}
		if len(documents) == 0 {
			break
		}
		for _, document := range documents {
			if position >= len(manifest.Documents) || document.ID <= afterID {
				return ErrManifestMismatch
			}
			entry, _, entryErr := manifestEntry(document)
			if entryErr != nil || !equalManifestDocument(entry, manifest.Documents[position]) {
				return ErrManifestMismatch
			}
			afterID = document.ID
			position++
		}
	}
	if position != len(manifest.Documents) {
		return ErrManifestMismatch
	}
	return nil
}

func manifestEntry(document LegacyDocument) (ManifestDocument, string, error) {
	if document.LegacyChunkCount < 0 || document.CreatedAt.IsZero() || document.UpdatedAt.IsZero() || len(document.Metadata) == 0 || !json.Valid(document.Metadata) {
		return ManifestDocument{}, "", ErrManifestMismatch
	}
	_, sourceKey, envelope, err := normalizeLegacy(document)
	if err != nil {
		return ManifestDocument{}, "", err
	}
	var metadata any
	decoder := json.NewDecoder(bytes.NewReader(document.Metadata))
	decoder.UseNumber()
	if err := decoder.Decode(&metadata); err != nil {
		return ManifestDocument{}, "", ErrManifestMismatch
	}
	metadataCanonical, err := json.Marshal(metadata)
	if err != nil {
		return ManifestDocument{}, "", ErrManifestMismatch
	}
	timestamp := func(value *time.Time) *string {
		if value == nil {
			return nil
		}
		normalized := value.UTC().Format(time.RFC3339Nano)
		return &normalized
	}
	row := struct {
		ID                                                                            int64 `json:"id"`
		SourceType, Title, SourceURL, GameCode, RegionCode, PatchVersion, ContentText string
		Metadata                                                                      json.RawMessage `json:"metadata"`
		PublishedAt                                                                   *string         `json:"publishedAt"`
		CreatedAt, UpdatedAt                                                          string
		LegacyChunkCount                                                              int64 `json:"legacyChunkCount"`
	}{
		ID: document.ID, SourceType: document.Draft.SourceType, Title: document.Draft.Title, SourceURL: document.Draft.SourceURL,
		GameCode: document.Draft.GameCode, RegionCode: document.Draft.RegionCode, PatchVersion: document.Draft.PatchVersion, ContentText: document.Draft.ContentText,
		Metadata: metadataCanonical, PublishedAt: timestamp(document.PublishedAt), CreatedAt: document.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: document.UpdatedAt.UTC().Format(time.RFC3339Nano), LegacyChunkCount: document.LegacyChunkCount,
	}
	rowBytes, _ := json.Marshal(row)
	return ManifestDocument{
		LegacyID: document.ID, SourceKey: sourceKey, SourceRowSHA256: digest(rowBytes), EnvelopeSHA256: digest([]byte(envelope)),
		EnvelopeRuneLength: utf8.RuneCountInString(envelope), EnvelopeByteLength: len(envelope), LegacyChunkCount: document.LegacyChunkCount,
		DiscardedFields: FieldDispositions{Metadata: discardedDisposition, PublishedAt: discardedDisposition, CreatedAt: discardedDisposition, UpdatedAt: discardedDisposition},
	}, envelope, nil
}

func normalizeLegacy(document LegacyDocument) (string, string, string, error) {
	_, sourceKey, envelope, err := entity.NormalizeLegacyDraft(document.ID, document.Draft)
	return "", sourceKey, envelope, err
}

func manifestDocumentsAfter(manifest Manifest, afterID int64, limit int) []ManifestDocument {
	start := sort.Search(len(manifest.Documents), func(index int) bool { return manifest.Documents[index].LegacyID > afterID })
	end := start + limit
	if end > len(manifest.Documents) {
		end = len(manifest.Documents)
	}
	return manifest.Documents[start:end]
}

func equalManifestDocument(left, right ManifestDocument) bool {
	return left == right
}

func manifestDigest(manifest Manifest) string {
	manifest.ManifestSHA256 = ""
	data, _ := json.Marshal(manifest)
	return digest(data)
}

func digest(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func validSHA256(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[7:] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validateSourceDescriptor(source SourceDescriptor) error {
	if !validSHA256(source.IdentitySHA256) || !validSHA256(source.SchemaSHA256) || strings.TrimSpace(source.SnapshotID) == "" ||
		len(source.SnapshotID) > 256 || strings.ContainsAny(source.SnapshotID, "\r\n\t") || source.Isolation != "repeatable_read" || !source.ReadOnly {
		return ErrManifestMismatch
	}
	return nil
}

func WriteManifest(path string, manifest Manifest) error {
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if existing, readErr := LoadManifest(path); readErr == nil {
		if existing.ManifestSHA256 == manifest.ManifestSHA256 {
			return nil
		}
		return ErrManifestMismatch
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	return writeExclusive(path, data)
}

func LoadManifest(path string) (Manifest, error) {
	var manifest Manifest
	if err := readStrictJSON(path, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func writeExclusive(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(path))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".xlh-import-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readStrictJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxArtifactBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxArtifactBytes {
		return ErrManifestMismatch
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrManifestMismatch
	}
	return nil
}
