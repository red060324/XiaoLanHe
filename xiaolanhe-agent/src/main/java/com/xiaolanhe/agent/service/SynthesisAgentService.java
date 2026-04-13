package com.xiaolanhe.agent.service;

import com.xiaolanhe.agent.model.SynthesisRequest;
import com.xiaolanhe.agent.model.SynthesisResult;
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

    public SynthesisAgentService(@Qualifier("synthesisChatClient") ChatClient synthesisChatClient) {
        this.synthesisChatClient = synthesisChatClient;
    }

    public SynthesisResult synthesize(SynthesisRequest request) {
        String content = synthesisChatClient.prompt()
                .user(buildUserPrompt(request))
                .call()
                .content();
        return new SynthesisResult(content, request.responseMode(), extractCitations(request.evidenceBundle()));
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
