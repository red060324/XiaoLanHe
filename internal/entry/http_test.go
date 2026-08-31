package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestHTTPMessage(t *testing.T) {
	store := &httpStore{}
	h := NewHTTP(":0", usecase.NewChat(store, httpAssistant{}))

	t.Run("keeps the REST response contract", func(t *testing.T) {
		response := ut.PerformRequest(
			h.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"s","message":"hi"}`), Len: -1},
			ut.Header{Key: "Content-Type", Value: "application/json"},
		)
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["sessionId"] != "s" || body["answer"] != "hello" || body["createdAt"] == "" {
			t.Fatalf("body = %#v", body)
		}
	})

	t.Run("rejects a blank message", func(t *testing.T) {
		response := ut.PerformRequest(
			h.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"message":"  "}`), Len: -1},
		)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "message cannot be blank") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("logs the validated request ID", func(t *testing.T) {
		logs, restore := captureDefaultLogger()
		defer restore()
		response := ut.PerformRequest(
			h.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"s","message":"hi"}`), Len: -1},
			ut.Header{Key: "X-Request-ID", Value: "unsafe id"},
		)
		requestID := string(response.Header().Peek("X-Request-ID"))
		if requestID == "" || requestID == "unsafe id" || !strings.Contains(logs.String(), "request_id="+requestID) {
			t.Fatalf("request ID=%q logs=%q", requestID, logs.String())
		}
	})
}

func TestHTTPStream(t *testing.T) {
	upstream := &httpStream{chunks: []string{"first", "second"}}
	h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{stream: upstream}))
	c := app.NewContext(0)
	c.Request.SetBodyString(`{"sessionId":"s","message":"hi"}`)
	w := &captureWriter{}
	c.Response.HijackWriter(w)
	h.stream(context.Background(), c)

	body := w.String()
	if !strings.Contains(body, "event: message\ndata: first\n\n") || !strings.Contains(body, "event: message\ndata: second\n\n") {
		t.Fatalf("unexpected SSE body: %q", body)
	}
	if got := string(c.Response.Header.ContentType()); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if w.flushes != 2 {
		t.Fatalf("flushes = %d", w.flushes)
	}
	if !upstream.closed {
		t.Fatal("upstream stream was not closed")
	}

	t.Run("disconnect closes upstream without saving a partial assistant message", func(t *testing.T) {
		store := &httpStore{}
		upstream := &httpStream{chunks: []string{"first", "second"}}
		h := NewHTTP(":0", usecase.NewChat(store, httpAssistant{stream: upstream}))
		c := app.NewContext(0)
		c.Request.SetBodyString(`{"sessionId":"s","message":"hi"}`)
		c.Response.HijackWriter(&captureWriter{writeErr: errors.New("client disconnected")})

		h.stream(context.Background(), c)

		if !upstream.closed {
			t.Fatal("upstream stream was not closed")
		}
		if len(store.roles) != 1 || store.roles[0] != "user" {
			t.Fatalf("saved roles = %v", store.roles)
		}
	})

	t.Run("logs the validated request ID", func(t *testing.T) {
		logs, restore := captureDefaultLogger()
		defer restore()
		h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
		c := app.NewContext(0)
		c.Request.Header.Set("X-Request-ID", "unsafe id")
		httpx.RequestIDMiddleware(context.Background(), c)
		c.Request.SetBodyString(`{"sessionId":"s","message":"hi"}`)
		c.Response.HijackWriter(&captureWriter{})
		h.stream(context.Background(), c)
		requestID := httpx.RequestID(c)
		if requestID == "" || requestID == "unsafe id" || !strings.Contains(logs.String(), "request_id="+requestID) {
			t.Fatalf("request ID=%q logs=%q", requestID, logs.String())
		}
	})
}

func TestHTTPHealth(t *testing.T) {
	h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
	response := ut.PerformRequest(h.server.Engine, "GET", "/healthz", nil)
	if response.Code != 200 || response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPServesWebAndSPAFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<main>xiaolanhe</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := server.Default()
	registerWeb(h, root)

	for _, path := range []string{"/", "/chat/session"} {
		response := ut.PerformRequest(h.Engine, "GET", path, nil)
		if response.Code != 200 || response.Body.String() != "<main>xiaolanhe</main>" {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	response := ut.PerformRequest(h.Engine, "GET", "/api/missing", nil)
	if response.Code != 404 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPKnowledge(t *testing.T) {
	store := &httpKnowledgeStore{items: []usecase.KnowledgeSnippet{{ChunkID: 1, DocumentID: 2, Title: "Guide", Text: "fact", Score: 30}}}
	knowledge := usecase.NewKnowledge(store, einoDisabledEmbedder{})
	h := NewHTTPWithServices(":0", usecase.NewChat(&httpStore{}, httpAssistant{}), knowledge, usecase.NewWebSearch(httpSearchClient{}), httpauth.RequireRole(httpAuthenticator{}, auth.RoleAdmin))

	anonymous := ut.PerformRequest(h.server.Engine, "POST", "/api/knowledge/documents", &ut.Body{Body: bytes.NewBufferString(`{"sourceType":"note","title":"Guide","contentText":"body"}`), Len: -1})
	if anonymous.Code != 401 {
		t.Fatalf("anonymous status=%d body=%s", anonymous.Code, anonymous.Body.String())
	}
	created := ut.PerformRequest(h.server.Engine, "POST", "/api/knowledge/documents", &ut.Body{Body: bytes.NewBufferString(`{"sourceType":"note","title":"Guide","contentText":"body"}`), Len: -1}, ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"})
	if created.Code != 200 || !strings.Contains(created.Body.String(), `"documentId":11`) || len(store.chunks) != 1 {
		t.Fatalf("status=%d body=%s chunks=%v", created.Code, created.Body.String(), store.chunks)
	}

	searched := ut.PerformRequest(h.server.Engine, "GET", "/api/knowledge/search?query=guide&limit=5", nil)
	if searched.Code != 200 || !strings.Contains(searched.Body.String(), `"snippet":"fact"`) {
		t.Fatalf("status=%d body=%s", searched.Code, searched.Body.String())
	}
}

type httpAuthenticator struct{}

func (httpAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if token != "admin" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return auth.Principal{UserID: 1, Role: auth.RoleAdmin}, nil
}

func TestHTTPWebSearchAndPing(t *testing.T) {
	h := NewHTTPWithServices(":0", usecase.NewChat(&httpStore{}, httpAssistant{}), nil, usecase.NewWebSearch(httpSearchClient{}))
	searched := ut.PerformRequest(h.server.Engine, "GET", "/api/search/web?query=guide", nil)
	if searched.Code != 200 || !strings.Contains(searched.Body.String(), `"provider":"searxng"`) || !strings.Contains(searched.Body.String(), `"title":"A"`) {
		t.Fatalf("status=%d body=%s", searched.Code, searched.Body.String())
	}
	ping := ut.PerformRequest(h.server.Engine, "GET", "/api/system/ping", nil)
	if ping.Code != 200 || ping.Body.String() != `{"name":"xiaolanhe","status":"ok"}` {
		t.Fatalf("status=%d body=%s", ping.Code, ping.Body.String())
	}
}

type httpStore struct {
	id    int64
	roles []string
}

func (s *httpStore) FindOrCreateSession(context.Context, string) (int64, error) {
	if s.id == 0 {
		s.id = 1
	}
	return s.id, nil
}
func (s *httpStore) SaveMessage(_ context.Context, _ int64, role, _, _ string) error {
	s.roles = append(s.roles, role)
	return nil
}
func (*httpStore) LoadContext(context.Context, int64, int) (string, error) { return "", nil }

type httpAssistant struct{ stream *httpStream }

func (httpAssistant) Generate(context.Context, usecase.AssistantInput) (usecase.Answer, error) {
	return usecase.Answer{Text: "hello", Model: "fake"}, nil
}
func (a httpAssistant) Stream(context.Context, usecase.AssistantInput) (usecase.AnswerStream, error) {
	if a.stream == nil {
		a.stream = &httpStream{chunks: []string{"first", "second"}}
	}
	return a.stream, nil
}

type httpStream struct {
	chunks []string
	index  int
	closed bool
}

func (s *httpStream) Recv() (string, error) {
	if s.index == len(s.chunks) {
		return "", io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}
func (s *httpStream) Close()      { s.closed = true }
func (*httpStream) Model() string { return "fake" }

type einoDisabledEmbedder struct{}

func (einoDisabledEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, usecase.ErrEmbeddingUnavailable
}

type httpKnowledgeStore struct {
	chunks []string
	items  []usecase.KnowledgeSnippet
}

func (s *httpKnowledgeStore) CreateDocument(_ context.Context, _ usecase.KnowledgeDocument, chunks []string, _ [][]float32) (int64, error) {
	s.chunks = chunks
	return 11, nil
}
func (s *httpKnowledgeStore) SearchKeyword(context.Context, string, string, string, int) ([]usecase.KnowledgeSnippet, error) {
	return s.items, nil
}
func (*httpKnowledgeStore) SearchVector(context.Context, []float32, string, string, int) ([]usecase.KnowledgeSnippet, error) {
	return nil, nil
}

type httpSearchClient struct{}

func (httpSearchClient) Search(_ context.Context, query string) (usecase.WebSearchResult, error) {
	return usecase.WebSearchResult{Enabled: true, Provider: "searxng", Query: query, Items: []usecase.WebSearchItem{{Title: "A", URL: "https://a"}}, Note: "ok"}, nil
}

type captureWriter struct {
	bytes.Buffer
	flushes  int
	writeErr error
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.Buffer.Write(p)
}

func (w *captureWriter) Flush() error {
	w.flushes++
	return nil
}
func (*captureWriter) Finalize() error { return nil }

func captureDefaultLogger() (*bytes.Buffer, func()) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	return &output, func() { slog.SetDefault(previous) }
}
