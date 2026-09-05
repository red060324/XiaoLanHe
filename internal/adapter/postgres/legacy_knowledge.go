package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/knowledge/importer"
)

// LegacyKnowledgeSource is wired only by the explicit one-time import command.
// The advanced application runtime never reads this legacy knowledge table.
type LegacyKnowledgeSource struct{ pool *pgxpool.Pool }

func NewLegacyKnowledgeSource(pool *pgxpool.Pool) *LegacyKnowledgeSource {
	return &LegacyKnowledgeSource{pool: pool}
}

func (s *LegacyKnowledgeSource) ListLegacyKnowledge(ctx context.Context, afterID int64, limit int) ([]importer.LegacyDocument, error) {
	rows, err := s.pool.Query(ctx, `
		select id,source_type,title,coalesce(source_url,''),coalesce(game_code,''),
			coalesce(region_code,''),coalesce(patch_version,''),coalesce(content_text,'')
		from knowledge_document where id>$1 order by id asc limit $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]importer.LegacyDocument, 0, limit)
	for rows.Next() {
		var value importer.LegacyDocument
		if err := rows.Scan(&value.ID, &value.Draft.SourceType, &value.Draft.Title, &value.Draft.SourceURL, &value.Draft.GameCode, &value.Draft.RegionCode, &value.Draft.PatchVersion, &value.Draft.ContentText); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

var _ importer.Source = (*LegacyKnowledgeSource)(nil)
