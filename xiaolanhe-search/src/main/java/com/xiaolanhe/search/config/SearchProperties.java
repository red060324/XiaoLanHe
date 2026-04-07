package com.xiaolanhe.search.config;

import java.time.Duration;
import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "xiaolanhe.search")
public record SearchProperties(
        boolean enabled,
        String provider,
        String endpoint,
        String apiKey,
        Duration cacheTtl
) {
}