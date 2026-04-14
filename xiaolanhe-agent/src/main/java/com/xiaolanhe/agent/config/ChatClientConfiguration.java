package com.xiaolanhe.agent.config;

import com.xiaolanhe.infrastructure.config.AgentProperties;
import java.util.Map;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.core.io.Resource;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class ChatClientConfiguration {

    @Bean("mainAgentPlanningChatClient")
    public ChatClient mainAgentPlanningChatClient(ChatClient.Builder chatClientBuilder,
                                                  AgentProperties agentProperties,
                                                  @Value("classpath:/prompts/main-agent-planning.md") Resource planningPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(resolveModel(agentProperties, "qwen3.5-flash", AgentProperties.Models::mainAgentPlanning));
        defaultOptions.setTemperature(0.2);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(planningPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }

    @Bean("searchAgentPlanningChatClient")
    public ChatClient searchAgentPlanningChatClient(ChatClient.Builder chatClientBuilder,
                                                    AgentProperties agentProperties,
                                                    @Value("classpath:/prompts/search-agent-decomposition.md") Resource searchPlanningPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(resolveModel(agentProperties, "qwen3.5-plus", AgentProperties.Models::searchAgentPlanning));
        defaultOptions.setTemperature(0.2);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(searchPlanningPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }

    @Bean("memorySummaryChatClient")
    public ChatClient memorySummaryChatClient(ChatClient.Builder chatClientBuilder,
                                              AgentProperties agentProperties,
                                              @Value("classpath:/prompts/memory-summary.md") Resource memorySummaryPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(resolveModel(agentProperties, "qwen3.5-flash", AgentProperties.Models::memorySummary));
        defaultOptions.setTemperature(0.2);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(memorySummaryPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }

    @Bean("synthesisChatClient")
    public ChatClient synthesisChatClient(ChatClient.Builder chatClientBuilder,
                                          AgentProperties agentProperties,
                                          @Value("${spring.ai.openai.chat.options.temperature:0.4}") Double temperature,
                                          @Value("classpath:/prompts/synthesis.md") Resource synthesisPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(resolveModel(agentProperties, "qwen3.5-plus", AgentProperties.Models::synthesis));
        defaultOptions.setTemperature(temperature);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(synthesisPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }

    @Bean("synthesisVerificationChatClient")
    public ChatClient synthesisVerificationChatClient(ChatClient.Builder chatClientBuilder,
                                                      AgentProperties agentProperties,
                                                      @Value("classpath:/prompts/verification.md") Resource verificationPrompt) {
        OpenAiChatOptions defaultOptions = new OpenAiChatOptions();
        defaultOptions.setModel(resolveModel(agentProperties, "qwen3.5-plus", AgentProperties.Models::synthesis));
        defaultOptions.setTemperature(0.2);
        defaultOptions.setExtraBody(Map.of("enable_thinking", false));

        return chatClientBuilder
                .defaultSystem(verificationPrompt)
                .defaultOptions(defaultOptions)
                .build();
    }

    private String resolveModel(AgentProperties agentProperties,
                                String fallback,
                                java.util.function.Function<AgentProperties.Models, String> extractor) {
        if (agentProperties == null || agentProperties.models() == null) {
            return fallback;
        }
        String configured = extractor.apply(agentProperties.models());
        return (configured == null || configured.isBlank()) ? fallback : configured;
    }
}
