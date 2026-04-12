package com.xiaolanhe.search.model;

import java.util.List;

public record EvidenceBundle(
        String query,
        boolean usedLocalKnowledge,
        boolean usedWebSearch,
        boolean freshnessRequired,
        List<EvidenceItem> items,
        List<String> notes
) {
}
