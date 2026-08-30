package presenter

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestChatRequestInput(t *testing.T) {
	tests := []struct {
		name    string
		request ChatRequest
		wantErr error
	}{
		{name: "valid", request: ChatRequest{SessionID: "s", Message: " hello "}},
		{name: "blank", request: ChatRequest{Message: " \n\t "}, wantErr: ErrInvalidMessage},
		{name: "too long", request: ChatRequest{Message: strings.Repeat("a", MaxMessageLength+1)}, wantErr: ErrMessageTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := tt.request.Input()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err=%v want=%v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (input.SessionID != tt.request.SessionID || input.Message != tt.request.Message) {
				t.Fatalf("input = %#v", input)
			}
		})
	}
}

func TestPresentChat(t *testing.T) {
	createdAt := time.Date(2026, 8, 30, 12, 34, 56, 123, time.FixedZone("CST", 8*60*60))
	response := PresentChat(usecase.ChatResult{SessionID: "s", Answer: "a", CreatedAt: createdAt})
	if response.SessionID != "s" || response.Answer != "a" || response.CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("response = %#v", response)
	}
}
