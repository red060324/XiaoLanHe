package usecase

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKnowledgeCreate(t *testing.T) {
	document := KnowledgeDocument{SourceType: "note", Title: "title", ContentText: "content"}
	t.Run("falls back to chunks without embeddings", func(t *testing.T) {
		store := &knowledgeStoreFake{id: 7}
		knowledge := NewKnowledge(store, embedderFunc(func(context.Context, []string) ([][]float32, error) { return nil, ErrEmbeddingUnavailable }))
		id, count, err := knowledge.Create(context.Background(), document)
		if err != nil || id != 7 || count != 1 || store.createCalls != 1 || len(store.embeddings) != 0 {
			t.Fatalf("id=%d count=%d calls=%d embeddings=%d err=%v", id, count, store.createCalls, len(store.embeddings), err)
		}
	})
	t.Run("does not persist after request cancellation", func(t *testing.T) {
		store := &knowledgeStoreFake{id: 7}
		knowledge := NewKnowledge(store, embedderFunc(func(context.Context, []string) ([][]float32, error) { return nil, context.Canceled }))
		_, _, err := knowledge.Create(context.Background(), document)
		if !errors.Is(err, context.Canceled) || store.createCalls != 0 {
			t.Fatalf("calls=%d err=%v", store.createCalls, err)
		}
	})
}

func TestKnowledgeSearch(t *testing.T) {
	t.Run("merges keyword and vector results within the limit", func(t *testing.T) {
		keyword := []KnowledgeSnippet{{ChunkID: 1, Score: 20}, {ChunkID: 2, Score: 30}}
		vector := []KnowledgeSnippet{{ChunkID: 1, Score: 50}, {ChunkID: 3, Score: 40}}
		store := &knowledgeStoreFake{keyword: keyword, vector: vector}
		embedding := make([]float32, 1536)
		items, err := NewKnowledge(store, embedderFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{embedding}, nil })).Search(context.Background(), "q", "g", "r", 99)
		if err != nil || len(items) != 3 || items[0].ChunkID != 1 || items[0].Score != 60 || store.limit != 10 {
			t.Fatalf("items=%#v limit=%d err=%v", items, store.limit, err)
		}
	})

	t.Run("normalizes the query before dependencies", func(t *testing.T) {
		store := &knowledgeStoreFake{}
		var embedded string
		knowledge := NewKnowledge(store, embedderFunc(func(_ context.Context, values []string) ([][]float32, error) {
			embedded = values[0]
			return nil, ErrEmbeddingUnavailable
		}))
		if _, err := knowledge.Search(context.Background(), "  guide  ", "g", "r", 5); err != nil {
			t.Fatal(err)
		}
		if store.query != "guide" || embedded != "guide" {
			t.Fatalf("keyword query=%q embedding query=%q", store.query, embedded)
		}
	})

	for name, contextErr := range map[string]error{"cancellation": context.Canceled, "deadline": context.DeadlineExceeded} {
		t.Run("propagates request "+name, func(t *testing.T) {
			store := &knowledgeStoreFake{keyword: []KnowledgeSnippet{{ChunkID: 1, Score: 20}}}
			knowledge := NewKnowledge(store, embedderFunc(func(context.Context, []string) ([][]float32, error) {
				return nil, contextErr
			}))
			items, err := knowledge.Search(context.Background(), "guide", "g", "r", 5)
			if !errors.Is(err, contextErr) || items != nil || store.vectorCalls != 0 {
				t.Fatalf("items=%#v vector calls=%d err=%v", items, store.vectorCalls, err)
			}
		})
	}

	for name, query := range map[string]string{"blank": " \t ", "too long": strings.Repeat("游", 101)} {
		t.Run("rejects "+name+" query", func(t *testing.T) {
			store := &knowledgeStoreFake{}
			embedCalls := 0
			knowledge := NewKnowledge(store, embedderFunc(func(context.Context, []string) ([][]float32, error) {
				embedCalls++
				return nil, nil
			}))
			if _, err := knowledge.Search(context.Background(), query, "g", "r", 5); err == nil || store.searchCalls != 0 || embedCalls != 0 {
				t.Fatalf("search calls=%d embed calls=%d err=%v", store.searchCalls, embedCalls, err)
			}
		})
	}
}

func TestChunkText(t *testing.T) {
	t.Run("keeps short content together", func(t *testing.T) {
		if got := chunkText("first line\nsecond line"); !slices.Equal(got, []string{"first line\nsecond line"}) {
			t.Fatalf("chunks=%q", got)
		}
	})
	t.Run("bounds a long paragraph by runes", func(t *testing.T) {
		input := strings.Repeat("游", 801)
		got := chunkText(input)
		if len(got) != 2 {
			t.Fatalf("chunk count=%d", len(got))
		}
		if utf8.RuneCountInString(got[0]) != 800 || utf8.RuneCountInString(got[1]) != 1 || strings.Join(got, "") != input {
			t.Fatalf("rune lengths=%v", []int{utf8.RuneCountInString(got[0]), utf8.RuneCountInString(got[1])})
		}
	})
}

func TestMergeKnowledge(t *testing.T) {
	got := mergeKnowledge([]KnowledgeSnippet{{ChunkID: 1, Score: 50}, {ChunkID: 2, Score: 40}}, []KnowledgeSnippet{{ChunkID: 1, Score: 30}, {ChunkID: 3, Score: 60}}, 2)
	if len(got) != 2 || got[0].ChunkID != 3 || got[0].Score != 60 || got[1].ChunkID != 1 {
		t.Fatalf("items=%#v", got)
	}
}

type embedderFunc func(context.Context, []string) ([][]float32, error)

func (f embedderFunc) Embed(ctx context.Context, input []string) ([][]float32, error) {
	return f(ctx, input)
}

type knowledgeStoreFake struct {
	id                                           int64
	createCalls, searchCalls, vectorCalls, limit int
	query                                        string
	embeddings                                   [][]float32
	keyword, vector                              []KnowledgeSnippet
}

func (s *knowledgeStoreFake) CreateDocument(_ context.Context, _ KnowledgeDocument, _ []string, embeddings [][]float32) (int64, error) {
	s.createCalls++
	s.embeddings = embeddings
	return s.id, nil
}
func (s *knowledgeStoreFake) SearchKeyword(_ context.Context, query string, _, _ string, limit int) ([]KnowledgeSnippet, error) {
	s.searchCalls++
	s.query = query
	s.limit = limit
	return s.keyword, nil
}
func (s *knowledgeStoreFake) SearchVector(_ context.Context, _ []float32, _, _ string, _ int) ([]KnowledgeSnippet, error) {
	s.vectorCalls++
	return s.vector, nil
}
