package com.xiaolanhe.rag.service;

import com.xiaolanhe.domain.knowledge.model.KnowledgeSnippet;
import com.xiaolanhe.rag.model.CreateKnowledgeDocumentCommand;
import com.xiaolanhe.rag.model.KnowledgeDocumentSummary;
import com.xiaolanhe.rag.repository.KnowledgeDocumentRepository;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.springframework.beans.factory.ObjectProvider;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class KnowledgeDocumentService {

    private static final Logger log = LoggerFactory.getLogger(KnowledgeDocumentService.class);
    private static final int VECTOR_DIMENSION = 1536;

    private final KnowledgeDocumentRepository knowledgeDocumentRepository;
    private final ObjectProvider<EmbeddingModel> embeddingModelProvider;

    public KnowledgeDocumentService(KnowledgeDocumentRepository knowledgeDocumentRepository,
                                    ObjectProvider<EmbeddingModel> embeddingModelProvider) {
        this.knowledgeDocumentRepository = knowledgeDocumentRepository;
        this.embeddingModelProvider = embeddingModelProvider;
    }

    public KnowledgeDocumentSummary createDocument(CreateKnowledgeDocumentCommand command) {
        long documentId = knowledgeDocumentRepository.createDocument(
                command.sourceType(),
                command.title(),
                command.sourceUrl(),
                normalize(command.gameCode()),
                normalize(command.regionCode()),
                normalize(command.patchVersion()),
                command.contentText()
        );
        List<String> chunks = chunk(command.contentText());
        List<String> embeddingLiterals = buildChunkEmbeddings(chunks);
        for (int i = 0; i < chunks.size(); i++) {
            knowledgeDocumentRepository.insertChunk(documentId, i, chunks.get(i), embeddingLiterals.get(i));
        }
        return new KnowledgeDocumentSummary(documentId, chunks.size(), command.title(), normalize(command.gameCode()), normalize(command.regionCode()));
    }

    public List<KnowledgeSnippet> search(String query, String gameCode, String regionCode, int limit) {
        int resolvedLimit = Math.max(1, Math.min(limit, 10));
        String normalizedGameCode = normalize(gameCode);
        String normalizedRegionCode = normalize(regionCode);
        List<KnowledgeSnippet> keywordHits = knowledgeDocumentRepository.searchByKeyword(query, normalizedGameCode, normalizedRegionCode, resolvedLimit);
        float[] queryEmbedding = embedQuery(query);
        if (queryEmbedding == null) {
            return keywordHits;
        }

        List<KnowledgeSnippet> vectorHits = knowledgeDocumentRepository.searchByVector(toVectorLiteral(queryEmbedding), normalizedGameCode, normalizedRegionCode, resolvedLimit);
        log.info(
                "Knowledge hybrid retrieval executed. query={}, vectorHitCount={}, keywordHitCount={}, limit={}",
                trim(query, 80),
                vectorHits.size(),
                keywordHits.size(),
                resolvedLimit
        );
        return mergeHybridHits(vectorHits, keywordHits, resolvedLimit);
    }

    private List<String> chunk(String contentText) {
        String normalized = contentText.replace("\r", "").trim();
        String[] rawBlocks = normalized.split("\n\n+");
        List<String> chunks = new ArrayList<>();
        StringBuilder current = new StringBuilder();
        for (String rawBlock : rawBlocks) {
            String block = rawBlock.trim();
            if (!StringUtils.hasText(block)) {
                continue;
            }
            if (current.length() + block.length() + 2 > 800 && current.length() > 0) {
                chunks.add(current.toString());
                current.setLength(0);
            }
            if (current.length() > 0) {
                current.append("\n\n");
            }
            current.append(block);
        }
        if (current.length() > 0) {
            chunks.add(current.toString());
        }
        if (chunks.isEmpty()) {
            chunks.add(normalized);
        }
        return chunks;
    }

    private String normalize(String value) {
        return StringUtils.hasText(value) ? value.trim() : null;
    }

    private List<String> buildChunkEmbeddings(List<String> chunks) {
        List<String> literals = new ArrayList<>();
        if (chunks.isEmpty()) {
            return literals;
        }

        EmbeddingModel embeddingModel = embeddingModelProvider.getIfAvailable();
        if (embeddingModel == null) {
            for (int i = 0; i < chunks.size(); i++) {
                literals.add(null);
            }
            return literals;
        }

        try {
            List<float[]> embeddings = embeddingModel.embed(chunks);
            for (int i = 0; i < chunks.size(); i++) {
                float[] embedding = i < embeddings.size() ? embeddings.get(i) : null;
                literals.add(toSafeVectorLiteral(embedding, "chunk-" + i));
            }
            return literals;
        } catch (Exception ex) {
            log.warn("Knowledge chunk embedding generation failed. Falling back to null embeddings.", ex);
            for (int i = 0; i < chunks.size(); i++) {
                literals.add(null);
            }
            return literals;
        }
    }

    private float[] embedQuery(String query) {
        if (!StringUtils.hasText(query)) {
            return null;
        }
        EmbeddingModel embeddingModel = embeddingModelProvider.getIfAvailable();
        if (embeddingModel == null) {
            return null;
        }
        try {
            float[] embedding = embeddingModel.embed(query.trim());
            if (embedding == null || embedding.length == 0) {
                return null;
            }
            if (embedding.length != VECTOR_DIMENSION) {
                log.warn(
                        "Knowledge query embedding dimension mismatch. expected={}, actual={}. Skip vector retrieval.",
                        VECTOR_DIMENSION,
                        embedding.length
                );
                return null;
            }
            return embedding;
        } catch (Exception ex) {
            log.warn("Knowledge query embedding generation failed. Falling back to keyword retrieval.", ex);
            return null;
        }
    }

    private List<KnowledgeSnippet> mergeHybridHits(List<KnowledgeSnippet> vectorHits,
                                                   List<KnowledgeSnippet> keywordHits,
                                                   int limit) {
        Map<Long, KnowledgeSnippet> merged = new LinkedHashMap<>();
        vectorHits.forEach(hit -> merged.put(hit.chunkId(), hit));
        keywordHits.forEach(hit -> merged.merge(hit.chunkId(), hit, this::mergeSnippetScore));
        return merged.values().stream()
                .sorted(Comparator.comparingInt(KnowledgeSnippet::score)
                        .reversed()
                        .thenComparing(Comparator.comparingLong(KnowledgeSnippet::chunkId).reversed()))
                .limit(limit)
                .toList();
    }

    private KnowledgeSnippet mergeSnippetScore(KnowledgeSnippet existing, KnowledgeSnippet incoming) {
        int boostedScore = Math.min(100, Math.max(existing.score(), incoming.score()) + 10);
        return new KnowledgeSnippet(
                existing.chunkId(),
                existing.documentId(),
                existing.title(),
                existing.gameCode(),
                existing.regionCode(),
                existing.patchVersion(),
                existing.sourceUrl(),
                existing.snippet(),
                boostedScore
        );
    }

    private String toSafeVectorLiteral(float[] embedding, String label) {
        if (embedding == null || embedding.length == 0) {
            return null;
        }
        if (embedding.length != VECTOR_DIMENSION) {
            log.warn(
                    "Knowledge embedding dimension mismatch for {}. expected={}, actual={}. Skip vector persistence.",
                    label,
                    VECTOR_DIMENSION,
                    embedding.length
            );
            return null;
        }
        return toVectorLiteral(embedding);
    }

    private String toVectorLiteral(float[] embedding) {
        StringBuilder builder = new StringBuilder("[");
        for (int i = 0; i < embedding.length; i++) {
            if (i > 0) {
                builder.append(',');
            }
            builder.append(Float.toString(embedding[i]));
        }
        builder.append(']');
        return builder.toString();
    }

    private String trim(String value, int maxLength) {
        if (!StringUtils.hasText(value) || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }
}
