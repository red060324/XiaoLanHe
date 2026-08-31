package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var ErrInvalidSearchQuery = errors.New("invalid search query")

const maxSearchQueryRunes = 100

type WebSearchItem struct {
	Title, URL, Snippet, Source string
}

type WebSearchResult struct {
	Enabled               bool
	Provider, Query, Note string
	Items                 []WebSearchItem
}

type WebSearchClient interface {
	Search(context.Context, string) (WebSearchResult, error)
}

type WebSearch struct{ client WebSearchClient }

func NewWebSearch(client WebSearchClient) *WebSearch { return &WebSearch{client: client} }

func (s *WebSearch) Run(ctx context.Context, query string) (WebSearchResult, error) {
	query, err := normalizeSearchQuery(query)
	if err != nil {
		return WebSearchResult{}, err
	}
	result, err := s.client.Search(ctx, query)
	if err != nil {
		return WebSearchResult{}, fmt.Errorf("web search: %w", err)
	}
	return result, nil
}

func normalizeSearchQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > maxSearchQueryRunes {
		return "", ErrInvalidSearchQuery
	}
	return query, nil
}
