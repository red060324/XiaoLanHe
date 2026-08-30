package entry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
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
	server                 *server.Hertz
	chat                   *usecase.Chat
	knowledge              *usecase.Knowledge
	search                 *usecase.WebSearch
	agentMode, minioBucket string
}

func NewHTTP(address string, chat *usecase.Chat) *HTTP {
	return NewHTTPWithServices(address, chat, nil, nil, "single-orchestrator", "xlh-dev")
}

func NewHTTPWithServices(address string, chat *usecase.Chat, knowledge *usecase.Knowledge, search *usecase.WebSearch, agentMode, minioBucket string) *HTTP {
	h := &HTTP{server: server.Default(server.WithHostPorts(address)), chat: chat, knowledge: knowledge, search: search, agentMode: agentMode, minioBucket: minioBucket}
	h.server.GET("/healthz", h.health)
	h.server.GET("/api/system/ping", h.ping)
	h.server.POST("/api/chat/message", h.message)
	h.server.POST("/api/chat/stream", h.stream)
	if knowledge != nil {
		h.server.POST("/api/knowledge/documents", h.createKnowledge)
		h.server.GET("/api/knowledge/search", h.searchKnowledge)
	}
	if search != nil {
		h.server.GET("/api/search/web", h.searchWeb)
	}
	return h
}

func (h *HTTP) ping(_ context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, map[string]any{"name": "xiaolanhe", "status": "ok", "agentMode": h.agentMode, "minioBucket": h.minioBucket})
}

func (h *HTTP) createKnowledge(ctx context.Context, c *app.RequestContext) {
	var request presenter.KnowledgeDocumentRequest
	if err := json.Unmarshal(c.Request.Body(), &request); err != nil {
		writeError(c, consts.StatusBadRequest, "invalid JSON body")
		return
	}
	document, err := request.Input()
	if err != nil {
		writeError(c, consts.StatusBadRequest, err.Error())
		return
	}
	id, count, err := h.knowledge.Create(ctx, document)
	if err != nil {
		slog.ErrorContext(ctx, "create knowledge document", "error", err)
		writeError(c, consts.StatusInternalServerError, "create knowledge document failed")
		return
	}
	c.JSON(consts.StatusOK, presenter.KnowledgeDocumentResponse{DocumentID: id, ChunkCount: count, Title: document.Title, GameCode: document.GameCode, RegionCode: document.RegionCode})
}

func (h *HTTP) searchKnowledge(ctx context.Context, c *app.RequestContext) {
	query := string(c.Query("query"))
	if query == "" {
		writeError(c, consts.StatusBadRequest, "query cannot be blank")
		return
	}
	limit, _ := strconv.Atoi(string(c.Query("limit")))
	if limit == 0 {
		limit = 5
	}
	items, err := h.knowledge.Search(ctx, query, string(c.Query("gameCode")), string(c.Query("regionCode")), limit)
	if err != nil {
		slog.ErrorContext(ctx, "search knowledge", "error", err)
		writeError(c, consts.StatusInternalServerError, "knowledge search failed")
		return
	}
	c.JSON(consts.StatusOK, presenter.PresentKnowledge(query, items))
}

func (h *HTTP) searchWeb(ctx context.Context, c *app.RequestContext) {
	query := string(c.Query("query"))
	if query == "" {
		writeError(c, consts.StatusBadRequest, "query cannot be blank")
		return
	}
	result, err := h.search.Run(ctx, query)
	if err != nil {
		slog.ErrorContext(ctx, "web search", "error", err)
		writeError(c, consts.StatusBadGateway, "web search failed")
		return
	}
	c.JSON(consts.StatusOK, presenter.PresentWebSearch(result))
}

func (h *HTTP) Spin() { h.server.Spin() }

func (h *HTTP) health(_ context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, map[string]string{"status": "ok"})
}

func (h *HTTP) message(ctx context.Context, c *app.RequestContext) {
	started := time.Now()
	resultStatus := "ok"
	route := "unknown"
	defer func() {
		slog.InfoContext(ctx, "chat request completed",
			"request_id", string(c.Request.Header.Peek("X-Request-ID")),
			"route", route, "node", "answer", "provider", "eino",
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
	route = result.Route
	c.JSON(consts.StatusOK, presenter.PresentChat(result))
}

func (h *HTTP) stream(ctx context.Context, c *app.RequestContext) {
	started := time.Now()
	resultStatus := "ok"
	route := "unknown"
	defer func() {
		slog.InfoContext(ctx, "chat stream completed",
			"request_id", string(c.Request.Header.Peek("X-Request-ID")),
			"route", route, "node", "answer", "provider", "eino",
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
	route = result.Route
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
