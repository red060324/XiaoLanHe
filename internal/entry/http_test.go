package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/ut"

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
}

func TestHTTPHealth(t *testing.T) {
	h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
	response := ut.PerformRequest(h.server.Engine, "GET", "/healthz", nil)
	if response.Code != 200 || response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
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

type httpAssistant struct{ stream *httpStream }

func (httpAssistant) Generate(context.Context, string) (usecase.Answer, error) {
	return usecase.Answer{Text: "hello", Model: "fake"}, nil
}
func (a httpAssistant) Stream(context.Context, string) (usecase.AnswerStream, error) {
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
