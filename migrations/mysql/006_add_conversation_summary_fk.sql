ALTER TABLE conversation_session ADD CONSTRAINT fk_conversation_summary_message FOREIGN KEY(summary_through_message_id) REFERENCES conversation_message(id) ON DELETE SET NULL;
