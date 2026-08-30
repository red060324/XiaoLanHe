package einoadapter

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

const emptyReply = "您好，请再说具体一点，我好帮您。"

type DirectAssistant struct {
	model  model.BaseChatModel
	name   string
	prompt string
}

func NewDirectAssistant(chatModel model.BaseChatModel, name, prompt string) *DirectAssistant {
	return &DirectAssistant{model: chatModel, name: name, prompt: prompt}
}

func (a *DirectAssistant) Generate(ctx context.Context, message string) (usecase.Answer, error) {
	response, err := a.model.Generate(ctx, a.messages(message))
	if err != nil {
		return usecase.Answer{}, err
	}
	text := response.Content
	if text == "" {
		text = emptyReply
	}
	return usecase.Answer{Text: text, Model: a.name}, nil
}

func (a *DirectAssistant) Stream(ctx context.Context, message string) (usecase.AnswerStream, error) {
	stream, err := a.model.Stream(ctx, a.messages(message))
	if err != nil {
		return nil, err
	}
	return &directStream{stream: stream, model: a.name}, nil
}

func (a *DirectAssistant) messages(message string) []*schema.Message {
	return []*schema.Message{schema.SystemMessage(a.prompt), schema.UserMessage(message)}
}

type directStream struct {
	stream *schema.StreamReader[*schema.Message]
	model  string
}

func (s *directStream) Recv() (string, error) {
	for {
		message, err := s.stream.Recv()
		if err != nil {
			return "", err
		}
		if message.Content != "" {
			return message.Content, nil
		}
	}
}

func (s *directStream) Close()        { s.stream.Close() }
func (s *directStream) Model() string { return s.model }
