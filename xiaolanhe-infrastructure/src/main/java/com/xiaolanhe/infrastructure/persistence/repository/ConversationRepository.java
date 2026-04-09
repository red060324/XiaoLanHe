package com.xiaolanhe.infrastructure.persistence.repository;

import com.xiaolanhe.common.util.JsonSupport;
import java.sql.PreparedStatement;
import java.sql.Statement;
import java.sql.Timestamp;
import java.time.OffsetDateTime;
import java.util.Map;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.support.GeneratedKeyHolder;
import org.springframework.jdbc.support.KeyHolder;
import org.springframework.stereotype.Repository;

@Repository
public class ConversationRepository {

    private final JdbcTemplate jdbcTemplate;

    public ConversationRepository(JdbcTemplate jdbcTemplate) {
        this.jdbcTemplate = jdbcTemplate;
    }

    public long findOrCreateSession(String sessionKey) {
        Long existing = jdbcTemplate.query(
                "select id from conversation_session where session_key = ?",
                ps -> ps.setString(1, sessionKey),
                rs -> rs.next() ? rs.getLong("id") : null
        );
        if (existing != null) {
            jdbcTemplate.update("update conversation_session set updated_at = now() where id = ?", existing);
            return existing;
        }

        KeyHolder keyHolder = new GeneratedKeyHolder();
        jdbcTemplate.update(connection -> {
            PreparedStatement ps = connection.prepareStatement(
                    """
                    insert into conversation_session(session_key, metadata)
                    values (?, ?::jsonb)
                    """,
                    new String[]{"id"}
            );
            ps.setString(1, sessionKey);
            ps.setString(2, "{}");
            return ps;
        }, keyHolder);

        Number key = keyHolder.getKey();
        if (key == null) {
            throw new IllegalStateException("Failed to create conversation session");
        }
        return key.longValue();
    }

    public void saveMessage(long sessionId, String role, String content, String modelName, Map<String, Object> metadata) {
        jdbcTemplate.update(
                """
                insert into conversation_message(session_id, role, content, model_name, metadata, created_at)
                values (?, ?, ?, ?, cast(? as jsonb), ?)
                """,
                sessionId,
                role,
                content,
                modelName,
                JsonSupport.toJson(metadata),
                Timestamp.from(OffsetDateTime.now().toInstant())
        );
    }
}
