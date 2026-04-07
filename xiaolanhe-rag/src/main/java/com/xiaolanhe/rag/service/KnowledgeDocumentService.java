package com.xiaolanhe.rag.service;

import com.xiaolanhe.domain.knowledge.model.KnowledgeSnippet;
import com.xiaolanhe.rag.model.CreateKnowledgeDocumentCommand;
import com.xiaolanhe.rag.model.KnowledgeDocumentSummary;
import com.xiaolanhe.rag.repository.KnowledgeDocumentRepository;
import java.util.ArrayList;
import java.util.List;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

@Service
public class KnowledgeDocumentService {

    private final KnowledgeDocumentRepository knowledgeDocumentRepository;

    public KnowledgeDocumentService(KnowledgeDocumentRepository knowledgeDocumentRepository) {
        this.knowledgeDocumentRepository = knowledgeDocumentRepository;
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
        for (int i = 0; i < chunks.size(); i++) {
            knowledgeDocumentRepository.insertChunk(documentId, i, chunks.get(i));
        }
        return new KnowledgeDocumentSummary(documentId, chunks.size(), command.title(), normalize(command.gameCode()), normalize(command.regionCode()));
    }

    public List<KnowledgeSnippet> search(String query, String gameCode, String regionCode, int limit) {
        return knowledgeDocumentRepository.search(query, normalize(gameCode), normalize(regionCode), Math.max(1, Math.min(limit, 10)));
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
}