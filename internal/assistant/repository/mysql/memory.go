package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

const loadSummaryWatermarkSQL = `
	select coalesce(summary_text, ''), coalesce(summary_through_message_id, 0)
	from conversation_session
	where id = ?`

const loadUnsummarizedMessagesSQL = `
	select id, role, content
	from conversation_message
	where session_id = ? and id > ?
	order by id`

const updateSummarySQL = `
	update conversation_session as cs
	set summary_text = ?,
	    summary_through_message_id = ?,
	    summary_prompt_version = ?,
	    summary_updated_at = utc_timestamp(6),
	    updated_at = utc_timestamp(6)
	where cs.id = ?
	  and coalesce(cs.summary_through_message_id, 0) = ?
	  and ? > ?
	  and exists (
		select 1
		from conversation_message as cm
		where cm.id = ? and cm.session_id = cs.id
	  )`

type MemoryStore struct{ db *sql.DB }

func NewMemoryStore(db *sql.DB) *MemoryStore { return &MemoryStore{db: db} }

func (s *MemoryStore) PrepareSummary(ctx context.Context, sessionID int64, recentWindow, threshold int) (entity.SummaryCandidate, bool, error) {
	var candidate entity.SummaryCandidate
	if err := s.db.QueryRowContext(ctx, loadSummaryWatermarkSQL, sessionID).Scan(
		&candidate.PriorSummary, &candidate.PriorWatermark,
	); err != nil {
		return candidate, false, fmt.Errorf("load summary watermark: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, loadUnsummarizedMessagesSQL, sessionID, candidate.PriorWatermark)
	if err != nil {
		return candidate, false, fmt.Errorf("load unsummarized messages: %w", err)
	}
	defer rows.Close()

	all := make([]entity.Message, 0)
	for rows.Next() {
		var message entity.Message
		if err := rows.Scan(&message.ID, &message.Role, &message.Content); err != nil {
			return candidate, false, fmt.Errorf("scan unsummarized message: %w", err)
		}
		all = append(all, message)
	}
	if err := rows.Err(); err != nil {
		return candidate, false, fmt.Errorf("iterate unsummarized messages: %w", err)
	}
	if assistant.RuneCountMessages(all) <= threshold || len(all) <= recentWindow {
		return candidate, false, nil
	}
	candidate.Messages = all[:len(all)-recentWindow]
	candidate.ThroughMessageID = candidate.Messages[len(candidate.Messages)-1].ID
	return candidate, true, nil
}

func (s *MemoryStore) UpdateSummary(ctx context.Context, sessionID, priorWatermark, throughMessageID int64, summary, promptVersion string) (bool, error) {
	result, err := s.db.ExecContext(ctx, updateSummarySQL,
		summary, throughMessageID, promptVersion, sessionID, priorWatermark,
		throughMessageID, priorWatermark, throughMessageID,
	)
	if err != nil {
		return false, fmt.Errorf("update conversation summary: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read updated conversation summary count: %w", err)
	}
	return affected == 1, nil
}

var _ assistant.MemoryStore = (*MemoryStore)(nil)
