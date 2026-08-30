package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

type SearXNG struct {
	enabled            bool
	provider, endpoint string
	client             *http.Client
}

func NewSearXNG(enabled bool, provider, endpoint string, timeout time.Duration) *SearXNG {
	return &SearXNG{enabled: enabled, provider: provider, endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{Timeout: timeout}}
}

func (s *SearXNG) Search(ctx context.Context, query string) (usecase.WebSearchResult, error) {
	if !s.enabled {
		return usecase.WebSearchResult{Provider: s.provider, Query: query, Note: "Web search is disabled in the current profile."}, nil
	}
	if s.endpoint == "" {
		return usecase.WebSearchResult{Enabled: true, Provider: s.provider, Query: query, Note: "SearXNG endpoint is not configured."}, nil
	}
	requestURL := s.endpoint + "/search?q=" + url.QueryEscape(query) + "&format=json&language=zh-CN"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return usecase.WebSearchResult{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return usecase.WebSearchResult{Enabled: true, Provider: s.provider, Query: query, Note: "SearXNG request failed."}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return usecase.WebSearchResult{Enabled: true, Provider: s.provider, Query: query, Note: fmt.Sprintf("SearXNG returned HTTP %d", resp.StatusCode)}, nil
	}
	var payload struct {
		Results []struct{ Title, URL, Content, Engine string } `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return usecase.WebSearchResult{Enabled: true, Provider: s.provider, Query: query, Note: "SearXNG response was invalid."}, nil
	}
	items := make([]usecase.WebSearchItem, 0, 5)
	for _, item := range payload.Results {
		if len(items) == 5 {
			break
		}
		items = append(items, usecase.WebSearchItem{Title: item.Title, URL: item.URL, Snippet: item.Content, Source: firstText(item.Engine, s.provider)})
	}
	note := fmt.Sprintf("SearXNG returned %d results.", len(items))
	if len(items) == 0 {
		note = "SearXNG returned no results."
	}
	return usecase.WebSearchResult{Enabled: true, Provider: s.provider, Query: query, Items: items, Note: note}, nil
}

func firstText(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
