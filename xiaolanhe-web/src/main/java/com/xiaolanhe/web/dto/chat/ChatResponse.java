package com.xiaolanhe.web.dto.chat;

import java.time.OffsetDateTime;

public record ChatResponse(
        String sessionId,
        String answer,
        String model,
        boolean fallback,
        OffsetDateTime createdAt
) {
}