package com.xiaolanhe.agent.model;

import com.xiaolanhe.agent.service.MemoryProfileAgentService.ContextSnapshot;
import com.xiaolanhe.search.model.EvidenceBundle;

public record SynthesisRequest(
        String query,
        String responseMode,
        ContextSnapshot contextSnapshot,
        EvidenceBundle evidenceBundle
) {
}
