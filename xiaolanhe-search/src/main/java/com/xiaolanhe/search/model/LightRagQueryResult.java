package com.xiaolanhe.search.model;

import java.util.List;

public record LightRagQueryResult(
        boolean available,
        String query,
        String mode,
        String response,
        java.util.List<LightRagChunk> chunks,
        List<LightRagReference> references,
        String note
) {
}
