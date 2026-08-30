package einoadapter

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestDirectAssistantGenerate(t *testing.T) {
	t.Run("passes system and user messages and returns model metadata", func(t *testing.T) {
		fake := &fakeChatModel{generate: func(input []*schema.Message) (*schema.Message, error) {
			if len(input) != 2 || input[0].Role != schema.System || input[0].Content != "prompt" || input[1].Role != schema.User || input[1].Content != "hi" {
				t.Fatalf("messages = %#v", input)
			}
			return schema.AssistantMessage("hello", nil), nil
		}}
		answer, err := NewDirectAssistant(fake, "qwen", "prompt").Generate(context.Background(), "hi")
		if err != nil || answer.Text != "hello" || answer.Model != "qwen" {
			t.Fatalf("answer=%#v err=%v", answer, err)
		}
	})

	t.Run("uses compatibility fallback for an empty response", func(t *testing.T) {
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage("", nil), nil
		}}
		answer, err := NewDirectAssistant(fake, "qwen", "prompt").Generate(context.Background(), "hi")
		if err != nil || answer.Text != emptyReply {
			t.Fatalf("answer=%#v err=%v", answer, err)
		}
	})

	t.Run("propagates model error", func(t *testing.T) {
		modelErr := errors.New("provider failed")
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) { return nil, modelErr }}
		_, err := NewDirectAssistant(fake, "qwen", "prompt").Generate(context.Background(), "hi")
		if !errors.Is(err, modelErr) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDirectAssistantStream(t *testing.T) {
	t.Run("skips empty chunks and preserves token order", func(t *testing.T) {
		fake := &fakeChatModel{stream: func(input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			if input[1].Content != "hi" {
				t.Fatalf("messages = %#v", input)
			}
			return schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("", nil),
				schema.AssistantMessage("你", nil),
				schema.AssistantMessage("好", nil),
			}), nil
		}}
		stream, err := NewDirectAssistant(fake, "qwen", "prompt").Stream(context.Background(), "hi")
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
			t.Fatalf("err = %v", err)
		}
		if stream.Model() != "qwen" {
			t.Fatalf("model = %q", stream.Model())
		}
	})

	t.Run("propagates stream creation error", func(t *testing.T) {
		modelErr := errors.New("provider failed")
		fake := &fakeChatModel{stream: func([]*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			return nil, modelErr
		}}
		_, err := NewDirectAssistant(fake, "qwen", "prompt").Stream(context.Background(), "hi")
		if !errors.Is(err, modelErr) {
			t.Fatalf("err = %v", err)
		}
	})
}

type fakeChatModel struct {
	generate func([]*schema.Message) (*schema.Message, error)
	stream   func([]*schema.Message) (*schema.StreamReader[*schema.Message], error)
}

func (m *fakeChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.generate(input)
}

func (m *fakeChatModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.stream(input)
}
