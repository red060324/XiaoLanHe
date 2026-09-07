package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

const (
	maxConversationInsertAttempts   = 2
	conversationReconciliationLimit = 5 * time.Second
)

var errConversationDurableStateMismatch = errors.New("mysql durable conversation state does not match attempted write")
var errConversationMessageConflict = errors.New("conversation message key is already bound to a different payload")
var conversationMessageKeyPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type ConversationStore struct {
	db *sql.DB
}

func NewConversationStore(db *sql.DB) *ConversationStore {
	return &ConversationStore{db: db}
}

func (s *ConversationStore) LoadContext(ctx context.Context, sessionID int64, limit int) (string, error) {
	var summary string
	var summaryWatermark int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(summary_text, ''), COALESCE(summary_through_message_id, 0)
		FROM conversation_session
		WHERE id = ?`, sessionID).Scan(&summary, &summaryWatermark); err != nil {
		return "", fmt.Errorf("load session summary: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT role, content
		FROM conversation_message
		WHERE session_id = ? AND id > ?
		ORDER BY id DESC
		LIMIT ?`, sessionID, summaryWatermark, limit)
	if err != nil {
		return "", fmt.Errorf("load recent messages: %w", err)
	}
	defer rows.Close()

	type message struct {
		role    string
		content string
	}
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

func (s *ConversationStore) FindOrCreateSession(ctx context.Context, sessionKey string, userID int64) (int64, error) {
	for attempt := 0; attempt < maxConversationInsertAttempts; attempt++ {
		id, duplicate, err := s.findOrCreateSessionAttempt(ctx, sessionKey, userID)
		if err != nil {
			return 0, err
		}
		if !duplicate {
			return id, nil
		}
	}
	return 0, errors.New("conversation session insert race did not converge")
}

func (s *ConversationStore) findOrCreateSessionAttempt(ctx context.Context, sessionKey string, userID int64) (int64, bool, error) {
	id, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (int64, error) {
		var id int64
		var owner sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT id, user_id
			FROM conversation_session
			WHERE session_key = ?
			FOR UPDATE`, sessionKey).Scan(&id, &owner)
		switch {
		case err == nil:
			if owner.Valid && (userID == 0 || owner.Int64 != userID) {
				return 0, usecase.ErrConversationForbidden
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE conversation_session
				SET user_id = COALESCE(user_id, NULLIF(?, 0)),
					updated_at = UTC_TIMESTAMP(6)
				WHERE id = ?`, userID, id)
			if err != nil {
				return 0, fmt.Errorf("update conversation session: %w", err)
			}
			if err := requireConversationAtMostOneAffected(result, "update conversation session"); err != nil {
				return 0, err
			}
			return id, nil
		case errors.Is(err, sql.ErrNoRows):
			result, insertErr := tx.ExecContext(ctx, `
				INSERT INTO conversation_session(session_key, user_id, metadata, created_at, updated_at)
				VALUES (?, NULLIF(?, 0), JSON_OBJECT(), UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, sessionKey, userID)
			if insertErr != nil {
				if isConversationDuplicateKey(insertErr) {
					return 0, errConversationInsertRace
				}
				return 0, fmt.Errorf("insert conversation session: %w", insertErr)
			}
			if err := requireConversationOneAffected(result, "insert conversation session"); err != nil {
				return 0, err
			}
			id, err = result.LastInsertId()
			if err != nil {
				return 0, fmt.Errorf("read conversation session id: %w", err)
			}
			return id, nil
		default:
			return 0, fmt.Errorf("find conversation session: %w", err)
		}
	})
	if err == nil {
		return id, false, nil
	}
	if errors.Is(err, errConversationInsertRace) {
		return 0, true, nil
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return 0, false, err
	}
	reconcileCtx, cancel := conversationReconciliationContext(ctx)
	defer cancel()
	return s.reconcileSession(reconcileCtx, sessionKey, userID, err)
}

var errConversationInsertRace = errors.New("conversation session insert raced")

func (s *ConversationStore) reconcileSession(ctx context.Context, sessionKey string, userID int64, commitErr error) (int64, bool, error) {
	var id int64
	var owner sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id
		FROM conversation_session
		WHERE session_key = ?`, sessionKey).Scan(&id, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, commitErr
	}
	if err != nil {
		return 0, false, errors.Join(commitErr, fmt.Errorf("reconcile conversation session: %w", err))
	}
	if !conversationOwnerMatches(owner, userID) {
		return 0, false, errors.Join(commitErr, errConversationDurableStateMismatch)
	}
	return id, false, nil
}

func conversationOwnerMatches(owner sql.NullInt64, userID int64) bool {
	return owner.Valid && owner.Int64 == userID || !owner.Valid && userID == 0
}

func (s *ConversationStore) SaveMessage(ctx context.Context, sessionID int64, role, content, model string) error {
	messageKey, err := newConversationMessageKey()
	if err != nil {
		return fmt.Errorf("create conversation message key: %w", err)
	}
	return s.SaveMessageWithKey(ctx, sessionID, messageKey, role, content, model)
}

func (s *ConversationStore) SaveMessageWithKey(ctx context.Context, sessionID int64, messageKey, role, content, model string) error {
	if !conversationMessageKeyPattern.MatchString(messageKey) {
		return errors.New("conversation message key must be a lowercase UUIDv4")
	}
	const query = `
		INSERT INTO conversation_message(session_id, message_key, role, content, model_name, metadata, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), JSON_OBJECT(), UTC_TIMESTAMP(6))`
	_, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		result, err := tx.ExecContext(ctx, query, sessionID, messageKey, role, content, model)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, requireConversationOneAffected(result, "insert conversation message")
	})
	if err == nil {
		return nil
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) && !isConversationDuplicateKey(err) {
		return fmt.Errorf("insert conversation message: %w", err)
	}

	reconcileCtx, cancel := conversationReconciliationContext(ctx)
	defer cancel()
	return s.reconcileMessage(reconcileCtx, sessionID, messageKey, role, content, model, err)
}

func (s *ConversationStore) reconcileMessage(ctx context.Context, sessionID int64, messageKey, role, content, model string, writeErr error) error {
	var storedRole, storedContent string
	var storedModel sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT role, content, model_name
		FROM conversation_message
		WHERE session_id = ? AND message_key = ?`, sessionID, messageKey).Scan(&storedRole, &storedContent, &storedModel)
	if errors.Is(err, sql.ErrNoRows) {
		if mysqltx.IsCommitOutcomeUnknown(writeErr) {
			return fmt.Errorf("insert conversation message: %w", writeErr)
		}
		return errors.Join(fmt.Errorf("insert conversation message: %w", writeErr), errConversationMessageConflict)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("insert conversation message: %w", writeErr), fmt.Errorf("reconcile conversation message: %w", err))
	}
	storedModelName := ""
	if storedModel.Valid {
		storedModelName = storedModel.String
	}
	if storedRole != role || storedContent != content || storedModelName != model {
		return errors.Join(fmt.Errorf("insert conversation message: %w", writeErr), errConversationMessageConflict)
	}
	return nil
}

func conversationReconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), conversationReconciliationLimit)
}

func newConversationMessageKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func requireConversationOneAffected(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%s affected %d rows, want 1", operation, affected)
	}
	return nil
}

func requireConversationAtMostOneAffected(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if affected > 1 {
		return fmt.Errorf("%s affected %d rows, want at most 1", operation, affected)
	}
	return nil
}

func isConversationDuplicateKey(err error) bool {
	var mysqlErr *drivermysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
