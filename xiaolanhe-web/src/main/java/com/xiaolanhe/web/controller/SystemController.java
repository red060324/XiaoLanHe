package com.xiaolanhe.web.controller;

import com.xiaolanhe.infrastructure.config.AgentProperties;
import com.xiaolanhe.infrastructure.config.StorageProperties;
import java.util.Map;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/system")
public class SystemController {

    private final AgentProperties agentProperties;
    private final StorageProperties storageProperties;

    public SystemController(AgentProperties agentProperties, StorageProperties storageProperties) {
        this.agentProperties = agentProperties;
        this.storageProperties = storageProperties;
    }

    @GetMapping("/ping")
    public Map<String, Object> ping() {
        return Map.of(
                "name", "xiaolanhe",
                "status", "ok",
                "agentMode", agentProperties.mode(),
                "minioBucket", storageProperties.minio().bucket()
        );
    }
}
