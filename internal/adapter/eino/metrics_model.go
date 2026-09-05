package einoadapter

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

// ObservedChatModel keeps Eino types at the adapter boundary while collecting
// only request latency, bounded outcomes and provider-reported token counts. It
// never reads or records prompt or response content.
type ObservedChatModel struct {
	base     model.ToolCallingChatModel
	registry *platformmetrics.Registry
}

func ObserveChatModel(base model.ToolCallingChatModel, registry *platformmetrics.Registry) *ObservedChatModel {
	if registry == nil {
		registry = platformmetrics.Default()
	}
	return &ObservedChatModel{base: base, registry: registry}
}

func (m *ObservedChatModel) Generate(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
	started := time.Now()
	response, err := m.base.Generate(ctx, input, options...)
	m.registry.ObserveModel(modelObservation("generate", started, response, err))
	return response, err
}

func (m *ObservedChatModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	started := time.Now()
	upstream, err := m.base.Stream(ctx, input, options...)
	if err != nil {
		m.registry.ObserveModel(modelObservation("stream", started, nil, err))
		return nil, err
	}
	downstream, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		defer upstream.Close()
		var lastWithUsage *schema.Message
		var finalErr error
		var once sync.Once
		record := func() {
			once.Do(func() { m.registry.ObserveModel(modelObservation("stream", started, lastWithUsage, finalErr)) })
		}
		defer record()
		for {
			message, receiveErr := upstream.Recv()
			if message != nil && message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
				lastWithUsage = message
			}
			if receiveErr != nil {
				if !errors.Is(receiveErr, io.EOF) {
					finalErr = receiveErr
				}
				return
			}
			if writer.Send(message, nil) {
				finalErr = context.Canceled
				return
			}
		}
	}()
	return downstream, nil
}

func (m *ObservedChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.base.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &ObservedChatModel{base: bound, registry: m.registry}, nil
}

func modelObservation(operation string, started time.Time, response *schema.Message, err error) platformmetrics.ModelObservation {
	value := platformmetrics.ModelObservation{Operation: operation, Outcome: dependencyOutcome(err), Duration: time.Since(started)}
	if response == nil || response.ResponseMeta == nil || response.ResponseMeta.Usage == nil {
		return value
	}
	usage := response.ResponseMeta.Usage
	value.UsageReported = true
	value.PromptTokens = max(0, usage.PromptTokens)
	value.CompletionTokens = max(0, usage.CompletionTokens)
	value.TotalTokens = max(0, usage.TotalTokens)
	value.CachedTokens = max(0, usage.PromptTokenDetails.CachedTokens)
	value.ReasoningTokens = max(0, usage.CompletionTokensDetails.ReasoningTokens)
	return value
}

func dependencyOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "error"
	}
}

var _ model.ToolCallingChatModel = (*ObservedChatModel)(nil)
