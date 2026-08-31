package presenter

import "github.com/red060324/XiaoLanHe/internal/usecase"

type WebSearchItemResponse struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
}

type WebSearchResponse struct {
	Enabled  bool                    `json:"enabled"`
	Provider string                  `json:"provider"`
	Query    string                  `json:"query"`
	Items    []WebSearchItemResponse `json:"items"`
	Note     string                  `json:"note"`
}

func PresentWebSearch(result usecase.WebSearchResult) WebSearchResponse {
	items := make([]WebSearchItemResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, WebSearchItemResponse{Title: item.Title, URL: item.URL, Snippet: item.Snippet, Source: item.Source})
	}
	return WebSearchResponse{Enabled: result.Enabled, Provider: result.Provider, Query: result.Query, Items: items, Note: result.Note}
}
