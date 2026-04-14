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

    @Bean("mainAgentPlanningChatClient")
    public ChatClient mainAgentPlanningChatClient(ChatClient.Builder chatClientBuilder,
                                                  @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel,
                                                  @Value("classpath:/prompts/main-agent-planning.md") Resource planningPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(chatModel);
        defaultOptions.setTemperature(0.2);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(planningPrompt)
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

    @Bean("synthesisChatClient")
    public ChatClient synthesisChatClient(ChatClient.Builder chatClientBuilder,
                                          @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel,
                                          @Value("${spring.ai.openai.chat.options.temperature:0.4}") Double temperature,
                                          @Value("classpath:/prompts/synthesis.md") Resource synthesisPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(chatModel);
        defaultOptions.setTemperature(temperature);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(synthesisPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }

    @Bean("synthesisVerificationChatClient")
    public ChatClient synthesisVerificationChatClient(ChatClient.Builder chatClientBuilder,
                                                      @Value("${spring.ai.openai.chat.options.model:qwen3.5-plus}") String chatModel,
                                                      @Value("classpath:/prompts/verification.md") Resource verificationPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(chatModel);
        defaultOptions.setTemperature(0.1);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(verificationPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }
}
