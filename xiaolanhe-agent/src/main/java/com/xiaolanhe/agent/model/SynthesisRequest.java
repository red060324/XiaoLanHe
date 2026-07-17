package com.xiaolanhe.agent.model;

import com.xiaolanhe.agent.service.MemoryProfileAgentService.ContextSnapshot;
import com.xiaolanhe.search.model.EvidenceBundle;
import java.util.List;

public record SynthesisRequest(
        String query,
        String routeType,
        String responseMode,
        List<String> planningNotes,
        ContextSnapshot contextSnapshot,
        EvidenceBundle evidenceBundle
) {
}
