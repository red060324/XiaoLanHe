package com.xiaolanhe.infrastructure.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

@ConfigurationProperties(prefix = "xiaolanhe.storage")
public record StorageProperties(
        Minio minio
) {

    public record Minio(
            String endpoint,
            String accessKey,
            String secretKey,
            String bucket
    ) {
    }
}