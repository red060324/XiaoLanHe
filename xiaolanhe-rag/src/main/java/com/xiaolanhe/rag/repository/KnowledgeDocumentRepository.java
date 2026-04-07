package com.xiaolanhe.rag.repository;

import com.xiaolanhe.domain.knowledge.model.KnowledgeSnippet;
import java.sql.PreparedStatement;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.support.GeneratedKeyHolder;
import org.springframework.jdbc.support.KeyHolder;
import org.springframework.stereotype.Repository;
import org.springframework.util.StringUtils;

@Repository
public class KnowledgeDocumentRepository {

    private final JdbcTemplate jdbcTemplate;

    public KnowledgeDocumentRepository(JdbcTemplate jdbcTemplate) {
        this.jdbcTemplate = jdbcTemplate;
    }

    public long createDocument(String sourceType,
                               String title,
                               String sourceUrl,
                               String gameCode,
                               String regionCode,
                               String patchVersion,
                               String contentText) {
        KeyHolder keyHolder = new GeneratedKeyHolder();
        jdbcTemplate.update(connection -> {
            PreparedStatement ps = connection.prepareStatement(
                    """
                    insert into knowledge_document(source_type, title, source_url, game_code, region_code, patch_version, metadata, content_text)
                    values (?, ?, ?, ?, ?, ?, '{}'::jsonb, ?)
                    """,
                    Statement.RETURN_GENERATED_KEYS
            );
            ps.setString(1, sourceType);
            ps.setString(2, title);
            ps.setString(3, sourceUrl);
            ps.setString(4, gameCode);
            ps.setString(5, regionCode);
            ps.setString(6, patchVersion);
            ps.setString(7, contentText);
            return ps;
        }, keyHolder);
        Number key = keyHolder.getKey();
        if (key == null) {
            throw new IllegalStateException("Failed to create knowledge document");
        }
        return key.longValue();
    }

    public void insertChunk(long documentId, int chunkNo, String chunkText) {
        jdbcTemplate.update(
                """
                insert into knowledge_chunk(document_id, chunk_no, chunk_text, metadata)
                values (?, ?, ?, '{}'::jsonb)
                """,
                documentId,
                chunkNo,
                chunkText
        );
    }

    public List<KnowledgeSnippet> search(String query, String gameCode, String regionCode, int limit) {
        StringBuilder sql = new StringBuilder("""
                select kc.id as chunk_id,
                       kd.id as document_id,
                       kd.title,
                       kd.game_code,
                       kd.region_code,
                       kd.patch_version,
                       kd.source_url,
                       kc.chunk_text,
                       case
                           when lower(kd.title) like lower(?) then 30
                           when lower(kc.chunk_text) like lower(?) then 20
                           else 10
                       end as score
                from knowledge_chunk kc
                join knowledge_document kd on kd.id = kc.document_id
                where lower(kc.chunk_text) like lower(?)
                """);

        List<Object> args = new ArrayList<>();
        String likeQuery = '%' + query + '%';
        args.add(likeQuery);
        args.add(likeQuery);
        args.add(likeQuery);

        if (StringUtils.hasText(gameCode)) {
            sql.append(" and kd.game_code = ?");
            args.add(gameCode);
        }
        if (StringUtils.hasText(regionCode)) {
            sql.append(" and (kd.region_code = ? or kd.region_code is null)");
            args.add(regionCode);
        }

        sql.append(" order by score desc, kc.id desc limit ?");
        args.add(limit);

        return jdbcTemplate.query(sql.toString(), args.toArray(), (rs, rowNum) -> new KnowledgeSnippet(
                rs.getLong("chunk_id"),
                rs.getLong("document_id"),
                rs.getString("title"),
                rs.getString("game_code"),
                rs.getString("region_code"),
                rs.getString("patch_version"),
                rs.getString("source_url"),
                rs.getString("chunk_text"),
                rs.getInt("score")
        ));
    }
}