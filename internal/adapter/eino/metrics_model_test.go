package einoadapter

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

func TestObservedChatModelRecordsGenerateAndStreamUsage(t *testing.T) {
	registry := platformmetrics.NewRegistry()
	observed := ObserveChatModel(&observedModelFake{generate: messageWithUsage(3, 2), stream: []*schema.Message{{Content: "chunk"}, messageWithUsage(5, 4)}}, registry)
	if _, err := observed.Generate(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	stream, err := observed.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	stream.Close()
	output := string(registry.Prometheus())
	for _, expected := range []string{`operation="generate"`, `operation="stream"`, `xiaolanhe_model_tokens_total{kind="total"} 14`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
}

func TestObservedChatModelMarksMissingUsageWithoutContent(t *testing.T) {
	registry := platformmetrics.NewRegistry()
	observed := ObserveChatModel(&observedModelFake{generate: &schema.Message{Content: "CANARY_PRIVATE_RESPONSE"}}, registry)
	if _, err := observed.Generate(context.Background(), []*schema.Message{{Content: "CANARY_PRIVATE_PROMPT"}}); err != nil {
		t.Fatal(err)
	}
	output := string(registry.Prometheus())
	if !strings.Contains(output, `usage_reported="false"`) || strings.Contains(output, "CANARY_PRIVATE") {
		t.Fatalf("unsafe or incomplete metrics:\n%s", output)
	}
}

func messageWithUsage(prompt, completion int) *schema.Message {
	return &schema.Message{Content: "response", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}}}
}

type observedModelFake struct {
	generate *schema.Message
	stream   []*schema.Message
}

func (m *observedModelFake) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.generate, nil
}
func (m *observedModelFake) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray(m.stream), nil
}
func (m *observedModelFake) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
