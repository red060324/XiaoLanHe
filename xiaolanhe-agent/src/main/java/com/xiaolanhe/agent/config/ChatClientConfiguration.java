package com.xiaolanhe.agent.config;

import java.util.Map;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.core.io.Resource;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class ChatClientConfiguration {

    @Bean
    public ChatClient chatClient(ChatClient.Builder chatClientBuilder,
                                 @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel,
                                 @Value("${spring.ai.openai.chat.options.temperature:0.4}") Double temperature,
                                 @Value("classpath:/prompts/system.md") Resource systemPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(chatModel);
        defaultOptions.setTemperature(temperature);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(systemPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }

    @Bean("memorySummaryChatClient")
    public ChatClient memorySummaryChatClient(ChatClient.Builder chatClientBuilder,
                                              @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel,
                                              @Value("classpath:/prompts/memory-summary.md") Resource memorySummaryPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(chatModel);
        defaultOptions.setTemperature(0.2);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(memorySummaryPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }
}
