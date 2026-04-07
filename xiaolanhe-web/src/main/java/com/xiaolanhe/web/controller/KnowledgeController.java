package com.xiaolanhe.web.controller;

import com.xiaolanhe.web.dto.knowledge.CreateKnowledgeDocumentRequest;
import com.xiaolanhe.web.dto.knowledge.KnowledgeDocumentResponse;
import com.xiaolanhe.web.dto.knowledge.KnowledgeSearchResponse;
import com.xiaolanhe.web.dto.knowledge.KnowledgeSnippetResponse;
import com.xiaolanhe.domain.knowledge.model.KnowledgeSnippet;
import com.xiaolanhe.rag.model.CreateKnowledgeDocumentCommand;
import com.xiaolanhe.rag.model.KnowledgeDocumentSummary;
import com.xiaolanhe.rag.service.KnowledgeDocumentService;
import jakarta.validation.Valid;
import java.util.List;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/knowledge")
public class KnowledgeController {

    private final KnowledgeDocumentService knowledgeDocumentService;

    public KnowledgeController(KnowledgeDocumentService knowledgeDocumentService) {
        this.knowledgeDocumentService = knowledgeDocumentService;
    }

    @PostMapping("/documents")
    public KnowledgeDocumentResponse create(@Valid @RequestBody CreateKnowledgeDocumentRequest request) {
        KnowledgeDocumentSummary summary = knowledgeDocumentService.createDocument(new CreateKnowledgeDocumentCommand(
                request.sourceType(),
                request.title(),
                request.sourceUrl(),
                request.gameCode(),
                request.regionCode(),
                request.patchVersion(),
                request.contentText()
        ));
        return new KnowledgeDocumentResponse(summary.documentId(), summary.chunkCount(), summary.title(), summary.gameCode(), summary.regionCode());
    }

    @GetMapping("/search")
    public KnowledgeSearchResponse search(@RequestParam String query,
                                          @RequestParam(required = false) String gameCode,
                                          @RequestParam(required = false) String regionCode,
                                          @RequestParam(defaultValue = "5") int limit) {
        List<KnowledgeSnippetResponse> items = knowledgeDocumentService.search(query, gameCode, regionCode, limit).stream()
                .map(this::toResponse)
                .toList();
        return new KnowledgeSearchResponse(query, items);
    }

    private KnowledgeSnippetResponse toResponse(KnowledgeSnippet item) {
        return new KnowledgeSnippetResponse(
                item.chunkId(),
                item.documentId(),
                item.title(),
                item.gameCode(),
                item.regionCode(),
                item.patchVersion(),
                item.sourceUrl(),
                item.snippet(),
                item.score()
        );
    }
}