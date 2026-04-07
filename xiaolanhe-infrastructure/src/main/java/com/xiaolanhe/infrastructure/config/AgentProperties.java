package com.xiaolanhe.infrastructure.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "xiaolanhe.agent")
public record AgentProperties(
        String mode,
        String defaultGame,
        String defaultRegion
) {
}