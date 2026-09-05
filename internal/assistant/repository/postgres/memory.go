package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

type MemoryStore struct{ pool *pgxpool.Pool }

func NewMemoryStore(pool *pgxpool.Pool) *MemoryStore { return &MemoryStore{pool: pool} }
func (s *MemoryStore) PrepareSummary(ctx context.Context, sessionID int64, recentWindow, threshold int) (entity.SummaryCandidate, bool, error) {
	var candidate entity.SummaryCandidate
	if err := s.pool.QueryRow(ctx, `select coalesce(summary_text,''),coalesce(summary_through_message_id,0) from conversation_session where id=$1`, sessionID).Scan(&candidate.PriorSummary, &candidate.PriorWatermark); err != nil {
		return candidate, false, fmt.Errorf("load summary watermark: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		select id,role,content
		from conversation_message
		where session_id=$1 and id>coalesce($2,0)
		order by id`, sessionID, candidate.PriorWatermark)
	if err != nil {
		return candidate, false, fmt.Errorf("load unsummarized messages: %w", err)
	}
	defer rows.Close()
	all := make([]entity.Message, 0)
	for rows.Next() {
		var message entity.Message
		if err := rows.Scan(&message.ID, &message.Role, &message.Content); err != nil {
			return candidate, false, err
		}
		all = append(all, message)
	}
	if err := rows.Err(); err != nil {
		return candidate, false, err
	}
	if assistant.RuneCountMessages(all) <= threshold || len(all) <= recentWindow {
		return candidate, false, nil
	}
	candidate.Messages = all[:len(all)-recentWindow]
	candidate.ThroughMessageID = candidate.Messages[len(candidate.Messages)-1].ID
	return candidate, true, nil
}
func (s *MemoryStore) UpdateSummary(ctx context.Context, sessionID, priorWatermark, throughMessageID int64, summary, promptVersion string) (bool, error) {
	result, err := s.pool.Exec(ctx, `
		update conversation_session cs
		set summary_text=$4,summary_through_message_id=$3,summary_prompt_version=$5,summary_updated_at=now(),updated_at=now()
		where cs.id=$1 and coalesce(cs.summary_through_message_id,0)=$2 and $3>$2
		  and exists(select 1 from conversation_message cm where cm.id=$3 and cm.session_id=cs.id)`, sessionID, priorWatermark, throughMessageID, summary, promptVersion)
	if err != nil {
		return false, fmt.Errorf("update conversation summary: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

var _ assistant.MemoryStore = (*MemoryStore)(nil)
