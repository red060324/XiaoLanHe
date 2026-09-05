package lightrag

import (
	"context"

	knowledgeentity "github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	knowledge "github.com/red060324/XiaoLanHe/internal/knowledge/usecase"
	legacy "github.com/red060324/XiaoLanHe/internal/usecase"
)

// SearchAdapter exposes only normalized, read-only evidence to the Agent layer.
type SearchAdapter struct{ service *knowledge.Service }

func NewSearchAdapter(service *knowledge.Service) *SearchAdapter {
	return &SearchAdapter{service: service}
}

func (a *SearchAdapter) SearchEvidence(ctx context.Context, query, gameCode, regionCode, mode string, limit int) ([]legacy.Evidence, error) {
	result, err := a.service.Search(ctx, knowledgeentity.SearchInput{Query: query, Mode: knowledgeentity.Mode(mode), GameCode: gameCode, RegionCode: regionCode, Limit: limit})
	if err != nil {
		return nil, err
	}
	evidence := make([]legacy.Evidence, 0, len(result.Items))
	for _, item := range result.Items {
		// Official /query/data references identify the managed file_source but do
		// not provide a dereferenceable URL. Keep the verified source key as the
		// citation label instead of fabricating an unsupported admin URL.
		evidence = append(evidence, legacy.Evidence{Source: "lightrag", Title: item.SourceKey, Content: item.Text})
	}
	return evidence, nil
}
