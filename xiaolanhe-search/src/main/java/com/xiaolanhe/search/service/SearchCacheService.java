package com.xiaolanhe.search.service;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.xiaolanhe.search.model.SearchResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.springframework.stereotype.Service;
import org.springframework.util.DigestUtils;

@Service
public class SearchCacheService {

    private static final TypeReference<SearchResponse> RESPONSE_TYPE = new TypeReference<>() {
    };

    private final StringRedisTemplate redisTemplate;
    private final ObjectMapper objectMapper;

    public SearchCacheService(StringRedisTemplate redisTemplate, ObjectMapper objectMapper) {
        this.redisTemplate = redisTemplate;
        this.objectMapper = objectMapper;
    }

    public SearchResponse get(String query) {
        try {
            String cached = redisTemplate.opsForValue().get(key(query));
            if (cached == null) {
                return null;
            }
            SearchResponse response = objectMapper.readValue(cached, RESPONSE_TYPE);
            return response.withCacheHit(true);
        } catch (Exception ex) {
            return null;
        }
    }

    public void put(String query, SearchResponse response, Duration ttl) {
        try {
            redisTemplate.opsForValue().set(key(query), objectMapper.writeValueAsString(response), ttl);
        } catch (Exception ignored) {
        }
    }

    private String key(String query) {
        return "xiaolanhe:search:web:" + DigestUtils.md5DigestAsHex(query.trim().toLowerCase().getBytes(StandardCharsets.UTF_8));
    }
}