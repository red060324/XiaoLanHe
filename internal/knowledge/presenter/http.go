package presenter

import (
	"strconv"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

type DocumentRequest struct {
	SourceType   string `json:"sourceType"`
	Title        string `json:"title"`
	SourceURL    string `json:"sourceUrl"`
	GameCode     string `json:"gameCode"`
	RegionCode   string `json:"regionCode"`
	PatchVersion string `json:"patchVersion"`
	ContentText  string `json:"contentText"`
}

func (r DocumentRequest) Draft() entity.DocumentDraft {
	return entity.DocumentDraft{SourceType: r.SourceType, Title: r.Title, SourceURL: r.SourceURL, GameCode: r.GameCode, RegionCode: r.RegionCode, PatchVersion: r.PatchVersion, ContentText: r.ContentText}
}

type AcceptedResponse struct {
	TrackID   string `json:"trackId"`
	SourceKey string `json:"sourceKey"`
	Status    string `json:"status"`
	Replayed  bool   `json:"replayed,omitempty"`
}
type EvidenceResponse struct {
	EvidenceID  string            `json:"evidenceId"`
	Kind        string            `json:"kind"`
	Text        string            `json:"text"`
	SourceKey   string            `json:"sourceKey"`
	ReferenceID string            `json:"referenceId,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}
type SearchResponse struct {
	Query    string             `json:"query"`
	Provider string             `json:"provider"`
	Mode     string             `json:"mode"`
	Items    []EvidenceResponse `json:"items"`
}
type DocumentResponse struct {
	DocumentID    string  `json:"documentId"`
	SourceKey     string  `json:"sourceKey"`
	Status        string  `json:"status"`
	ContentLength int     `json:"contentLength"`
	ChunksCount   int     `json:"chunksCount"`
	CreatedAt     string  `json:"createdAt,omitempty"`
	UpdatedAt     string  `json:"updatedAt,omitempty"`
	FailureCode   *string `json:"failureCode"`
}
type TrackResponse struct {
	TrackID      string             `json:"trackId"`
	Documents    []DocumentResponse `json:"documents"`
	TotalCount   int                `json:"totalCount"`
	StatusCounts map[string]int     `json:"statusCounts"`
}
type ListResponse struct {
	Items      []DocumentResponse `json:"items"`
	Page       int                `json:"page"`
	PageSize   int                `json:"pageSize"`
	TotalCount int                `json:"totalCount"`
	TotalPages int                `json:"totalPages"`
}

func PresentAccepted(value entity.AcceptedDocument) AcceptedResponse {
	return AcceptedResponse{TrackID: value.TrackID, SourceKey: value.SourceKey, Status: value.Status, Replayed: value.Replayed}
}
func PresentSearch(value entity.SearchResult) SearchResponse {
	items := make([]EvidenceResponse, len(value.Items))
	for i, item := range value.Items {
		items[i] = EvidenceResponse{EvidenceID: item.EvidenceID, Kind: item.Kind, Text: item.Text, SourceKey: item.SourceKey, ReferenceID: item.ReferenceID, Attributes: item.Attributes}
	}
	return SearchResponse{Query: value.Query, Provider: value.Provider, Mode: string(value.Mode), Items: items}
}
func PresentTrack(value entity.Track) TrackResponse {
	return TrackResponse{TrackID: value.TrackID, Documents: presentDocuments(value.Documents), TotalCount: value.TotalCount, StatusCounts: value.StatusCounts}
}
func PresentList(value entity.DocumentList) ListResponse {
	return ListResponse{Items: presentDocuments(value.Items), Page: value.Page, PageSize: value.PageSize, TotalCount: value.TotalCount, TotalPages: value.TotalPages}
}
func presentDocuments(values []entity.Document) []DocumentResponse {
	result := make([]DocumentResponse, len(values))
	for i, value := range values {
		var failure *string
		if value.FailureCode != "" {
			copy := value.FailureCode
			failure = &copy
		}
		result[i] = DocumentResponse{DocumentID: value.DocumentID, SourceKey: value.SourceKey, Status: value.Status, ContentLength: value.ContentLength, ChunksCount: value.ChunksCount, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt), FailureCode: failure}
	}
	return result
}
func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func PositiveInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, entity.ErrInvalidInput
	}
	return parsed, nil
}
