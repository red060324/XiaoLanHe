package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidInput = errors.New("invalid knowledge input")
	ErrNotFound     = errors.New("knowledge resource not found")
	ErrConflict     = errors.New("knowledge conflict")
	ErrCapacity     = errors.New("knowledge capacity exceeded")
	ErrContract     = errors.New("knowledge dependency contract mismatch")
	ErrUnavailable  = errors.New("knowledge dependency unavailable")
)

type Mode string

const (
	ModeLocal  Mode = "local"
	ModeGlobal Mode = "global"
	ModeHybrid Mode = "hybrid"
	ModeMix    Mode = "mix"
)

func (m Mode) Valid() bool {
	return m == ModeLocal || m == ModeGlobal || m == ModeHybrid || m == ModeMix
}

type DocumentDraft struct {
	SourceType, Title, SourceURL, GameCode, RegionCode, PatchVersion, ContentText string
}

type AcceptedDocument struct {
	TrackID, SourceKey, Status string
	Replayed                   bool
}

type Evidence struct {
	EvidenceID  string
	Kind        string
	Text        string
	SourceKey   string
	ReferenceID string
	Attributes  map[string]string
}

type SearchInput struct {
	Query                string
	Mode                 Mode
	GameCode, RegionCode string
	Limit                int
}

type SearchResult struct {
	Query    string
	Provider string
	Mode     Mode
	Items    []Evidence
}

type Document struct {
	DocumentID, SourceKey, Status string
	ContentLength, ChunksCount    int
	CreatedAt, UpdatedAt          time.Time
	FailureCode                   string
	TrackID                       string
}

type Track struct {
	TrackID      string
	Documents    []Document
	TotalCount   int
	StatusCounts map[string]int
}

type DocumentList struct {
	Items                  []Document
	Page, PageSize         int
	TotalCount, TotalPages int
}

type ListInput struct {
	Page, PageSize                   int
	Status, SortField, SortDirection string
}

type DeleteResult struct {
	DocumentID, Status string
}

type Health struct {
	CoreVersion, APIVersion, Workspace, WorkingDirectory     string
	KVStorage, VectorStorage, GraphStorage, DocStatusStorage string
	ServerMode                                               string
	Workers                                                  int
	PipelineActive, RecoveryRequired                         bool
}

var managedSourcePattern = regexp.MustCompile(`^(xlh-[0-9a-f]{64}\.txt|xlh-legacy-[1-9][0-9]*\.txt)$`)

func IsManagedSource(value string) bool {
	value = strings.TrimSpace(value)
	return value == path.Base(value) && managedSourcePattern.MatchString(value)
}

func NormalizeDraft(draft DocumentDraft) (DocumentDraft, string, string, error) {
	draft.SourceType = strings.TrimSpace(draft.SourceType)
	draft.Title = strings.TrimSpace(draft.Title)
	draft.SourceURL = strings.TrimSpace(draft.SourceURL)
	draft.GameCode = strings.TrimSpace(draft.GameCode)
	draft.RegionCode = strings.TrimSpace(draft.RegionCode)
	draft.PatchVersion = strings.TrimSpace(draft.PatchVersion)
	draft.ContentText = strings.TrimSpace(draft.ContentText)
	fields := []struct {
		value    string
		min, max int
	}{
		{draft.SourceType, 1, 32}, {draft.Title, 1, 512}, {draft.SourceURL, 0, 2048},
		{draft.GameCode, 0, 64}, {draft.RegionCode, 0, 32}, {draft.PatchVersion, 0, 64},
		{draft.ContentText, 1, 1 << 20},
	}
	for _, field := range fields {
		count := utf8.RuneCountInString(field.value)
		if count < field.min || count > field.max {
			return DocumentDraft{}, "", "", ErrInvalidInput
		}
	}
	for _, value := range []string{draft.SourceType, draft.Title, draft.SourceURL, draft.GameCode, draft.RegionCode, draft.PatchVersion} {
		if hasUnsafeHeaderRune(value) {
			return DocumentDraft{}, "", "", ErrInvalidInput
		}
	}
	if draft.SourceURL != "" {
		parsed, err := url.ParseRequestURI(draft.SourceURL)
		if err != nil || parsed.User != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return DocumentDraft{}, "", "", ErrInvalidInput
		}
	}
	canonical, _ := json.Marshal(struct {
		SourceType, Title, SourceURL, GameCode, RegionCode, PatchVersion, ContentText string
	}{draft.SourceType, draft.Title, draft.SourceURL, draft.GameCode, draft.RegionCode, draft.PatchVersion, draft.ContentText})
	sum := sha256.Sum256(canonical)
	sourceKey := "xlh-" + hex.EncodeToString(sum[:]) + ".txt"
	var envelope strings.Builder
	envelope.WriteString("XiaoLanHe-Knowledge-v1\n")
	writeHeader := func(name, value string) { envelope.WriteString(name + ": " + value + "\n") }
	writeHeader("Title", draft.Title)
	writeHeader("Source-Type", draft.SourceType)
	writeHeader("Source-URL", draft.SourceURL)
	writeHeader("Game-Code", draft.GameCode)
	writeHeader("Region-Code", draft.RegionCode)
	writeHeader("Patch-Version", draft.PatchVersion)
	envelope.WriteString("\n")
	envelope.WriteString(draft.ContentText)
	return draft, sourceKey, envelope.String(), nil
}

// NormalizeLegacyDraft validates a legacy row with the same rules as a normal
// document, but gives the one-time importer a stable, resumable source key.
func NormalizeLegacyDraft(id int64, draft DocumentDraft) (DocumentDraft, string, string, error) {
	if id <= 0 {
		return DocumentDraft{}, "", "", ErrInvalidInput
	}
	normalized, _, envelope, err := NormalizeDraft(draft)
	if err != nil {
		return DocumentDraft{}, "", "", err
	}
	return normalized, "xlh-legacy-" + strconv.FormatInt(id, 10) + ".txt", envelope, nil
}

func hasUnsafeHeaderRune(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r == '\r' || r == '\n' || unicode.IsControl(r) }) >= 0
}
