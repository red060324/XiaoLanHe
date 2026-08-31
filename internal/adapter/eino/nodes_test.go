package einoadapter

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestModelNodesRoute(t *testing.T) {
	t.Run("parses router JSON", func(t *testing.T) {
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(`{"routeType":"DIRECT_CHAT","responseMode":"chat","subQueries":[]}`, nil), nil
		}}
		decision, err := NewModelNodes(fake, "m", "route", "direct", "synthesis").Route(context.Background(), "hi", "")
		if err != nil || decision.Route != usecase.RouteDirect || decision.ResponseMode != "chat" {
			t.Fatalf("decision=%#v err=%v", decision, err)
		}
	})

	t.Run("malformed output falls back to local evidence", func(t *testing.T) {
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) { return schema.AssistantMessage("oops", nil), nil }}
		decision, err := NewModelNodes(fake, "m", "route", "direct", "synthesis").Route(context.Background(), "q", "")
		if err != nil || decision.Route != usecase.RouteEvidence || !decision.NeedLocalKnowledge {
			t.Fatalf("decision=%#v err=%v", decision, err)
		}
	})

	t.Run("evidence route always keeps a source", func(t *testing.T) {
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(`{"routeType":"EVIDENCE_ANSWER","responseMode":"qa"}`, nil), nil
		}}
		decision, err := NewModelNodes(fake, "m", "route", "direct", "synthesis").Route(context.Background(), "q", "")
		if err != nil || !decision.NeedLocalKnowledge || decision.NeedWeb {
			t.Fatalf("decision=%#v err=%v", decision, err)
		}
	})
}

func TestModelNodesGenerateAnswer(t *testing.T) {
	fake := &fakeChatModel{generate: func(messages []*schema.Message) (*schema.Message, error) {
		if messages[0].Content != "synthesis" || messages[1].Content == "" {
			t.Fatalf("messages=%#v", messages)
		}
		return schema.AssistantMessage("answer", nil), nil
	}}
	answer, err := NewModelNodes(fake, "m", "route", "direct", "synthesis").GenerateAnswer(context.Background(), usecase.AnswerRequest{Route: usecase.RouteEvidence, Message: "q", Evidence: []usecase.Evidence{{Source: "web", Title: "A", Content: "fact"}}})
	if err != nil || answer.Text != "answer" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
}
