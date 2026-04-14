package com.xiaolanhe.infrastructure.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "xiaolanhe.agent")
public record AgentProperties(
        String mode,
        Memory memory,
        Verification verification,
        Models models
) {
    public record Memory(
            boolean enabled,
            int recentWindowSize,
            int summaryTriggerMessageCount
    ) {
    }

    public record Verification(
            boolean enabled
    ) {
    }

    public record Models(
            String mainAgentPlanning,
            String memorySummary,
            String searchAgentPlanning,
            String synthesis
    ) {
    }
}
