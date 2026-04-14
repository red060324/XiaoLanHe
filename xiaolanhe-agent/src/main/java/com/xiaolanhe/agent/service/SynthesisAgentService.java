package com.xiaolanhe.agent.service;

import com.xiaolanhe.agent.model.SynthesisRequest;
import com.xiaolanhe.agent.model.SynthesisResult;
import com.xiaolanhe.agent.model.VerificationResult;
import com.xiaolanhe.infrastructure.config.AgentProperties;
import com.xiaolanhe.search.model.EvidenceBundle;
import com.xiaolanhe.search.model.EvidenceItem;
import java.util.ArrayList;
import java.util.List;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

@Service
public class SynthesisAgentService {

    private final ChatClient synthesisChatClient;
    private final ChatClient synthesisVerificationChatClient;
    private final AgentProperties agentProperties;

    public SynthesisAgentService(@Qualifier("synthesisChatClient") ChatClient synthesisChatClient,
                                 @Qualifier("synthesisVerificationChatClient") ChatClient synthesisVerificationChatClient,
                                 AgentProperties agentProperties) {
        this.synthesisChatClient = synthesisChatClient;
        this.synthesisVerificationChatClient = synthesisVerificationChatClient;
        this.agentProperties = agentProperties;
    }

    public SynthesisResult synthesize(SynthesisRequest request) {
        String content = synthesisChatClient.prompt()
                .user(buildUserPrompt(request))
                .call()
                .content();
        VerificationResult verificationResult = verifyAnswer(request, content);
        String finalContent = verificationResult.revised() && StringUtils.hasText(verificationResult.revisedContent())
                ? verificationResult.revisedContent()
                : content;
        return new SynthesisResult(finalContent, request.responseMode(), extractCitations(request.evidenceBundle()));
    }

    public Flux<String> streamSynthesis(SynthesisRequest request) {
        return synthesisChatClient.prompt()
                .user(buildUserPrompt(request))
                .stream()
                .content();
    }

    private String buildUserPrompt(SynthesisRequest request) {
        String contextText = request.contextSnapshot() == null ? "" : request.contextSnapshot().promptContext();
        String evidenceText = buildEvidenceSection(request.evidenceBundle());

        return """
                【输出模式】
                %s

                【用户问题】
                %s

                【上下文】
                %s

                【证据材料】
                %s
                """.formatted(
                defaultText(request.responseMode(), "chat"),
                request.query(),
                defaultText(contextText, "无"),
                defaultText(evidenceText, "无")
        ).trim();
    }

    private String buildEvidenceSection(EvidenceBundle evidenceBundle) {
        if (evidenceBundle == null || evidenceBundle.items().isEmpty()) {
            return "";
        }

        StringBuilder builder = new StringBuilder();
        int index = 1;
        for (EvidenceItem item : evidenceBundle.items()) {
            builder.append(index++)
                    .append(". [")
                    .append(item.sourceType())
                    .append("] ")
                    .append(item.title())
                    .append('\n')
                    .append(item.content())
                    .append('\n');
            if (StringUtils.hasText(item.sourceUrl())) {
                builder.append("来源：").append(item.sourceUrl()).append('\n');
            }
            builder.append('\n');
        }
        if (!evidenceBundle.notes().isEmpty()) {
            builder.append("检索备注：\n");
            evidenceBundle.notes().forEach(note -> builder.append("- ").append(note).append('\n'));
        }
        return builder.toString().trim();
    }

    public VerificationResult verifyAnswer(SynthesisRequest request, String answer) {
        if (agentProperties.verification() == null || !agentProperties.verification().enabled()) {
            return VerificationResult.pass("Verification disabled.");
        }
        if (!StringUtils.hasText(answer)) {
            return VerificationResult.pass("答案为空，跳过校验。");
        }

        String verificationOutput = synthesisVerificationChatClient.prompt()
                .user(buildVerificationPrompt(request, answer))
                .call()
                .content();

        return parseVerificationResult(verificationOutput, answer);
    }

    private String buildVerificationPrompt(SynthesisRequest request, String answer) {
        String contextText = request.contextSnapshot() == null ? "" : request.contextSnapshot().promptContext();
        String evidenceText = buildEvidenceSection(request.evidenceBundle());

        return """
                【用户问题】
                %s

                【输出模式】
                %s

                【上下文】
                %s

                【证据材料】
                %s

                【候选答案】
                %s
                """.formatted(
                request.query(),
                defaultText(request.responseMode(), "chat"),
                defaultText(contextText, "无"),
                defaultText(evidenceText, "无"),
                answer
        ).trim();
    }

    private VerificationResult parseVerificationResult(String output, String originalAnswer) {
        if (!StringUtils.hasText(output)) {
            return VerificationResult.pass("校验结果为空，默认通过。");
        }

        String normalized = output.replace("\r", "").trim();
        String[] lines = normalized.split("\n");
        String decision = "";
        String reason = "";
        StringBuilder revised = new StringBuilder();
        boolean readingRevisedAnswer = false;

        for (String line : lines) {
            if (line.startsWith("DECISION:")) {
                decision = line.substring("DECISION:".length()).trim();
                continue;
            }
            if (line.startsWith("REASON:")) {
                reason = line.substring("REASON:".length()).trim();
                continue;
            }
            if (line.startsWith("REVISED_ANSWER:")) {
                readingRevisedAnswer = true;
                String inline = line.substring("REVISED_ANSWER:".length()).trim();
                if (StringUtils.hasText(inline)) {
                    revised.append(inline);
                }
                continue;
            }
            if (readingRevisedAnswer) {
                if (revised.length() > 0) {
                    revised.append('\n');
                }
                revised.append(line);
            }
        }

        if ("REVISE".equalsIgnoreCase(decision) && StringUtils.hasText(revised.toString())) {
            return new VerificationResult(false, true, defaultText(reason, "校验建议修正答案。"), revised.toString().trim());
        }
        return VerificationResult.pass(defaultText(reason, "校验通过。"));
    }

    private List<String> extractCitations(EvidenceBundle evidenceBundle) {
        if (evidenceBundle == null || evidenceBundle.items().isEmpty()) {
            return List.of();
        }
        List<String> citations = new ArrayList<>();
        for (EvidenceItem item : evidenceBundle.items()) {
            if (StringUtils.hasText(item.sourceUrl())) {
                citations.add(item.sourceUrl());
            }
        }
        return citations.stream().distinct().toList();
    }

    private String defaultText(String value, String fallback) {
        return StringUtils.hasText(value) ? value : fallback;
    }
}
