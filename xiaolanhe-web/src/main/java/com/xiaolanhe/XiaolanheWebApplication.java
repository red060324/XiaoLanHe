package com.xiaolanhe;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.context.properties.ConfigurationPropertiesScan;

@SpringBootApplication(scanBasePackages = "com.xiaolanhe")
@ConfigurationPropertiesScan("com.xiaolanhe")
public class XiaolanheWebApplication {

    public static void main(String[] args) {
        SpringApplication.run(XiaolanheWebApplication.class, args);
    }
}