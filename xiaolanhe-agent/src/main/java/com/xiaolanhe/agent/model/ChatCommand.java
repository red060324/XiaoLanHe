package com.xiaolanhe.agent.model;

public record ChatCommand(
        String sessionId,
        String message
) {
}
