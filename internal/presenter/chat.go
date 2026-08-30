package presenter

import (
	"errors"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

const MaxMessageLength = 16 * 1024

var ErrInvalidMessage = errors.New("message cannot be blank")
var ErrMessageTooLong = errors.New("message is too long")

type ChatRequest struct {
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
}

type ChatResponse struct {
	SessionID string `json:"sessionId"`
	Answer    string `json:"answer"`
	CreatedAt string `json:"createdAt"`
}

func (r ChatRequest) Input() (usecase.ChatInput, error) {
	if strings.TrimSpace(r.Message) == "" {
		return usecase.ChatInput{}, ErrInvalidMessage
	}
	if len(r.Message) > MaxMessageLength {
		return usecase.ChatInput{}, ErrMessageTooLong
	}
	return usecase.ChatInput{SessionID: r.SessionID, Message: r.Message}, nil
}

func PresentChat(result usecase.ChatResult) ChatResponse {
	return ChatResponse{
		SessionID: result.SessionID,
		Answer:    result.Answer,
		CreatedAt: result.CreatedAt.Format(time.RFC3339Nano),
	}
}
