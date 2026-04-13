package com.xiaolanhe.agent.model;

import java.util.List;

public record SynthesisResult(
        String content,
        String answerType,
        List<String> citations
) {
}
