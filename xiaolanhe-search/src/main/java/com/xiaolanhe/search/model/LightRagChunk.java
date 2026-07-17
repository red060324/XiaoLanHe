package com.xiaolanhe.search.model;

public record LightRagChunk(
        String referenceId,
        String content,
        String filePath,
        String chunkId
) {
}
