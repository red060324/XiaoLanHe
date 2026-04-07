package com.xiaolanhe.web.controller;

import com.xiaolanhe.agent.model.ChatCommand;
import com.xiaolanhe.agent.model.ChatResult;
import com.xiaolanhe.agent.service.ChatService;
import com.xiaolanhe.web.dto.chat.ChatRequest;
import com.xiaolanhe.web.dto.chat.ChatResponse;
import jakarta.validation.Valid;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/chat")
public class ChatController {

    private final ChatService chatService;

    public ChatController(ChatService chatService) {
        this.chatService = chatService;
    }

    @PostMapping("/message")
    public ChatResponse message(@Valid @RequestBody ChatRequest request) {
        ChatResult result = chatService.chat(new ChatCommand(
                request.sessionId(),
                request.message(),
                request.gameCode(),
                request.regionCode()
        ));
        return new ChatResponse(result.sessionId(), result.answer(), result.model(), result.fallback(), result.createdAt());
    }
}