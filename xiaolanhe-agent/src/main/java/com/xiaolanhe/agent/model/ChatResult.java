package com.xiaolanhe.agent.model;

import java.time.OffsetDateTime;

public record ChatResult(
        String sessionId,
        String answer,
        String model,
        boolean fallback,
        OffsetDateTime createdAt
) {
}