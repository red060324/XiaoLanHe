package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

type KnowledgeStore struct{ pool *pgxpool.Pool }

func NewKnowledgeStore(pool *pgxpool.Pool) *KnowledgeStore { return &KnowledgeStore{pool: pool} }

func (s *KnowledgeStore) CreateDocument(ctx context.Context, document usecase.KnowledgeDocument, chunks []string, embeddings [][]float32) (id int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	err = tx.QueryRow(ctx, `
		insert into knowledge_document(source_type,title,source_url,game_code,region_code,patch_version,metadata,content_text)
		values ($1,$2,nullif($3,''),nullif($4,''),nullif($5,''),nullif($6,''),'{}'::jsonb,$7)
		returning id`, document.SourceType, document.Title, document.SourceURL, document.GameCode, document.RegionCode, document.PatchVersion, document.ContentText).Scan(&id)
	if err != nil {
		return 0, err
	}
	for i, chunk := range chunks {
		var vector any
		if i < len(embeddings) && len(embeddings[i]) == 1536 {
			vector = vectorLiteral(embeddings[i])
		}
		if _, err = tx.Exec(ctx, `insert into knowledge_chunk(document_id,chunk_no,chunk_text,embedding,metadata) values ($1,$2,$3,cast($4 as vector),'{}'::jsonb)`, id, i, chunk, vector); err != nil {
			return 0, err
		}
	}
	err = tx.Commit(ctx)
	return id, err
}

func (s *KnowledgeStore) SearchKeyword(ctx context.Context, query, gameCode, regionCode string, limit int) ([]usecase.KnowledgeSnippet, error) {
	sql := `select kc.id,kd.id,kd.title,coalesce(kd.game_code,''),coalesce(kd.region_code,''),coalesce(kd.patch_version,''),coalesce(kd.source_url,''),kc.chunk_text,
		case when lower(kd.title) like lower($1) then 30 when lower(kc.chunk_text) like lower($1) then 20 else 10 end
		from knowledge_chunk kc join knowledge_document kd on kd.id=kc.document_id where lower(kc.chunk_text) like lower($1)`
	args := []any{"%" + query + "%"}
	if strings.TrimSpace(gameCode) != "" {
		args = append(args, gameCode)
		sql += fmt.Sprintf(" and kd.game_code=$%d", len(args))
	}
	if strings.TrimSpace(regionCode) != "" {
		args = append(args, regionCode)
		sql += fmt.Sprintf(" and (kd.region_code=$%d or kd.region_code is null)", len(args))
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" order by 9 desc,kc.id desc limit $%d", len(args))
	return scanKnowledge(ctx, s.pool, sql, args...)
}

func (s *KnowledgeStore) SearchVector(ctx context.Context, embedding []float32, gameCode, regionCode string, limit int) ([]usecase.KnowledgeSnippet, error) {
	sql := `select kc.id,kd.id,kd.title,coalesce(kd.game_code,''),coalesce(kd.region_code,''),coalesce(kd.patch_version,''),coalesce(kd.source_url,''),kc.chunk_text,
		cast(greatest(0,least(100,round((1-(kc.embedding <=> cast($1 as vector)))*100))) as integer)
		from knowledge_chunk kc join knowledge_document kd on kd.id=kc.document_id where kc.embedding is not null`
	args := []any{vectorLiteral(embedding)}
	if strings.TrimSpace(gameCode) != "" {
		args = append(args, gameCode)
		sql += fmt.Sprintf(" and kd.game_code=$%d", len(args))
	}
	if strings.TrimSpace(regionCode) != "" {
		args = append(args, regionCode)
		sql += fmt.Sprintf(" and (kd.region_code=$%d or kd.region_code is null)", len(args))
	}
	args = append(args, vectorLiteral(embedding))
	orderArg := len(args)
	args = append(args, limit)
	sql += fmt.Sprintf(" order by kc.embedding <=> cast($%d as vector),kc.id desc limit $%d", orderArg, len(args))
	return scanKnowledge(ctx, s.pool, sql, args...)
}

type knowledgeQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func scanKnowledge(ctx context.Context, q knowledgeQueryer, sql string, args ...any) ([]usecase.KnowledgeSnippet, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]usecase.KnowledgeSnippet, 0)
	for rows.Next() {
		var item usecase.KnowledgeSnippet
		if err := rows.Scan(&item.ChunkID, &item.DocumentID, &item.Title, &item.GameCode, &item.RegionCode, &item.PatchVersion, &item.SourceURL, &item.Text, &item.Score); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func vectorLiteral(values []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprint(&b, value)
	}
	b.WriteByte(']')
	return b.String()
}
