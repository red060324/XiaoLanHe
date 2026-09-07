package entry

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/http1/ext"
	"github.com/cloudwego/hertz/pkg/protocol/sse"

	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
	"github.com/red060324/XiaoLanHe/internal/presenter"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

const maxRequestBytes = presenter.MaxMessageLength + 1024
const maxDefaultRequestBytes = 1 << 20
const requestBodyPrefetchBytes = 8 << 10
const webRoot = "frontend/xiaolanhe-web/dist"
const readinessOverallTimeout = 2 * time.Second
const readinessDependencyTimeout = 1500 * time.Millisecond

type HTTP struct {
	server *server.Hertz
	chat   *usecase.Chat
	search *usecase.WebSearch
}

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

func NewHTTP(address string, chat *usecase.Chat) *HTTP {
	return NewHTTPWithServices(address, chat, nil, nil)
}

func NewHTTPWithServices(address string, chat *usecase.Chat, search *usecase.WebSearch, chatAuthenticator httpauth.Authenticator) *HTTP {
	return newHTTPWithServices(newHertzServer(address), chat, search, chatAuthenticator)
}

func newHertzServer(address string, extraOptions ...config.Option) *server.Hertz {
	options := []config.Option{
		server.WithHostPorts(address),
		server.WithSenseClientDisconnection(true),
		// Streaming turns this transport value into a bounded prefetch size;
		// limitRequestBody below enforces the route-specific hard limit.
		server.WithMaxRequestBodySize(requestBodyPrefetchBytes),
		server.WithStreamBody(true),
		server.WithDisablePreParseMultipartForm(true),
	}
	return server.Default(append(options, extraOptions...)...)
}

func newHTTPWithServices(hertzServer *server.Hertz, chat *usecase.Chat, search *usecase.WebSearch, chatAuthenticator httpauth.Authenticator) *HTTP {
	hertzServer.Engine.ContinueHandler = func(header *protocol.RequestHeader) bool {
		return header.ContentLength() <= requestBodyLimit(header.RequestURI())
	}
	h := &HTTP{
		server: hertzServer,
		chat:   chat,
		search: search,
	}
	h.server.Use(httpx.RequestIDMiddleware, limitRequestBody())
	h.server.GET("/healthz", h.health)
	h.server.GET("/api/system/ping", h.ping)
	if chatAuthenticator == nil {
		h.server.POST("/api/chat/message", h.message)
		h.server.POST("/api/chat/stream", h.stream)
	} else {
		h.server.POST("/api/chat/message", httpauth.Optional(chatAuthenticator), h.message)
		h.server.POST("/api/chat/stream", httpauth.Optional(chatAuthenticator), h.stream)
	}
	if search != nil {
		h.server.GET("/api/search/web", h.searchWeb)
	}
	registerWeb(h.server, webRoot)
	return h
}

func requestBodyLimit(requestURI []byte) int {
	path := string(requestURI)
	if query := strings.IndexByte(path, '?'); query >= 0 {
		path = path[:query]
	}
	if path == "/api/chat/message" || path == "/api/chat/stream" {
		return maxRequestBytes
	}
	return maxDefaultRequestBytes
}

func limitRequestBody() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		limit := requestBodyLimit(c.Path())
		if c.Request.Header.ContentLength() > limit {
			rejectOversizedRequest(c)
			return
		}
		if !c.Request.IsBodyStream() {
			if len(c.Request.BodyBytes()) > limit {
				rejectOversizedRequest(c)
				return
			}
			c.Next(ctx)
			return
		}

		stream := c.RequestBodyStream()
		body, err := io.ReadAll(io.LimitReader(stream, int64(limit)+1))
		if err != nil {
			releaseRejectedRequestBody(c, stream)
			httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "invalid request body", nil)
			c.Abort()
			return
		}
		if len(body) > limit {
			releaseRejectedRequestBody(c, stream)
			rejectOversizedRequest(c)
			return
		}

		_ = c.Request.CloseBodyStream()
		if err := ext.ReleaseBodyStream(stream); err != nil {
			c.SetConnectionClose()
			httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "invalid request body", nil)
			c.Abort()
			return
		}
		_ = c.Request.CloseBodyStream()
		c.Request.SetBody(body)
		c.Request.Header.SetContentLength(len(body))
		c.Next(ctx)
	}
}

func rejectOversizedRequest(c *app.RequestContext) {
	if c.Request.IsBodyStream() {
		releaseRejectedRequestBody(c, c.RequestBodyStream())
	} else {
		c.SetConnectionClose()
	}
	httpx.WriteError(c, consts.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", nil)
	c.Abort()
}

func releaseRejectedRequestBody(c *app.RequestContext, stream io.Reader) {
	// Poison further reads before release so Hertz's body-stream cleanup fails
	// promptly instead of draining attacker-controlled input for keep-alive.
	c.SetConnectionClose()
	connection := c.GetConn()
	if connection == nil || connection.SetReadDeadline(time.Now()) != nil {
		_ = c.Request.CloseBodyStream()
		return
	}
	_ = ext.ReleaseBodyStream(stream)
	_ = c.Request.CloseBodyStream()
}

func (h *HTTP) Router() *server.Hertz { return h.server }

func (h *HTTP) RegisterReadiness(check func(context.Context) error) {
	h.RegisterReadinessChecks(check)
}

func (h *HTTP) RegisterReadinessChecks(checks ...func(context.Context) error) {
	h.server.GET("/readyz", func(ctx context.Context, c *app.RequestContext) {
		if err := runReadinessChecks(ctx, readinessOverallTimeout, readinessDependencyTimeout, checks); err != nil {
			httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "service is not ready", nil)
			return
		}
		c.JSON(consts.StatusOK, map[string]string{"status": "ready"})
	})
}

// runReadinessChecks starts every dependency check at the same time. Each check
// receives an independent deadline while the caller retains a separate overall
// response bound. The buffered result channel lets a context-aware check finish
// after the caller has already timed out without blocking its goroutine.
func runReadinessChecks(ctx context.Context, overallTimeout, dependencyTimeout time.Duration, checks []func(context.Context) error) error {
	if overallTimeout <= 0 || dependencyTimeout <= 0 || len(checks) == 0 {
		return errors.New("invalid readiness configuration")
	}
	overallCtx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()
	results := make(chan error, len(checks))
	for _, check := range checks {
		check := check
		go func() {
			checkCtx, checkCancel := context.WithTimeout(overallCtx, dependencyTimeout)
			defer checkCancel()
			results <- runReadinessCheck(checkCtx, check)
		}()
	}
	var firstError error
	for range checks {
		select {
		case err := <-results:
			if err != nil && firstError == nil {
				firstError = err
			}
		case <-overallCtx.Done():
			return overallCtx.Err()
		}
	}
	return firstError
}

func runReadinessCheck(ctx context.Context, check func(context.Context) error) (err error) {
	if check == nil {
		return errors.New("readiness check is nil")
	}
	defer func() {
		if recover() != nil {
			err = errors.New("readiness check panicked")
		}
	}()
	return check(ctx)
}

// RegisterMetrics exposes process metrics only when a distinct operator token
// is configured. It is deliberately not registered by the convenience
// constructors used by public-only deployments.
func (h *HTTP) RegisterMetrics(token string, registry *platformmetrics.Registry) {
	if token == "" || registry == nil {
		return
	}
	h.server.GET("/metrics", func(_ context.Context, c *app.RequestContext) {
		provided := strings.TrimSpace(string(c.Request.Header.Peek("Authorization")))
		expected := "Bearer " + token
		if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			c.Header("WWW-Authenticate", `Bearer realm="metrics"`)
			httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", nil)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(consts.StatusOK, prometheusContentType, registry.Prometheus())
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

func (h *HTTP) searchWeb(ctx context.Context, c *app.RequestContext) {
	query := strings.TrimSpace(string(c.Query("query")))
	if query == "" {
		writeError(c, consts.StatusBadRequest, "query cannot be blank")
		return
	}
	result, err := h.search.Run(ctx, query)
	if err != nil {
		if httpx.WriteDeadlineError(c, err) {
			return
		}
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
		if httpx.WriteDeadlineError(c, err) {
			resultStatus = "deadline_exceeded"
			return
		}
		if errors.Is(err, usecase.ErrConversationForbidden) {
			resultStatus = "forbidden"
			writeError(c, consts.StatusForbidden, "conversation is not accessible")
			return
		}
		resultStatus = "error"
		slog.ErrorContext(ctx, "chat failed", "event", "assistant.http", "operation", "message", "error_class", chatErrorClass(err))
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
		if httpx.WriteDeadlineError(c, err) {
			resultStatus = "deadline_exceeded"
			return
		}
		if errors.Is(err, usecase.ErrConversationForbidden) {
			resultStatus = "forbidden"
			writeError(c, consts.StatusForbidden, "conversation is not accessible")
			return
		}
		resultStatus = "error"
		slog.ErrorContext(ctx, "chat stream failed", "event", "assistant.http", "operation", "stream_start", "error_class", chatErrorClass(err))
		writeError(c, consts.StatusInternalServerError, "chat failed")
		return
	}
	route = result.Route
	defer result.Stream.Close()

	c.Header("X-Conversation-ID", result.SessionID)
	writer := sse.NewWriter(c)
	for {
		chunk, err := result.Stream.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				resultStatus = "cancelled"
				return
			}
			resultStatus = "error"
			slog.ErrorContext(ctx, "chat stream interrupted", "event", "assistant.http", "operation", "stream_read", "error_class", chatErrorClass(err))
			body, marshalErr := json.Marshal(httpx.ErrorBody{Error: httpx.ErrorDetail{
				Code:      "stream_failed",
				Message:   "assistant stream failed",
				RequestID: httpx.RequestID(c),
			}})
			if marshalErr != nil {
				slog.ErrorContext(ctx, "marshal chat stream error", "event", "assistant.http", "operation", "stream_error_encode", "error_class", "encoding")
				return
			}
			if err := writer.WriteEvent("", "error", body); err != nil {
				resultStatus = "cancelled"
				slog.InfoContext(ctx, "chat stream disconnected", "event", "assistant.http", "operation", "stream_error_write", "error_class", "disconnected")
			}
			return
		}
		if err := writer.WriteEvent("", "message", []byte(chunk)); err != nil {
			resultStatus = "cancelled"
			slog.InfoContext(ctx, "chat stream disconnected", "event", "assistant.http", "operation", "stream_write", "error_class", "disconnected")
			return
		}
	}
}

func chatErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, usecase.ErrConversationForbidden):
		return "forbidden"
	default:
		return "dependency"
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
