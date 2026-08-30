package presenter

import (
	"errors"
	"strings"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

type KnowledgeDocumentRequest struct {
	SourceType   string `json:"sourceType"`
	Title        string `json:"title"`
	SourceURL    string `json:"sourceUrl"`
	GameCode     string `json:"gameCode"`
	RegionCode   string `json:"regionCode"`
	PatchVersion string `json:"patchVersion"`
	ContentText  string `json:"contentText"`
}

type KnowledgeDocumentResponse struct {
	DocumentID int64  `json:"documentId"`
	ChunkCount int    `json:"chunkCount"`
	Title      string `json:"title"`
	GameCode   string `json:"gameCode"`
	RegionCode string `json:"regionCode"`
}

type KnowledgeSnippetResponse struct {
	ChunkID      int64  `json:"chunkId"`
	DocumentID   int64  `json:"documentId"`
	Title        string `json:"title"`
	GameCode     string `json:"gameCode"`
	RegionCode   string `json:"regionCode"`
	PatchVersion string `json:"patchVersion"`
	SourceURL    string `json:"sourceUrl"`
	Snippet      string `json:"snippet"`
	Score        int    `json:"score"`
}

type KnowledgeSearchResponse struct {
	Query string                     `json:"query"`
	Items []KnowledgeSnippetResponse `json:"items"`
}

func (r KnowledgeDocumentRequest) Input() (usecase.KnowledgeDocument, error) {
	if strings.TrimSpace(r.SourceType) == "" {
		return usecase.KnowledgeDocument{}, errors.New("sourceType cannot be blank")
	}
	if strings.TrimSpace(r.Title) == "" {
		return usecase.KnowledgeDocument{}, errors.New("title cannot be blank")
	}
	if strings.TrimSpace(r.ContentText) == "" {
		return usecase.KnowledgeDocument{}, errors.New("contentText cannot be blank")
	}
	return usecase.KnowledgeDocument{SourceType: r.SourceType, Title: r.Title, SourceURL: r.SourceURL, GameCode: r.GameCode, RegionCode: r.RegionCode, PatchVersion: r.PatchVersion, ContentText: r.ContentText}, nil
}

func PresentKnowledge(query string, items []usecase.KnowledgeSnippet) KnowledgeSearchResponse {
	result := make([]KnowledgeSnippetResponse, 0, len(items))
	for _, item := range items {
		result = append(result, KnowledgeSnippetResponse{ChunkID: item.ChunkID, DocumentID: item.DocumentID, Title: item.Title, GameCode: item.GameCode, RegionCode: item.RegionCode, PatchVersion: item.PatchVersion, SourceURL: item.SourceURL, Snippet: item.Text, Score: item.Score})
	}
	return KnowledgeSearchResponse{Query: query, Items: result}
}
