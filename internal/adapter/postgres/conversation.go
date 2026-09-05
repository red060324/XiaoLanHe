package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

type ConversationStore struct {
	pool *pgxpool.Pool
}

func (s *ConversationStore) LoadContext(ctx context.Context, sessionID int64, limit int) (string, error) {
	var summary string
	var summaryWatermark int64
	if err := s.pool.QueryRow(ctx, `
		select coalesce(summary_text,''),coalesce(summary_through_message_id,0)
		from conversation_session cs
		where cs.id=$1`, sessionID).Scan(&summary, &summaryWatermark); err != nil {
		return "", fmt.Errorf("load session summary: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		select role,content
		from conversation_message
		where session_id=$1 and id>$2
		order by id desc
		limit $3`, sessionID, summaryWatermark, limit)
	if err != nil {
		return "", fmt.Errorf("load recent messages: %w", err)
	}
	defer rows.Close()
	type message struct{ role, content string }
	reversed := make([]message, 0, limit)
	for rows.Next() {
		var item message
		if err := rows.Scan(&item.role, &item.content); err != nil {
			return "", err
		}
		reversed = append(reversed, item)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	var b strings.Builder
	if summary != "" {
		fmt.Fprintf(&b, "【会话摘要】\n%s\n\n", summary)
	}
	if len(reversed) > 0 {
		b.WriteString("【最近对话】\n")
		for i := len(reversed) - 1; i >= 0; i-- {
			role := "用户"
			if reversed[i].role == "assistant" {
				role = "助手"
			}
			fmt.Fprintf(&b, "%s：%s\n", role, reversed[i].content)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func NewConversationStore(pool *pgxpool.Pool) *ConversationStore {
	return &ConversationStore{pool: pool}
}

func (s *ConversationStore) FindOrCreateSession(ctx context.Context, sessionKey string, userID int64) (int64, error) {
	const query = `
		insert into conversation_session(session_key, user_id, metadata)
		values ($1, nullif($2, 0), '{}'::jsonb)
		on conflict (session_key) do update set
			user_id = coalesce(conversation_session.user_id, excluded.user_id),
			updated_at = now()
		where conversation_session.user_id is null
			or conversation_session.user_id = excluded.user_id
		returning id`
	var id int64
	if err := s.pool.QueryRow(ctx, query, sessionKey, userID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, usecase.ErrConversationForbidden
		}
		return 0, fmt.Errorf("upsert conversation session: %w", err)
	}
	return id, nil
}

func (s *ConversationStore) SaveMessage(ctx context.Context, sessionID int64, role, content, model string) error {
	const query = `
		insert into conversation_message(session_id, role, content, model_name, metadata, created_at)
		values ($1, $2, $3, nullif($4, ''), '{}'::jsonb, now())`
	if _, err := s.pool.Exec(ctx, query, sessionID, role, content, model); err != nil {
		return fmt.Errorf("insert conversation message: %w", err)
	}
	return nil
}
