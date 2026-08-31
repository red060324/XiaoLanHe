package einoadapter

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestModelNodesStreamAnswer(t *testing.T) {
	t.Run("skips empty chunks and preserves token order", func(t *testing.T) {
		fake := &fakeChatModel{stream: func(input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			if input[1].Content == "" {
				t.Fatal("missing user message")
			}
			return schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("", nil),
				schema.AssistantMessage("你", nil),
				schema.AssistantMessage("好", nil),
			}), nil
		}}
		stream, err := NewModelNodes(fake, "qwen", "route", "direct", "synthesis").StreamAnswer(context.Background(), usecase.AnswerRequest{Route: usecase.RouteDirect, Message: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		for _, want := range []string{"你", "好"} {
			got, err := stream.Recv()
			if err != nil || got != want {
				t.Fatalf("got=%q err=%v want=%q", got, err, want)
			}
		}
		if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
			t.Fatalf("err=%v", err)
		}
		if stream.Model() != "qwen" {
			t.Fatalf("model=%q", stream.Model())
		}
	})

	t.Run("propagates stream creation error", func(t *testing.T) {
		want := errors.New("provider failed")
		fake := &fakeChatModel{stream: func([]*schema.Message) (*schema.StreamReader[*schema.Message], error) { return nil, want }}
		_, err := NewModelNodes(fake, "qwen", "route", "direct", "synthesis").StreamAnswer(context.Background(), usecase.AnswerRequest{Route: usecase.RouteDirect, Message: "hi"})
		if !errors.Is(err, want) {
			t.Fatalf("err=%v", err)
		}
	})
}

type fakeChatModel struct {
	generate        func([]*schema.Message) (*schema.Message, error)
	generateContext func(context.Context, []*schema.Message) (*schema.Message, error)
	stream          func([]*schema.Message) (*schema.StreamReader[*schema.Message], error)
}

func (m *fakeChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if m.generateContext != nil {
		return m.generateContext(ctx, input)
	}
	return m.generate(input)
}

func (m *fakeChatModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.stream(input)
}

func (m *fakeChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	clone := *m
	return &clone, nil
}
