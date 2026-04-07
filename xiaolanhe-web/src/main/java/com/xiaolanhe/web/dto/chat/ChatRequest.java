package com.xiaolanhe.web.dto.chat;

import jakarta.validation.constraints.NotBlank;

public record ChatRequest(
        String sessionId,
        @NotBlank(message = "message cannot be blank") String message,
        String gameCode,
        String regionCode
) {
}