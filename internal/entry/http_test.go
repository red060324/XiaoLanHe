package entry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertzmock "github.com/cloudwego/hertz/pkg/common/test/mock"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/network/standard"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestHTTPMessage(t *testing.T) {
	store := &httpStore{}
	h := NewHTTPWithServices(":0", usecase.NewChat(store, httpAssistant{}), nil, nil, httpAuthenticator{})

	t.Run("keeps the REST response contract", func(t *testing.T) {
		response := ut.PerformRequest(
			h.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"f47ac10b-58cc-4372-a567-0e02b2c3d479","message":"hi"}`), Len: -1},
			ut.Header{Key: "Content-Type", Value: "application/json"},
		)
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["sessionId"] != "f47ac10b-58cc-4372-a567-0e02b2c3d479" || body["answer"] != "hello" || body["createdAt"] == "" {
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

	t.Run("rejects a predictable session id before storage", func(t *testing.T) {
		before := len(store.userIDs)
		response := ut.PerformRequest(
			h.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"shared","message":"hi"}`), Len: -1},
		)
		if response.Code != 400 || len(store.userIDs) != before {
			t.Fatalf("status=%d body=%s user_ids=%v", response.Code, response.Body.String(), store.userIDs)
		}
	})

	t.Run("logs the validated request ID", func(t *testing.T) {
		logs, restore := captureDefaultLogger()
		defer restore()
		response := ut.PerformRequest(
			h.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"f47ac10b-58cc-4372-a567-0e02b2c3d479","message":"hi"}`), Len: -1},
			ut.Header{Key: "X-Request-ID", Value: "unsafe id"},
		)
		requestID := string(response.Header().Peek("X-Request-ID"))
		if requestID == "" || requestID == "unsafe id" || !strings.Contains(logs.String(), "request_id="+requestID) {
			t.Fatalf("request ID=%q logs=%q", requestID, logs.String())
		}
	})

	t.Run("passes authenticated identity to the conversation boundary", func(t *testing.T) {
		response := ut.PerformRequest(
			h.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"11111111-1111-4111-8111-111111111111","message":"hi"}`), Len: -1},
			ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"},
		)
		if response.Code != 200 || len(store.userIDs) == 0 || store.userIDs[len(store.userIDs)-1] != 1 {
			t.Fatalf("status=%d body=%s user_ids=%v", response.Code, response.Body.String(), store.userIDs)
		}
	})

	t.Run("returns forbidden when the conversation owner does not match", func(t *testing.T) {
		blocked := NewHTTPWithServices(":0", usecase.NewChat(&httpStore{findErr: usecase.ErrConversationForbidden}, httpAssistant{}), nil, nil, httpAuthenticator{})
		response := ut.PerformRequest(
			blocked.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"22222222-2222-4222-8222-222222222222","message":"hi"}`), Len: -1},
			ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"},
		)
		if response.Code != 403 || !strings.Contains(response.Body.String(), `"code":"conversation_forbidden"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("maps a request deadline to the public timeout contract", func(t *testing.T) {
		timedOut := NewHTTPWithServices(":0", usecase.NewChat(&httpStore{findErr: context.DeadlineExceeded}, httpAssistant{}), nil, nil, httpAuthenticator{})
		response := ut.PerformRequest(
			timedOut.server.Engine,
			"POST",
			"/api/chat/message",
			&ut.Body{Body: bytes.NewBufferString(`{"sessionId":"33333333-3333-4333-8333-333333333333","message":"hi"}`), Len: -1},
		)
		if response.Code != 504 || !strings.Contains(response.Body.String(), `"code":"deadline_exceeded"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestHTTPMessageRedactsAssistantError(t *testing.T) {
	const canary = "CANARY_PRIVATE_MODEL_RESPONSE"
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{generateErr: errors.New(canary)}))
	response := ut.PerformRequest(h.server.Engine, "POST", "/api/chat/message", &ut.Body{Body: bytes.NewBufferString(`{"message":"hi"}`), Len: -1})
	if response.Code != 500 || strings.Contains(output.String(), canary) || !strings.Contains(output.String(), `"error_class":"dependency"`) {
		t.Fatalf("status=%d logs=%s body=%s", response.Code, output.String(), response.Body.String())
	}
}

func TestHTTPOversizedRequestContract(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()

	h := newHTTPWithServices(
		newHertzServer(address, server.WithListener(listener), server.WithTransport(standard.NewTransporter)),
		usecase.NewChat(&httpStore{}, httpAssistant{}), nil, nil, nil,
	)
	h.server.POST("/test/request-body", func(_ context.Context, c *app.RequestContext) {
		c.JSON(http.StatusOK, map[string]int{"bytes": len(c.Request.Body())})
	})
	runErr := make(chan error, 1)
	go func() { runErr <- h.server.Run() }()
	t.Cleanup(func() {
		_ = h.server.Close()
		select {
		case <-runErr:
		case <-time.After(time.Second):
			t.Error("HTTP server did not stop")
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for !h.server.IsRunning() {
		select {
		case err := <-runErr:
			t.Fatalf("HTTP server exited during startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP server did not start")
		}
		time.Sleep(time.Millisecond)
	}

	dial := func(t *testing.T) (net.Conn, *bufio.Reader) {
		t.Helper()
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		return connection, bufio.NewReader(connection)
	}
	readResponse := func(t *testing.T, reader *bufio.Reader) (*http.Response, []byte) {
		t.Helper()
		response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response, body
	}
	assertError := func(t *testing.T, response *http.Response, body []byte, status int, code, message, expectedRequestID string) {
		t.Helper()
		if response.StatusCode != status {
			t.Fatalf("status=%d body=%s", response.StatusCode, body)
		}
		if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
			t.Fatalf("content-type=%q body=%s", contentType, body)
		}
		requestID := response.Header.Get("X-Request-ID")
		if !regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`).MatchString(requestID) {
			t.Fatalf("request ID=%q body=%s", requestID, body)
		}
		if expectedRequestID != "" && requestID != expectedRequestID {
			t.Fatalf("request ID=%q want=%q body=%s", requestID, expectedRequestID, body)
		}
		var payload httpx.ErrorBody
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode response %q: %v", body, err)
		}
		if payload.Error.Code != code || payload.Error.Message != message || payload.Error.RequestID != requestID {
			t.Fatalf("error=%+v request ID=%q", payload.Error, requestID)
		}
	}
	assertServerClosed := func(t *testing.T, response *http.Response, reader *bufio.Reader) {
		t.Helper()
		if !response.Close {
			t.Fatalf("response did not announce a server-initiated connection close: headers=%v", response.Header)
		}
		if _, err := reader.ReadByte(); err == nil {
			t.Fatal("connection remained readable after close response")
		} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			t.Fatalf("connection close was not observed before the read deadline: %v", err)
		}
	}
	writeChunk := func(connection net.Conn, body []byte) error {
		if _, err := fmt.Fprintf(connection, "%x\r\n", len(body)); err != nil {
			return err
		}
		if _, err := io.Copy(connection, bytes.NewReader(body)); err != nil {
			return err
		}
		_, err := io.WriteString(connection, "\r\n")
		return err
	}

	exactBody := bytes.Repeat([]byte("x"), maxKnowledgeBody)

	t.Run("accepts a fixed body at the limit", func(t *testing.T) {
		connection, reader := dial(t)
		if _, err := fmt.Fprintf(connection, "POST /test/request-body HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nX-Request-ID: fixed-limit\r\n\r\n", address, len(exactBody)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(connection, bytes.NewReader(exactBody)); err != nil {
			t.Fatal(err)
		}
		response, body := readResponse(t, reader)
		if response.StatusCode != http.StatusOK || response.Close || string(body) != fmt.Sprintf(`{"bytes":%d}`, maxKnowledgeBody) {
			t.Fatalf("status=%d close=%v body=%s", response.StatusCode, response.Close, body)
		}
	})

	t.Run("rejects a fixed body above the limit and closes", func(t *testing.T) {
		connection, reader := dial(t)
		if _, err := fmt.Fprintf(connection, "POST /test/request-body HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nX-Request-ID: fixed-oversized\r\n\r\n", address, maxKnowledgeBody+1); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(connection, bytes.NewReader(bytes.Repeat([]byte("x"), requestBodyPrefetchBytes+1))); err != nil {
			t.Fatal(err)
		}
		response, body := readResponse(t, reader)
		assertError(t, response, body, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", "fixed-oversized")
		assertServerClosed(t, response, reader)
	})

	t.Run("accepts a chunked body at the limit", func(t *testing.T) {
		connection, reader := dial(t)
		if _, err := fmt.Fprintf(connection, "POST /test/request-body HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nX-Request-ID: chunked-limit\r\n\r\n", address); err != nil {
			t.Fatal(err)
		}
		if err := writeChunk(connection, exactBody); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(connection, "0\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		response, body := readResponse(t, reader)
		if response.StatusCode != http.StatusOK || response.Close || string(body) != fmt.Sprintf(`{"bytes":%d}`, maxKnowledgeBody) {
			t.Fatalf("status=%d close=%v body=%s", response.StatusCode, response.Close, body)
		}
	})

	t.Run("rejects a chunked body above the limit and closes", func(t *testing.T) {
		connection, reader := dial(t)
		if _, err := fmt.Fprintf(connection, "POST /test/request-body HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nX-Request-ID: chunked-oversized\r\n\r\n", address); err != nil {
			t.Fatal(err)
		}
		if err := writeChunk(connection, exactBody); err != nil {
			t.Fatal(err)
		}
		if err := writeChunk(connection, []byte("x")); err != nil {
			t.Fatal(err)
		}
		response, body := readResponse(t, reader)
		assertError(t, response, body, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", "chunked-oversized")
		assertServerClosed(t, response, reader)
	})

	t.Run("rejects oversized expect continue before acknowledging or reading a body", func(t *testing.T) {
		connection, reader := dial(t)
		if _, err := fmt.Fprintf(connection, "POST /test/request-body HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nExpect: 100-continue\r\nX-Request-ID: expect-oversized\r\n\r\n", address, maxKnowledgeBody+1); err != nil {
			t.Fatal(err)
		}
		response, body := readResponse(t, reader)
		assertError(t, response, body, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", "expect-oversized")
		assertServerClosed(t, response, reader)
	})

	for _, scenario := range []struct {
		name string
		path string
		id   string
	}{
		{name: "REST chat", path: "/api/chat/message", id: "chat-message-oversized"},
		{name: "streaming chat", path: "/api/chat/stream", id: "chat-stream-oversized"},
	} {
		t.Run("rejects oversized "+scenario.name+" before acknowledging expect continue", func(t *testing.T) {
			connection, reader := dial(t)
			if _, err := fmt.Fprintf(connection, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nExpect: 100-continue\r\nX-Request-ID: %s\r\n\r\n", scenario.path, address, maxRequestBytes+1, scenario.id); err != nil {
				t.Fatal(err)
			}
			response, body := readResponse(t, reader)
			assertError(t, response, body, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", scenario.id)
			assertServerClosed(t, response, reader)
		})
	}

	t.Run("rejects an active chunked chat body at the route limit", func(t *testing.T) {
		connection, reader := dial(t)
		if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(connection, "POST /api/chat/message HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nX-Request-ID: chat-chunked-oversized\r\n\r\n", address); err != nil {
			t.Fatal(err)
		}
		if err := writeChunk(connection, bytes.Repeat([]byte("x"), maxRequestBytes+1)); err != nil {
			t.Fatal(err)
		}
		response, body := readResponse(t, reader)
		assertError(t, response, body, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large", "chat-chunked-oversized")
		assertServerClosed(t, response, reader)
	})

	t.Run("returns a standard error for an incomplete chunked stream", func(t *testing.T) {
		connection, reader := dial(t)
		if _, err := fmt.Fprintf(connection, "POST /test/request-body HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nX-Request-ID: incomplete-stream\r\n\r\n5\r\nabc", address); err != nil {
			t.Fatal(err)
		}
		tcpConnection, ok := connection.(*net.TCPConn)
		if !ok {
			t.Fatalf("connection type=%T", connection)
		}
		if err := tcpConnection.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		response, body := readResponse(t, reader)
		assertError(t, response, body, http.StatusBadRequest, "invalid_request", "invalid request body", "incomplete-stream")
		assertServerClosed(t, response, reader)
	})
}

func TestHTTPOversizedRequestWithDefaultNetpollStopsReading(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	h := newHTTPWithServices(
		newHertzServer(address, server.WithListener(listener)),
		usecase.NewChat(&httpStore{}, httpAssistant{}), nil, nil, nil,
	)
	h.server.POST("/test/request-body", func(_ context.Context, c *app.RequestContext) {
		c.JSON(http.StatusOK, map[string]int{"bytes": len(c.Request.Body())})
	})

	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	const initialBodyBytes = 64 << 10
	declaredBodyBytes := maxKnowledgeBody + (32 << 20)
	header := fmt.Sprintf(
		"POST /test/request-body HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nX-Request-ID: netpoll-oversized\r\n\r\n",
		address, declaredBodyBytes,
	)
	initialRequest := append([]byte(header), bytes.Repeat([]byte("x"), initialBodyBytes)...)
	if _, err := connection.Write(initialRequest); err != nil {
		t.Fatal(err)
	}

	type writeResult struct {
		written int
		err     error
	}
	writeDone := make(chan writeResult, 1)
	writerStarted := make(chan struct{})
	remainingBody := bytes.Repeat([]byte("x"), declaredBodyBytes-initialBodyBytes)
	go func() {
		close(writerStarted)
		n, err := connection.Write(remainingBody)
		writeDone <- writeResult{written: initialBodyBytes + n, err: err}
	}()
	<-writerStarted
	select {
	case result := <-writeDone:
		t.Fatalf("test setup failed to keep the oversized sender active: wrote=%d err=%v", result.written, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	runErr := make(chan error, 1)
	go func() { runErr <- h.server.Run() }()
	t.Cleanup(func() {
		_ = h.server.Close()
		select {
		case <-runErr:
		case <-time.After(time.Second):
			t.Error("HTTP server did not stop")
		}
	})

	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestEntityTooLarge || !response.Close {
		t.Fatalf("status=%d close=%v body=%s", response.StatusCode, response.Close, body)
	}
	var payload httpx.ErrorBody
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	requestID := response.Header.Get("X-Request-ID")
	if requestID != "netpoll-oversized" || payload.Error.Code != "request_too_large" || payload.Error.Message != "request body is too large" || payload.Error.RequestID != requestID {
		t.Fatalf("request ID=%q error=%+v", requestID, payload.Error)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatalf("connection remained readable after close response: %v", err)
	}

	result := <-writeDone
	t.Logf("oversized sender stopped after %d of %d bytes: %v", result.written, declaredBodyBytes, result.err)
	if result.written == declaredBodyBytes {
		t.Fatalf("server drained the full oversized body before responding: wrote=%d err=%v", result.written, result.err)
	}
}

func TestReleaseRejectedRequestBodyUsesAbsoluteDeadline(t *testing.T) {
	connection := &deadlineRecordingConn{Conn: hertzmock.NewConn("")}
	c := app.NewContext(0)
	c.SetConn(connection)
	stream := bytes.NewReader(nil)
	c.Request.SetBodyStream(stream, maxKnowledgeBody+1)
	started := time.Now()

	releaseRejectedRequestBody(c, stream)

	if !c.Response.Header.ConnectionClose() {
		t.Fatal("rejected request did not mark the connection for close")
	}
	if connection.readDeadline.IsZero() || connection.readDeadline.Before(started) || connection.readDeadline.After(time.Now()) {
		t.Fatalf("read deadline=%v started=%v", connection.readDeadline, started)
	}
	if connection.readTimeoutCalls != 0 {
		t.Fatalf("relative read timeout was used %d times", connection.readTimeoutCalls)
	}
}

func TestHTTPStream(t *testing.T) {
	const suppliedSessionID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	upstream := &httpStream{chunks: []string{"first", "second"}}
	store := &httpStore{}
	h := NewHTTP(":0", usecase.NewChat(store, httpAssistant{stream: upstream}))
	c := app.NewContext(0)
	c.Request.SetBodyString(`{"sessionId":"` + suppliedSessionID + `","message":"hi"}`)
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
	if got := string(c.Response.Header.Peek("X-Conversation-ID")); got != suppliedSessionID {
		t.Errorf("conversation header=%q want=%q", got, suppliedSessionID)
	}

	t.Run("returns a generated conversation identifier", func(t *testing.T) {
		store := &httpStore{}
		h := NewHTTP(":0", usecase.NewChat(store, httpAssistant{}))
		c := app.NewContext(0)
		c.Request.SetBodyString(`{"message":"hi"}`)
		c.Response.HijackWriter(&captureWriter{})

		h.stream(context.Background(), c)

		got := string(c.Response.Header.Peek("X-Conversation-ID"))
		uuidV4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
		if !uuidV4.MatchString(got) || len(store.sessionKeys) != 1 || store.sessionKeys[0] != got {
			t.Fatalf("conversation header=%q stored keys=%v", got, store.sessionKeys)
		}
	})

	for _, scenario := range []struct {
		name   string
		chunks []string
	}{
		{name: "before the first message"},
		{name: "after a partial message", chunks: []string{"partial"}},
	} {
		t.Run("reports a stream failure "+scenario.name, func(t *testing.T) {
			store := &httpStore{}
			upstream := &httpStream{chunks: scenario.chunks, finalErr: errors.New("provider secret detail")}
			h := NewHTTP(":0", usecase.NewChat(store, httpAssistant{stream: upstream}))
			c := app.NewContext(0)
			c.Request.SetBodyString(`{"sessionId":"f47ac10b-58cc-4372-a567-0e02b2c3d479","message":"hi"}`)
			w := &captureWriter{}
			c.Response.HijackWriter(w)

			h.stream(context.Background(), c)

			body := w.String()
			const prefix = "event: error\ndata: "
			start := strings.Index(body, prefix)
			if start < 0 || strings.Count(body, prefix) != 1 {
				t.Fatalf("missing one SSE error event: %q", body)
			}
			data := body[start+len(prefix):]
			if end := strings.Index(data, "\n\n"); end >= 0 {
				data = data[:end]
			}
			var payload httpx.ErrorBody
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatalf("decode SSE error %q: %v", data, err)
			}
			if payload.Error.Code != "stream_failed" || payload.Error.Message != "assistant stream failed" {
				t.Fatalf("SSE error=%+v", payload.Error)
			}
			if strings.Contains(body, "provider secret detail") {
				t.Fatalf("internal error leaked: %q", body)
			}
			if !upstream.closed || len(store.roles) != 1 || store.roles[0] != "user" {
				t.Fatalf("closed=%v saved roles=%v", upstream.closed, store.roles)
			}
		})
	}

	t.Run("returns forbidden before starting an unauthorized stream", func(t *testing.T) {
		h := NewHTTP(":0", usecase.NewChat(&httpStore{findErr: usecase.ErrConversationForbidden}, httpAssistant{}))
		c := app.NewContext(0)
		c.Request.SetBodyString(`{"sessionId":"22222222-2222-4222-8222-222222222222","message":"hi"}`)

		h.stream(context.Background(), c)

		if c.Response.StatusCode() != 403 || !strings.Contains(string(c.Response.Body()), `"code":"conversation_forbidden"`) {
			t.Fatalf("status=%d body=%s", c.Response.StatusCode(), c.Response.Body())
		}
	})

	t.Run("disconnect closes upstream without saving a partial assistant message", func(t *testing.T) {
		store := &httpStore{}
		upstream := &httpStream{chunks: []string{"first", "second"}}
		h := NewHTTP(":0", usecase.NewChat(store, httpAssistant{stream: upstream}))
		c := app.NewContext(0)
		c.Request.SetBodyString(`{"sessionId":"f47ac10b-58cc-4372-a567-0e02b2c3d479","message":"hi"}`)
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
		c.Request.SetBodyString(`{"sessionId":"f47ac10b-58cc-4372-a567-0e02b2c3d479","message":"hi"}`)
		c.Response.HijackWriter(&captureWriter{})
		h.stream(context.Background(), c)
		requestID := httpx.RequestID(c)
		if requestID == "" || requestID == "unsafe id" || !strings.Contains(logs.String(), "request_id="+requestID) {
			t.Fatalf("request ID=%q logs=%q", requestID, logs.String())
		}
	})
}

func TestHTTPStreamCancelsOnClientDisconnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	stream := newDisconnectAwareHTTPStream()
	store := &httpStore{}
	h := newHTTPWithServices(
		newHertzServer(address, server.WithListener(listener)),
		usecase.NewChat(store, disconnectAwareHTTPAssistant{stream: stream}), nil, nil, nil,
	)
	runDone := make(chan struct{})
	var runErr error
	go func() {
		runErr = h.server.Run()
		close(runDone)
	}()

	var connection net.Conn
	t.Cleanup(func() {
		streamWasWaiting := false
		select {
		case <-stream.waiting:
			streamWasWaiting = true
		default:
		}
		stream.release()
		if connection != nil {
			_ = connection.Close()
		}
		_ = h.server.Close()
		if streamWasWaiting {
			select {
			case <-stream.closed:
			case <-time.After(2 * time.Second):
				t.Error("upstream stream did not close during cleanup")
			}
		}
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("HTTP server did not stop")
		}
	})

	startupDeadline := time.Now().Add(2 * time.Second)
	for !h.server.IsRunning() {
		select {
		case <-runDone:
			t.Fatalf("HTTP server exited during startup: %v", runErr)
		default:
		}
		if time.Now().After(startupDeadline) {
			t.Fatal("HTTP server did not start")
		}
		time.Sleep(time.Millisecond)
	}

	connection, err = net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	const requestBody = "{\"sessionId\":\"f47ac10b-58cc-4372-a567-0e02b2c3d479\",\"message\":\"hi\"}"
	if _, err := fmt.Fprintf(connection,
		"POST /api/chat/stream HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		address, len(requestBody), requestBody,
	); err != nil {
		t.Fatal(err)
	}

	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type=%q", contentType)
	}
	if conversationID := response.Header.Get("X-Conversation-ID"); conversationID != "f47ac10b-58cc-4372-a567-0e02b2c3d479" {
		t.Fatalf("conversation ID=%q", conversationID)
	}
	const firstEvent = "event: message\ndata: first\n\n"
	firstChunk := make([]byte, len(firstEvent))
	if _, err := io.ReadFull(response.Body, firstChunk); err != nil {
		t.Fatalf("read first SSE event: %v", err)
	}
	if string(firstChunk) != firstEvent {
		t.Fatalf("first SSE event=%q", firstChunk)
	}
	select {
	case <-stream.waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not block waiting for the next chunk")
	}

	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case err := <-stream.cancelObserved:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request context error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request context was not canceled after the client disconnected")
	}
	select {
	case <-stream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream stream was not closed after the client disconnected")
	}
	if len(store.roles) != 1 || store.roles[0] != "user" {
		t.Fatalf("saved roles=%v", store.roles)
	}
}

func TestHTTPHealth(t *testing.T) {
	h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
	response := ut.PerformRequest(h.server.Engine, "GET", "/healthz", nil)
	if response.Code != 200 || response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPMetricsRequiresOperatorToken(t *testing.T) {
	h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
	registry := platformmetrics.NewRegistry()
	registry.ObserveLightRAGRequest("query", "ok", time.Millisecond)
	const token = "metrics-secret-at-least-thirty-two-chars"
	h.RegisterMetrics(token, registry)

	for _, authorization := range []string{"", "Bearer wrong"} {
		response := ut.PerformRequest(h.server.Engine, "GET", "/metrics", nil, ut.Header{Key: "Authorization", Value: authorization})
		if response.Code != 401 || strings.Contains(response.Body.String(), token) {
			t.Fatalf("authorization=%q status=%d body=%s", authorization, response.Code, response.Body.String())
		}
	}
	response := ut.PerformRequest(h.server.Engine, "GET", "/metrics", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if response.Code != 200 || !strings.HasPrefix(string(response.Header().ContentType()), "text/plain") || !strings.Contains(response.Body.String(), `xiaolanhe_lightrag_requests_total{operation="query",outcome="ok"} 1`) {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().ContentType(), response.Body.String())
	}
}

func TestHTTPMetricsRouteIsAbsentWithoutToken(t *testing.T) {
	h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
	h.RegisterMetrics("", platformmetrics.NewRegistry())
	response := ut.PerformRequest(h.server.Engine, "GET", "/metrics", nil)
	if response.Code != 404 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPReadinessChecks(t *testing.T) {
	t.Run("all dependencies ready", func(t *testing.T) {
		h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
		called := 0
		h.RegisterReadinessChecks(func(context.Context) error { called++; return nil }, func(context.Context) error { called++; return nil })
		response := ut.PerformRequest(h.server.Engine, "GET", "/readyz", nil)
		if response.Code != 200 || called != 2 || response.Body.String() != `{"status":"ready"}` {
			t.Fatalf("status=%d called=%d body=%s", response.Code, called, response.Body.String())
		}
	})
	t.Run("fails closed and stops checks", func(t *testing.T) {
		h := NewHTTP(":0", usecase.NewChat(&httpStore{}, httpAssistant{}))
		called := 0
		h.RegisterReadinessChecks(func(context.Context) error { called++; return errors.New("private dependency detail") }, func(context.Context) error { called++; return nil })
		response := ut.PerformRequest(h.server.Engine, "GET", "/readyz", nil)
		if response.Code != 503 || called != 1 || strings.Contains(response.Body.String(), "private dependency detail") {
			t.Fatalf("status=%d called=%d body=%s", response.Code, called, response.Body.String())
		}
	})
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
	h := NewHTTPWithServices(":0", usecase.NewChat(&httpStore{}, httpAssistant{}), knowledge, usecase.NewWebSearch(httpSearchClient{}), httpAuthenticator{}, httpauth.RequireRole(httpAuthenticator{}, auth.RoleAdmin))

	t.Run("use case rejects an unprotected anonymous write", func(t *testing.T) {
		unprotectedStore := &httpKnowledgeStore{}
		unprotected := NewHTTPWithServices(":0", usecase.NewChat(&httpStore{}, httpAssistant{}), usecase.NewKnowledge(unprotectedStore, einoDisabledEmbedder{}), nil, nil)
		response := ut.PerformRequest(unprotected.server.Engine, "POST", "/api/knowledge/documents", &ut.Body{Body: bytes.NewBufferString(`{"sourceType":"note","title":"Guide","contentText":"body"}`), Len: -1})
		if response.Code != 401 || !strings.Contains(response.Body.String(), `"code":"unauthenticated"`) || unprotectedStore.createCalls != 0 {
			t.Fatalf("status=%d body=%s create calls=%d", response.Code, response.Body.String(), unprotectedStore.createCalls)
		}
	})

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
	searchCalls := store.searchCalls
	for _, path := range []string{
		"/api/knowledge/search?query=%20%20",
		"/api/knowledge/search?query=" + strings.Repeat("a", 101),
		"/api/knowledge/search?query=guide&limit=bad",
		"/api/knowledge/search?query=guide&limit=0",
		"/api/knowledge/search?query=guide&limit=11",
	} {
		response := ut.PerformRequest(h.server.Engine, "GET", path, nil)
		if response.Code != 400 {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if store.searchCalls != searchCalls {
		t.Fatalf("invalid requests reached knowledge store: before=%d after=%d", searchCalls, store.searchCalls)
	}
}

type httpAuthenticator struct{}

type deadlineRecordingConn struct {
	*hertzmock.Conn
	readDeadline     time.Time
	readTimeoutCalls int
}

func (c *deadlineRecordingConn) SetReadDeadline(deadline time.Time) error {
	c.readDeadline = deadline
	return nil
}

func (c *deadlineRecordingConn) SetReadTimeout(timeout time.Duration) error {
	c.readTimeoutCalls++
	return c.Conn.SetReadTimeout(timeout)
}

func (httpAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if token != "admin" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return auth.Principal{UserID: 1, Role: auth.RoleAdmin}, nil
}

func TestHTTPWebSearchAndPing(t *testing.T) {
	h := NewHTTPWithServices(":0", usecase.NewChat(&httpStore{}, httpAssistant{}), nil, usecase.NewWebSearch(httpSearchClient{}), nil)
	searched := ut.PerformRequest(h.server.Engine, "GET", "/api/search/web?query=guide", nil)
	if searched.Code != 200 || !strings.Contains(searched.Body.String(), `"provider":"searxng"`) || !strings.Contains(searched.Body.String(), `"title":"A"`) || strings.Contains(searched.Body.String(), `"cacheHit"`) {
		t.Fatalf("status=%d body=%s", searched.Code, searched.Body.String())
	}
	for _, path := range []string{"/api/search/web?query=%20%20", "/api/search/web?query=" + strings.Repeat("a", 101)} {
		response := ut.PerformRequest(h.server.Engine, "GET", path, nil)
		if response.Code != 400 {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	ping := ut.PerformRequest(h.server.Engine, "GET", "/api/system/ping", nil)
	if ping.Code != 200 || ping.Body.String() != `{"name":"xiaolanhe","status":"ok"}` {
		t.Fatalf("status=%d body=%s", ping.Code, ping.Body.String())
	}
}

type httpStore struct {
	id          int64
	roles       []string
	userIDs     []int64
	sessionKeys []string
	findErr     error
}

func (s *httpStore) FindOrCreateSession(_ context.Context, sessionKey string, userID int64) (int64, error) {
	s.sessionKeys = append(s.sessionKeys, sessionKey)
	s.userIDs = append(s.userIDs, userID)
	if s.findErr != nil {
		return 0, s.findErr
	}
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

type httpAssistant struct {
	stream      *httpStream
	generateErr error
	streamErr   error
}

func (a httpAssistant) Generate(context.Context, usecase.AssistantInput) (usecase.Answer, error) {
	return usecase.Answer{Text: "hello", Model: "fake"}, a.generateErr
}
func (a httpAssistant) Stream(context.Context, usecase.AssistantInput) (usecase.AnswerStream, error) {
	if a.streamErr != nil {
		return nil, a.streamErr
	}
	if a.stream == nil {
		a.stream = &httpStream{chunks: []string{"first", "second"}}
	}
	return a.stream, nil
}

type httpStream struct {
	chunks   []string
	index    int
	closed   bool
	finalErr error
}

func (s *httpStream) Recv() (string, error) {
	if s.index == len(s.chunks) {
		if s.finalErr != nil {
			return "", s.finalErr
		}
		return "", io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}
func (s *httpStream) Close()      { s.closed = true }
func (*httpStream) Model() string { return "fake" }

type disconnectAwareHTTPAssistant struct {
	stream *disconnectAwareHTTPStream
}

func (disconnectAwareHTTPAssistant) Generate(context.Context, usecase.AssistantInput) (usecase.Answer, error) {
	return usecase.Answer{Text: "hello", Model: "fake"}, nil
}

func (a disconnectAwareHTTPAssistant) Stream(ctx context.Context, _ usecase.AssistantInput) (usecase.AnswerStream, error) {
	a.stream.ctx = ctx
	return a.stream, nil
}

type disconnectAwareHTTPStream struct {
	ctx            context.Context
	waiting        chan struct{}
	cancelObserved chan error
	closed         chan struct{}
	released       chan struct{}
	waitingOnce    sync.Once
	closeOnce      sync.Once
	releaseOnce    sync.Once
	sentFirst      bool
}

func newDisconnectAwareHTTPStream() *disconnectAwareHTTPStream {
	return &disconnectAwareHTTPStream{
		waiting:        make(chan struct{}),
		cancelObserved: make(chan error, 1),
		closed:         make(chan struct{}),
		released:       make(chan struct{}),
	}
}

func (s *disconnectAwareHTTPStream) Recv() (string, error) {
	if !s.sentFirst {
		s.sentFirst = true
		return "first", nil
	}
	s.waitingOnce.Do(func() { close(s.waiting) })
	select {
	case <-s.ctx.Done():
		s.cancelObserved <- s.ctx.Err()
		return "", s.ctx.Err()
	case <-s.released:
		return "", errors.New("disconnect test stream released during cleanup")
	}
}

func (s *disconnectAwareHTTPStream) Close() {
	s.closeOnce.Do(func() { close(s.closed) })
}

func (*disconnectAwareHTTPStream) Model() string { return "fake" }

func (s *disconnectAwareHTTPStream) release() {
	s.releaseOnce.Do(func() { close(s.released) })
}

type einoDisabledEmbedder struct{}

func (einoDisabledEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, usecase.ErrEmbeddingUnavailable
}

type httpKnowledgeStore struct {
	chunks      []string
	items       []usecase.KnowledgeSnippet
	createCalls int
	searchCalls int
}

func (s *httpKnowledgeStore) CreateDocument(_ context.Context, _ usecase.KnowledgeDocument, chunks []string, _ [][]float32) (int64, error) {
	s.createCalls++
	s.chunks = chunks
	return 11, nil
}
func (s *httpKnowledgeStore) SearchKeyword(context.Context, string, string, string, int) ([]usecase.KnowledgeSnippet, error) {
	s.searchCalls++
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
