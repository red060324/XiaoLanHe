package com.xiaolanhe.infrastructure.persistence.repository;

import com.xiaolanhe.common.util.JsonSupport;
import java.sql.ResultSet;
import java.sql.PreparedStatement;
import java.sql.Statement;
import java.sql.Timestamp;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
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

    public SessionMemoryState loadSessionMemoryState(long sessionId) {
        return jdbcTemplate.query(
                """
                select
                    coalesce(metadata ->> 'summary_text', '') as summary_text,
                    coalesce((metadata ->> 'summary_message_count')::integer, 0) as summary_message_count
                from conversation_session
                where id = ?
                """,
                ps -> ps.setLong(1, sessionId),
                rs -> rs.next() ? mapSessionMemoryState(rs) : new SessionMemoryState("", 0)
        );
    }

    public List<ConversationMessageRecord> findRecentMessages(long sessionId, int limit) {
        List<ConversationMessageRecord> reversed = jdbcTemplate.query(
                """
                select role, content, coalesce(model_name, '') as model_name, created_at
                from conversation_message
                where session_id = ?
                order by created_at desc
                limit ?
                """,
                ps -> {
                    ps.setLong(1, sessionId);
                    ps.setInt(2, limit);
                },
                (rs, rowNum) -> new ConversationMessageRecord(
                        rs.getString("role"),
                        rs.getString("content"),
                        rs.getString("model_name"),
                        rs.getTimestamp("created_at").toInstant().atOffset(OffsetDateTime.now().getOffset())
                )
        );
        List<ConversationMessageRecord> ordered = new ArrayList<>(reversed);
        java.util.Collections.reverse(ordered);
        return ordered;
    }

    public int countMessages(long sessionId) {
        Integer count = jdbcTemplate.queryForObject(
                "select count(*) from conversation_message where session_id = ?",
                Integer.class,
                sessionId
        );
        return count == null ? 0 : count;
    }

    public List<ConversationMessageRecord> findMessagesForSummary(long sessionId, int retainRecentCount) {
        int totalCount = countMessages(sessionId);
        int summaryCount = Math.max(0, totalCount - retainRecentCount);
        if (summaryCount == 0) {
            return List.of();
        }
        return jdbcTemplate.query(
                """
                select role, content, coalesce(model_name, '') as model_name, created_at
                from conversation_message
                where session_id = ?
                order by created_at asc
                limit ?
                """,
                ps -> {
                    ps.setLong(1, sessionId);
                    ps.setInt(2, summaryCount);
                },
                (rs, rowNum) -> new ConversationMessageRecord(
                        rs.getString("role"),
                        rs.getString("content"),
                        rs.getString("model_name"),
                        rs.getTimestamp("created_at").toInstant().atOffset(OffsetDateTime.now().getOffset())
                )
        );
    }

    public void updateSessionSummary(long sessionId, String summaryText, int summaryMessageCount) {
        jdbcTemplate.update(
                """
                update conversation_session
                set metadata = jsonb_set(
                    jsonb_set(metadata, '{summary_text}', to_jsonb(?::text), true),
                    '{summary_message_count}', to_jsonb(?::integer), true
                ),
                updated_at = now()
                where id = ?
                """,
                summaryText,
                summaryMessageCount,
                sessionId
        );
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

    private SessionMemoryState mapSessionMemoryState(ResultSet rs) throws java.sql.SQLException {
        return new SessionMemoryState(
                rs.getString("summary_text"),
                rs.getInt("summary_message_count")
        );
    }

    public record ConversationMessageRecord(
            String role,
            String content,
            String modelName,
            OffsetDateTime createdAt
    ) {
    }

    public record SessionMemoryState(
            String summaryText,
            int summaryMessageCount
    ) {
    }
}
