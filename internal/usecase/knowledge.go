package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

var (
	ErrEmbeddingUnavailable = errors.New("embedding unavailable")
	ErrKnowledgeForbidden   = errors.New("knowledge forbidden")
)

type KnowledgeDocument struct {
	SourceType, Title, SourceURL, GameCode, RegionCode, PatchVersion, ContentText string
}

type KnowledgeSnippet struct {
	ChunkID, DocumentID                                        int64
	Title, GameCode, RegionCode, PatchVersion, SourceURL, Text string
	Score                                                      int
}

type KnowledgeStore interface {
	CreateDocument(context.Context, KnowledgeDocument, []string, [][]float32) (int64, error)
	SearchKeyword(context.Context, string, string, string, int) ([]KnowledgeSnippet, error)
	SearchVector(context.Context, []float32, string, string, int) ([]KnowledgeSnippet, error)
}

type Embedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type Knowledge struct {
	store    KnowledgeStore
	embedder Embedder
}

func NewKnowledge(store KnowledgeStore, embedder Embedder) *Knowledge {
	return &Knowledge{store: store, embedder: embedder}
}

func (k *Knowledge) Create(ctx context.Context, principal auth.Principal, document KnowledgeDocument) (int64, int, error) {
	if principal.UserID <= 0 {
		return 0, 0, auth.ErrUnauthenticated
	}
	if !principal.IsAdmin() {
		return 0, 0, ErrKnowledgeForbidden
	}
	chunks := chunkText(document.ContentText)
	embeddings, err := k.embedder.Embed(ctx, chunks)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 0, 0, err
		}
		embeddings = nil
	}
	id, err := k.store.CreateDocument(ctx, document, chunks, embeddings)
	if err != nil {
		return 0, 0, fmt.Errorf("create knowledge document: %w", err)
	}
	return id, len(chunks), nil
}

func (k *Knowledge) Search(ctx context.Context, query, gameCode, regionCode string, limit int) ([]KnowledgeSnippet, error) {
	query, err := normalizeSearchQuery(query)
	if err != nil {
		return nil, err
	}
	limit = clamp(limit, 1, 10)
	keyword, err := k.store.SearchKeyword(ctx, query, gameCode, regionCode, limit)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	embeddings, err := k.embedder.Embed(ctx, []string{query})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil || len(embeddings) != 1 || len(embeddings[0]) != 1536 {
		return keyword, nil
	}
	vector, err := k.store.SearchVector(ctx, embeddings[0], gameCode, regionCode, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return mergeKnowledge(vector, keyword, limit), nil
}

// SearchEvidence keeps the legacy PostgreSQL retriever behind the same
// read-only port as advanced LightRAG. Its provenance remains explicit and is
// never relabelled as LightRAG.
func (k *Knowledge) SearchEvidence(ctx context.Context, query, gameCode, regionCode, _ string, limit int) ([]Evidence, error) {
	items, err := k.Search(ctx, query, gameCode, regionCode, limit)
	if err != nil {
		return nil, err
	}
	evidence := make([]Evidence, 0, len(items))
	for _, item := range items {
		evidence = append(evidence, Evidence{Source: "legacy_local", Title: item.Title, Content: item.Text, URL: item.SourceURL, Score: float64(item.Score)})
	}
	return evidence, nil
}

func chunkText(value string) []string {
	const maxChunkRunes = 800
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "\r", "")
	lines := strings.Split(normalized, "\n")
	blocks := make([]string, 0, len(lines))
	start := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			if block := strings.TrimSpace(strings.Join(lines[start:i], "\n")); block != "" {
				blocks = append(blocks, block)
			}
			start = i + 1
		}
	}
	if block := strings.TrimSpace(strings.Join(lines[start:], "\n")); block != "" {
		blocks = append(blocks, block)
	}
	chunks := make([]string, 0, len(blocks))
	var current strings.Builder
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		blockRunes := []rune(block)
		if len(blockRunes) > maxChunkRunes {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			for len(blockRunes) > maxChunkRunes {
				chunks = append(chunks, string(blockRunes[:maxChunkRunes]))
				blockRunes = blockRunes[maxChunkRunes:]
			}
			if len(blockRunes) > 0 {
				current.WriteString(string(blockRunes))
			}
			continue
		}
		if current.Len() > 0 && utf8.RuneCountInString(current.String())+len(blockRunes)+2 > maxChunkRunes {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(block)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	if len(chunks) == 0 {
		return []string{normalized}
	}
	return chunks
}

func mergeKnowledge(vector, keyword []KnowledgeSnippet, limit int) []KnowledgeSnippet {
	byID := make(map[int64]KnowledgeSnippet, len(vector)+len(keyword))
	for _, item := range vector {
		byID[item.ChunkID] = item
	}
	for _, item := range keyword {
		if existing, ok := byID[item.ChunkID]; ok {
			if item.Score > existing.Score {
				existing.Score = item.Score
			}
			existing.Score = clamp(existing.Score+10, 0, 100)
			byID[item.ChunkID] = existing
			continue
		}
		byID[item.ChunkID] = item
	}
	items := make([]KnowledgeSnippet, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return items[i].ChunkID > items[j].ChunkID
		}
		return items[i].Score > items[j].Score
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
