package usecase

import (
	"context"
	"fmt"
)

type WebSearchItem struct {
	Title, URL, Snippet, Source string
}

type WebSearchResult struct {
	Enabled, CacheHit     bool
	Provider, Query, Note string
	Items                 []WebSearchItem
}

type WebSearchClient interface {
	Search(context.Context, string) (WebSearchResult, error)
}

type WebSearch struct{ client WebSearchClient }

func NewWebSearch(client WebSearchClient) *WebSearch { return &WebSearch{client: client} }

func (s *WebSearch) Run(ctx context.Context, query string) (WebSearchResult, error) {
	result, err := s.client.Search(ctx, query)
	if err != nil {
		return WebSearchResult{}, fmt.Errorf("web search: %w", err)
	}
	return result, nil
}
