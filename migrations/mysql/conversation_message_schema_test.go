package mysql

import (
	"strings"
	"testing"
)

func TestConversationMessageSchemaHasNullableDurableIdentity(t *testing.T) {
	schema := migrationSQL(t, "005_create_conversation_message.sql")
	for _, fragment := range []string{
		"message_key VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL",
		"CONSTRAINT uk_conversation_message_key UNIQUE(session_id,message_key)",
		"KEY idx_conversation_message_session(session_id,created_at)",
		"KEY idx_conversation_message_cursor(session_id,id)",
		"CONSTRAINT ck_conversation_message_key CHECK(message_key IS NULL OR REGEXP_LIKE(message_key,",
	} {
		if !strings.Contains(schema, fragment) {
			t.Errorf("conversation-message schema is missing %q", fragment)
		}
	}
	if strings.Contains(schema, "message_key VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL") {
		t.Fatal("legacy PostgreSQL copy must be able to omit message_key and store NULL")
	}
}
