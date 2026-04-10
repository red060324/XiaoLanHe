package com.xiaolanhe.infrastructure.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "xiaolanhe.agent")
public record AgentProperties(
        String mode,
        Memory memory
) {
    public record Memory(
            boolean enabled,
            int recentWindowSize,
            int summaryTriggerMessageCount
    ) {
    }
}
