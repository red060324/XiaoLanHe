package entry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"

	"github.com/red060324/XiaoLanHe/internal/presenter"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

const maxRequestBytes = presenter.MaxMessageLength + 1024

type HTTP struct {
	server *server.Hertz
	chat   *usecase.Chat
}

func NewHTTP(address string, chat *usecase.Chat) *HTTP {
	h := &HTTP{server: server.Default(server.WithHostPorts(address)), chat: chat}
	h.server.GET("/healthz", h.health)
	h.server.POST("/api/chat/message", h.message)
	h.server.POST("/api/chat/stream", h.stream)
	return h
}

func (h *HTTP) Spin() { h.server.Spin() }

func (h *HTTP) health(_ context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, map[string]string{"status": "ok"})
}

func (h *HTTP) message(ctx context.Context, c *app.RequestContext) {
	started := time.Now()
	resultStatus := "ok"
	defer func() {
		slog.InfoContext(ctx, "chat request completed",
			"request_id", string(c.Request.Header.Peek("X-Request-ID")),
			"route", "direct_chat", "node", "direct_answer", "provider", "eino",
			"result", resultStatus, "latency_ms", time.Since(started).Milliseconds())
	}()
	in, ok := bind(c)
	if !ok {
		resultStatus = "invalid_request"
		return
	}
	result, err := h.chat.Run(ctx, in)
	if err != nil {
		resultStatus = "error"
		slog.ErrorContext(ctx, "chat failed", "error", err)
		writeError(c, consts.StatusInternalServerError, "chat failed")
		return
	}
	c.JSON(consts.StatusOK, presenter.PresentChat(result))
}

func (h *HTTP) stream(ctx context.Context, c *app.RequestContext) {
	started := time.Now()
	resultStatus := "ok"
	defer func() {
		slog.InfoContext(ctx, "chat stream completed",
			"request_id", string(c.Request.Header.Peek("X-Request-ID")),
			"route", "direct_chat", "node", "direct_answer", "provider", "eino",
			"result", resultStatus, "latency_ms", time.Since(started).Milliseconds())
	}()
	in, ok := bind(c)
	if !ok {
		resultStatus = "invalid_request"
		return
	}
	result, err := h.chat.Stream(ctx, in)
	if err != nil {
		resultStatus = "error"
		slog.ErrorContext(ctx, "chat stream failed", "error", err)
		writeError(c, consts.StatusInternalServerError, "chat failed")
		return
	}
	defer result.Stream.Close()

	writer := sse.NewWriter(c)
	for {
		chunk, err := result.Stream.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			resultStatus = "error"
			slog.ErrorContext(ctx, "chat stream interrupted", "error", err)
			return
		}
		if err := writer.WriteEvent("", "message", []byte(chunk)); err != nil {
			resultStatus = "cancelled"
			slog.InfoContext(ctx, "chat stream disconnected", "error", err)
			return
		}
	}
}

func bind(c *app.RequestContext) (usecase.ChatInput, bool) {
	if len(c.Request.Body()) > maxRequestBytes {
		writeError(c, consts.StatusRequestEntityTooLarge, "request body is too large")
		return usecase.ChatInput{}, false
	}
	var request presenter.ChatRequest
	if err := json.Unmarshal(c.Request.Body(), &request); err != nil {
		writeError(c, consts.StatusBadRequest, "invalid JSON body")
		return usecase.ChatInput{}, false
	}
	in, err := request.Input()
	if err != nil {
		writeError(c, consts.StatusBadRequest, err.Error())
		return usecase.ChatInput{}, false
	}
	return in, true
}

func writeError(c *app.RequestContext, status int, message string) {
	c.JSON(status, map[string]string{"error": message})
}
