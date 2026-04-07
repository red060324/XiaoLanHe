package com.xiaolanhe.infrastructure.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "xiaolanhe.messaging")
public record MessagingProperties(
        Topics topics
) {

    public record Topics(
            String chatAudit
    ) {
    }
}