package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ConversationStore struct {
	pool *pgxpool.Pool
}

func NewConversationStore(pool *pgxpool.Pool) *ConversationStore {
	return &ConversationStore{pool: pool}
}

func (s *ConversationStore) FindOrCreateSession(ctx context.Context, sessionKey string) (int64, error) {
	const query = `
		insert into conversation_session(session_key, metadata)
		values ($1, '{}'::jsonb)
		on conflict (session_key) do update set updated_at = now()
		returning id`
	var id int64
	if err := s.pool.QueryRow(ctx, query, sessionKey).Scan(&id); err != nil {
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
