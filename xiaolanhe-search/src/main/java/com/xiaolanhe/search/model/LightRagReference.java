package com.xiaolanhe.search.model;

import java.util.List;

public record LightRagReference(
        String referenceId,
        String filePath,
        List<String> content
) {
}
