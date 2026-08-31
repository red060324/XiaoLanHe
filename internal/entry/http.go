package entry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"

	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
	"github.com/red060324/XiaoLanHe/internal/presenter"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

const maxRequestBytes = presenter.MaxMessageLength + 1024
const maxKnowledgeBody = 1 << 20
const webRoot = "frontend/xiaolanhe-web/dist"

type HTTP struct {
	server    *server.Hertz
	chat      *usecase.Chat
	knowledge *usecase.Knowledge
	search    *usecase.WebSearch
}

func NewHTTP(address string, chat *usecase.Chat) *HTTP {
	return NewHTTPWithServices(address, chat, nil, nil, nil)
}

func NewHTTPWithServices(address string, chat *usecase.Chat, knowledge *usecase.Knowledge, search *usecase.WebSearch, chatAuthenticator httpauth.Authenticator, knowledgeWriteMiddleware ...app.HandlerFunc) *HTTP {
	h := &HTTP{server: server.Default(server.WithHostPorts(address)), chat: chat, knowledge: knowledge, search: search}
	h.server.Use(httpx.RequestIDMiddleware)
	h.server.GET("/healthz", h.health)
	h.server.GET("/api/system/ping", h.ping)
	if chatAuthenticator == nil {
		h.server.POST("/api/chat/message", h.message)
		h.server.POST("/api/chat/stream", h.stream)
	} else {
		h.server.POST("/api/chat/message", httpauth.Optional(chatAuthenticator), h.message)
		h.server.POST("/api/chat/stream", httpauth.Optional(chatAuthenticator), h.stream)
	}
	if knowledge != nil {
		if len(knowledgeWriteMiddleware) == 0 {
			h.server.POST("/api/knowledge/documents", h.createKnowledge)
		} else {
			h.server.POST("/api/knowledge/documents", append(knowledgeWriteMiddleware, h.createKnowledge)...)
		}
		h.server.GET("/api/knowledge/search", h.searchKnowledge)
	}
	if search != nil {
		h.server.GET("/api/search/web", h.searchWeb)
	}
	registerWeb(h.server, webRoot)
	return h
}

func (h *HTTP) Router() *server.Hertz { return h.server }

func (h *HTTP) RegisterReadiness(check func(context.Context) error) {
	h.server.GET("/readyz", func(ctx context.Context, c *app.RequestContext) {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := check(checkCtx); err != nil {
			httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "service is not ready", nil)
			return
		}
		c.JSON(consts.StatusOK, map[string]string{"status": "ready"})
	})
}

func registerWeb(h *server.Hertz, root string) {
	index := filepath.Join(root, "index.html")
	if _, err := os.Stat(index); err != nil {
		return
	}
	h.StaticFS("/", &app.FS{
		Root:       root,
		IndexNames: []string{"index.html"},
		PathNotFound: func(_ context.Context, c *app.RequestContext) {
			path := string(c.Path())
			if path == "/api" || strings.HasPrefix(path, "/api/") {
				writeError(c, consts.StatusNotFound, "not found")
				return
			}
			c.File(index)
		},
	})
}

func (h *HTTP) ping(_ context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, map[string]string{"name": "xiaolanhe", "status": "ok"})
}

func (h *HTTP) createKnowledge(ctx context.Context, c *app.RequestContext) {
	var request presenter.KnowledgeDocumentRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxKnowledgeBody, &request); err != nil {
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
	query := strings.TrimSpace(string(c.Query("query")))
	if query == "" {
		writeError(c, consts.StatusBadRequest, "query cannot be blank")
		return
	}
	limit := 5
	if raw := string(c.Query("limit")); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 10 {
			writeError(c, consts.StatusBadRequest, "limit must be between 1 and 10")
			return
		}
	}
	items, err := h.knowledge.Search(ctx, query, string(c.Query("gameCode")), string(c.Query("regionCode")), limit)
	if err != nil {
		if errors.Is(err, usecase.ErrInvalidSearchQuery) {
			writeError(c, consts.StatusBadRequest, "query must contain 1 to 100 characters")
			return
		}
		slog.ErrorContext(ctx, "search knowledge", "error", err)
		writeError(c, consts.StatusInternalServerError, "knowledge search failed")
		return
	}
	c.JSON(consts.StatusOK, presenter.PresentKnowledge(query, items))
}

func (h *HTTP) searchWeb(ctx context.Context, c *app.RequestContext) {
	query := strings.TrimSpace(string(c.Query("query")))
	if query == "" {
		writeError(c, consts.StatusBadRequest, "query cannot be blank")
		return
	}
	result, err := h.search.Run(ctx, query)
	if err != nil {
		if errors.Is(err, usecase.ErrInvalidSearchQuery) {
			writeError(c, consts.StatusBadRequest, "query must contain 1 to 100 characters")
			return
		}
		slog.ErrorContext(ctx, "web search", "error", err)
		writeError(c, consts.StatusServiceUnavailable, "web search failed")
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
			"request_id", httpx.RequestID(c),
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
		if errors.Is(err, usecase.ErrConversationForbidden) {
			resultStatus = "forbidden"
			writeError(c, consts.StatusForbidden, "conversation is not accessible")
			return
		}
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
			"request_id", httpx.RequestID(c),
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
		if errors.Is(err, usecase.ErrConversationForbidden) {
			resultStatus = "forbidden"
			writeError(c, consts.StatusForbidden, "conversation is not accessible")
			return
		}
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
	if err := httpx.DecodeJSON(c.Request.Body(), maxRequestBytes, &request); err != nil {
		writeError(c, consts.StatusBadRequest, "invalid JSON body")
		return usecase.ChatInput{}, false
	}
	in, err := request.Input()
	if err != nil {
		writeError(c, consts.StatusBadRequest, err.Error())
		return usecase.ChatInput{}, false
	}
	if principal, ok := httpauth.Principal(c); ok {
		in.UserID = principal.UserID
	}
	return in, true
}

func writeError(c *app.RequestContext, status int, message string) {
	code := "internal_error"
	switch status {
	case consts.StatusBadRequest, consts.StatusRequestEntityTooLarge:
		code = "invalid_request"
	case consts.StatusNotFound:
		code = "not_found"
	case consts.StatusForbidden:
		code = "conversation_forbidden"
	case consts.StatusServiceUnavailable:
		code = "dependency_unavailable"
	}
	httpx.WriteError(c, status, code, message, nil)
}
