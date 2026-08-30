package usecase

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type ConversationStore interface {
	FindOrCreateSession(context.Context, string) (int64, error)
	SaveMessage(context.Context, int64, string, string, string) error
}

type Assistant interface {
	Generate(context.Context, string) (Answer, error)
	Stream(context.Context, string) (AnswerStream, error)
}

type AnswerStream interface {
	Recv() (string, error)
	Close()
	Model() string
}

type Answer struct {
	Text  string
	Model string
}

type ChatInput struct {
	SessionID string
	Message   string
}

type ChatResult struct {
	SessionID string
	Answer    string
	CreatedAt time.Time
}

type ChatStream struct {
	SessionID string
	Stream    AnswerStream
}

type Chat struct {
	store     ConversationStore
	assistant Assistant
	now       func() time.Time
	newID     func() (string, error)
}

func NewChat(store ConversationStore, assistant Assistant) *Chat {
	return &Chat{store: store, assistant: assistant, now: time.Now, newID: newSessionID}
}

func (c *Chat) Run(ctx context.Context, in ChatInput) (ChatResult, error) {
	sessionID, sessionDBID, err := c.prepare(ctx, in)
	if err != nil {
		return ChatResult{}, err
	}

	answer, err := c.assistant.Generate(ctx, in.Message)
	if err != nil {
		return ChatResult{}, fmt.Errorf("generate answer: %w", err)
	}
	if err := c.store.SaveMessage(ctx, sessionDBID, "assistant", answer.Text, answer.Model); err != nil {
		return ChatResult{}, fmt.Errorf("save assistant message: %w", err)
	}
	return ChatResult{SessionID: sessionID, Answer: answer.Text, CreatedAt: c.now()}, nil
}

func (c *Chat) Stream(ctx context.Context, in ChatInput) (ChatStream, error) {
	sessionID, sessionDBID, err := c.prepare(ctx, in)
	if err != nil {
		return ChatStream{}, err
	}

	stream, err := c.assistant.Stream(ctx, in.Message)
	if err != nil {
		return ChatStream{}, fmt.Errorf("start answer stream: %w", err)
	}
	return ChatStream{
		SessionID: sessionID,
		Stream: &persistingStream{
			AnswerStream: stream,
			ctx:          ctx,
			store:        c.store,
			sessionDBID:  sessionDBID,
		},
	}, nil
}

func (c *Chat) prepare(ctx context.Context, in ChatInput) (string, int64, error) {
	sessionID := in.SessionID
	if strings.TrimSpace(sessionID) == "" {
		var err error
		sessionID, err = c.newID()
		if err != nil {
			return "", 0, fmt.Errorf("create session id: %w", err)
		}
	}
	sessionDBID, err := c.store.FindOrCreateSession(ctx, sessionID)
	if err != nil {
		return "", 0, fmt.Errorf("find or create session: %w", err)
	}
	if err := c.store.SaveMessage(ctx, sessionDBID, "user", in.Message, ""); err != nil {
		return "", 0, fmt.Errorf("save user message: %w", err)
	}
	return sessionID, sessionDBID, nil
}

type persistingStream struct {
	AnswerStream
	ctx         context.Context
	store       ConversationStore
	sessionDBID int64
	answer      []byte
	done        bool
}

func (s *persistingStream) Recv() (string, error) {
	chunk, err := s.AnswerStream.Recv()
	if err == nil {
		s.answer = append(s.answer, chunk...)
		return chunk, nil
	}
	if !errors.Is(err, io.EOF) || s.done {
		return "", err
	}
	s.done = true
	if err := s.store.SaveMessage(s.ctx, s.sessionDBID, "assistant", string(s.answer), s.Model()); err != nil {
		return "", fmt.Errorf("save streamed assistant message: %w", err)
	}
	return "", io.EOF
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
