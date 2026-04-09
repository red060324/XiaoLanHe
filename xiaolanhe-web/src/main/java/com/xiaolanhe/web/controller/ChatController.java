package com.xiaolanhe.web.controller;

import com.xiaolanhe.agent.service.ChatService;
import com.xiaolanhe.web.dto.chat.ChatRequest;
import com.xiaolanhe.web.dto.chat.ChatResponse;
import jakarta.validation.Valid;
import org.springframework.http.MediaType;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;
import reactor.core.publisher.Flux;

@RestController
@RequestMapping("/api/chat")
public class ChatController {

    private final ChatService chatService;

    public ChatController(ChatService chatService) {
        this.chatService = chatService;
    }

    @PostMapping("/message")
    public ChatResponse message(@Valid @RequestBody ChatRequest request) {
        ChatService.ChatResponseData result = chatService.chat(request.sessionId(), request.message());
        return new ChatResponse(result.sessionId(), result.answer(), result.createdAt());
    }

    @PostMapping(path = "/stream", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    public Flux<String> stream(@Valid @RequestBody ChatRequest request) {
        return chatService.stream(request.sessionId(), request.message());
    }
}