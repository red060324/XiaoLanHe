package usecase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"testing"
	"time"
)

func TestChatRun(t *testing.T) {
	t.Run("loads history before persisting the current user message", func(t *testing.T) {
		var calls []string
		store := &fakeStore{calls: &calls, sessionID: 7}
		assistant := &fakeAssistant{calls: &calls, answer: Answer{Text: "hello", Model: "model"}}
		chat := NewChat(store, assistant)
		now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		chat.now = func() time.Time { return now }

		result, err := chat.Run(context.Background(), ChatInput{SessionID: "session", Message: "hi", UserID: 42})
		if err != nil {
			t.Fatal(err)
		}
		if result != (ChatResult{SessionID: "session", Answer: "hello", CreatedAt: now}) {
			t.Fatalf("unexpected result: %#v", result)
		}
		want := []string{"find:session:42", "context:8", "save:user:hi:", "generate:hi:", "save:assistant:hello:model"}
		if !slices.Equal(calls, want) {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	})

	t.Run("creates a session id when absent", func(t *testing.T) {
		store := &fakeStore{sessionID: 9}
		chat := NewChat(store, &fakeAssistant{answer: Answer{Text: "ok"}})
		chat.newID = func() (string, error) { return "new-session", nil }

		result, err := chat.Run(context.Background(), ChatInput{Message: "hi"})
		if err != nil || result.SessionID != "new-session" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("treats a whitespace session id as absent", func(t *testing.T) {
		store := &fakeStore{sessionID: 9}
		chat := NewChat(store, &fakeAssistant{answer: Answer{Text: "ok"}})
		chat.newID = func() (string, error) { return "new-session", nil }

		result, err := chat.Run(context.Background(), ChatInput{SessionID: "  \t", Message: "hi"})
		if err != nil || result.SessionID != "new-session" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("does not call the model when saving user fails", func(t *testing.T) {
		storeErr := errors.New("database unavailable")
		store := &fakeStore{sessionID: 1, saveUserErr: storeErr}
		assistant := &fakeAssistant{}

		_, err := NewChat(store, assistant).Run(context.Background(), ChatInput{SessionID: "s", Message: "hi"})
		if !errors.Is(err, storeErr) {
			t.Fatalf("err = %v", err)
		}
		if assistant.generateCalls != 0 {
			t.Fatalf("generate calls = %d", assistant.generateCalls)
		}
	})

	t.Run("stops before loading context when conversation access is forbidden", func(t *testing.T) {
		store := &fakeStore{findErr: ErrConversationForbidden}
		assistant := &fakeAssistant{}

		_, err := NewChat(store, assistant).Run(context.Background(), ChatInput{SessionID: "session", Message: "hi", UserID: 42})
		if !errors.Is(err, ErrConversationForbidden) {
			t.Fatalf("err = %v", err)
		}
		if assistant.generateCalls != 0 || len(store.saved) != 0 {
			t.Fatalf("generate calls=%d saved=%#v", assistant.generateCalls, store.saved)
		}
	})

	t.Run("keeps user message but not assistant message on model failure", func(t *testing.T) {
		modelErr := errors.New("model failed")
		store := &fakeStore{sessionID: 1}
		_, err := NewChat(store, &fakeAssistant{generateErr: modelErr}).Run(
			context.Background(), ChatInput{SessionID: "s", Message: "hi"},
		)
		if !errors.Is(err, modelErr) {
			t.Fatalf("err = %v", err)
		}
		if len(store.saved) != 1 || store.saved[0].role != "user" {
			t.Fatalf("saved messages = %#v", store.saved)
		}
	})

	t.Run("refreshes memory only after the complete assistant message is stored", func(t *testing.T) {
		var calls []string
		store := &fakeStore{calls: &calls, sessionID: 17}
		memory := &fakeMemory{calls: &calls}
		_, err := NewChat(store, &fakeAssistant{calls: &calls, answer: Answer{Text: "ok", Model: "m"}}).
			WithMemory(memory).Run(context.Background(), ChatInput{SessionID: "s", Message: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		if memory.sessionID != 17 || calls[len(calls)-1] != "memory:17" || calls[len(calls)-2] != "save:assistant:ok:m" {
			t.Fatalf("session=%d calls=%v", memory.sessionID, calls)
		}
	})

	t.Run("memory failure never changes a successful answer", func(t *testing.T) {
		memoryErr := errors.New("summary unavailable")
		result, err := NewChat(&fakeStore{sessionID: 2}, &fakeAssistant{answer: Answer{Text: "ok"}}).
			WithMemory(&fakeMemory{err: memoryErr}).Run(context.Background(), ChatInput{SessionID: "s", Message: "hi"})
		if err != nil || result.Answer != "ok" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestChatStream(t *testing.T) {
	t.Run("prepares conversation and returns stream", func(t *testing.T) {
		upstream := &fakeStream{chunks: []string{"a"}}
		store := &fakeStore{sessionID: 2}
		result, err := NewChat(store, &fakeAssistant{stream: upstream}).Stream(
			context.Background(), ChatInput{SessionID: "s", Message: "hi"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.SessionID != "s" || len(store.saved) != 1 || store.saved[0].role != "user" {
			t.Fatalf("result=%#v saved=%#v", result, store.saved)
		}
	})

	t.Run("returns model stream error after saving user", func(t *testing.T) {
		streamErr := errors.New("stream failed")
		store := &fakeStore{sessionID: 2}
		_, err := NewChat(store, &fakeAssistant{streamErr: streamErr}).Stream(
			context.Background(), ChatInput{SessionID: "s", Message: "hi"},
		)
		if !errors.Is(err, streamErr) || len(store.saved) != 1 {
			t.Fatalf("err=%v saved=%#v", err, store.saved)
		}
	})
}

func TestPersistingStreamRecv(t *testing.T) {
	t.Run("persists the complete assistant answer exactly once on EOF", func(t *testing.T) {
		store := &fakeStore{}
		stream := &persistingStream{
			AnswerStream: &fakeStream{chunks: []string{"你", "好"}, model: "m"},
			ctx:          context.Background(),
			store:        store,
			sessionDBID:  3,
		}
		for range 2 {
			if _, err := stream.Recv(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v", err)
		}
		if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
			t.Fatalf("second EOF err = %v", err)
		}
		if len(store.saved) != 1 || store.saved[0] != (savedMessage{role: "assistant", content: "你好", model: "m"}) {
			t.Fatalf("saved = %#v", store.saved)
		}
	})

	t.Run("does not persist a partial answer on upstream error", func(t *testing.T) {
		store := &fakeStore{}
		upstreamErr := errors.New("connection reset")
		stream := &persistingStream{
			AnswerStream: &fakeStream{chunks: []string{"partial"}, finalErr: upstreamErr},
			ctx:          context.Background(), store: store, sessionDBID: 3,
		}
		_, _ = stream.Recv()
		if _, err := stream.Recv(); !errors.Is(err, upstreamErr) {
			t.Fatalf("err = %v", err)
		}
		if len(store.saved) != 0 {
			t.Fatalf("saved = %#v", store.saved)
		}
	})

	t.Run("close propagates without persisting", func(t *testing.T) {
		store := &fakeStore{}
		upstream := &fakeStream{chunks: []string{"partial"}}
		stream := &persistingStream{AnswerStream: upstream, ctx: context.Background(), store: store, sessionDBID: 3}
		_, _ = stream.Recv()
		stream.Close()
		if !upstream.closed || len(store.saved) != 0 {
			t.Fatalf("closed=%v saved=%#v", upstream.closed, store.saved)
		}
	})

	t.Run("refreshes memory once after streamed persistence and ignores refresh failure", func(t *testing.T) {
		store := &fakeStore{}
		memory := &fakeMemory{err: errors.New("summary unavailable")}
		stream := &persistingStream{
			AnswerStream: &fakeStream{chunks: []string{"done"}}, ctx: context.Background(),
			store: store, sessionDBID: 9, memory: memory,
		}
		_, _ = stream.Recv()
		if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
			t.Fatalf("err=%v", err)
		}
		if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
			t.Fatalf("second err=%v", err)
		}
		if memory.callsCount != 1 || memory.sessionID != 9 {
			t.Fatalf("memory calls=%d session=%d", memory.callsCount, memory.sessionID)
		}
	})
}

func TestNewSessionID(t *testing.T) {
	id, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("invalid UUID v4: %q", id)
	}
}

type savedMessage struct{ role, content, model string }

type fakeStore struct {
	calls       *[]string
	sessionID   int64
	findErr     error
	saveUserErr error
	saved       []savedMessage
	contextText string
}

func (s *fakeStore) LoadContext(_ context.Context, _ int64, limit int) (string, error) {
	if s.calls != nil {
		*s.calls = append(*s.calls, fmt.Sprintf("context:%d", limit))
	}
	return s.contextText, nil
}

func (s *fakeStore) FindOrCreateSession(_ context.Context, key string, userID int64) (int64, error) {
	if s.calls != nil {
		*s.calls = append(*s.calls, fmt.Sprintf("find:%s:%d", key, userID))
	}
	return s.sessionID, s.findErr
}

func (s *fakeStore) SaveMessage(_ context.Context, _ int64, role, content, model string) error {
	if s.calls != nil {
		*s.calls = append(*s.calls, "save:"+role+":"+content+":"+model)
	}
	if role == "user" && s.saveUserErr != nil {
		return s.saveUserErr
	}
	s.saved = append(s.saved, savedMessage{role: role, content: content, model: model})
	return nil
}

type fakeAssistant struct {
	calls         *[]string
	answer        Answer
	generateErr   error
	generateCalls int
	stream        AnswerStream
	streamErr     error
}

func (a *fakeAssistant) Generate(_ context.Context, input AssistantInput) (Answer, error) {
	a.generateCalls++
	if a.calls != nil {
		*a.calls = append(*a.calls, "generate:"+input.Message+":"+input.Context)
	}
	return a.answer, a.generateErr
}

func (a *fakeAssistant) Stream(_ context.Context, _ AssistantInput) (AnswerStream, error) {
	return a.stream, a.streamErr
}

type fakeStream struct {
	chunks   []string
	index    int
	finalErr error
	model    string
	closed   bool
}

func (s *fakeStream) Recv() (string, error) {
	if s.index < len(s.chunks) {
		chunk := s.chunks[s.index]
		s.index++
		return chunk, nil
	}
	if s.finalErr != nil {
		return "", s.finalErr
	}
	return "", io.EOF
}

func (s *fakeStream) Close()        { s.closed = true }
func (s *fakeStream) Model() string { return s.model }

type fakeMemory struct {
	calls      *[]string
	callsCount int
	sessionID  int64
	err        error
}

func (m *fakeMemory) Refresh(_ context.Context, sessionID int64) error {
	m.callsCount++
	m.sessionID = sessionID
	if m.calls != nil {
		*m.calls = append(*m.calls, fmt.Sprintf("memory:%d", sessionID))
	}
	return m.err
}
