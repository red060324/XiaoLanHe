package eino

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type scriptedModel struct {
	responses []*schema.Message
	inputs    [][]*schema.Message
	generate  func(context.Context, []*schema.Message) (*schema.Message, error)
}

func (m *scriptedModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.inputs = append(m.inputs, input)
	if m.generate != nil {
		return m.generate(ctx, input)
	}
	if len(m.responses) == 0 {
		return nil, errors.New("model unavailable")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}
func (*scriptedModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unsupported")
}
func (m *scriptedModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
