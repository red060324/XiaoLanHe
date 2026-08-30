package einoadapter

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestAgentModelPlan(t *testing.T) {
	t.Run("parses planner JSON", func(t *testing.T) {
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(`{"routeType":"DIRECT_CHAT","responseMode":"chat","subQueries":[]}`, nil), nil
		}}
		plan, err := NewAgentModel(fake, "m", "plan", "research", "direct", "synthesis").Plan(context.Background(), "hi", "")
		if err != nil || plan.Route != usecase.RouteDirect || plan.ResponseMode != "chat" {
			t.Fatalf("plan=%#v err=%v", plan, err)
		}
	})
	t.Run("malformed output falls back to local evidence", func(t *testing.T) {
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) { return schema.AssistantMessage("oops", nil), nil }}
		plan, err := NewAgentModel(fake, "m", "plan", "research", "direct", "synthesis").Plan(context.Background(), "q", "")
		if err != nil || plan.Route != usecase.RouteEvidence || !plan.NeedLocalKnowledge {
			t.Fatalf("plan=%#v err=%v", plan, err)
		}
	})
	t.Run("evidence route always keeps a source", func(t *testing.T) {
		fake := &fakeChatModel{generate: func([]*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(`{"routeType":"EVIDENCE_ANSWER","responseMode":"qa"}`, nil), nil
		}}
		plan, err := NewAgentModel(fake, "m", "plan", "research", "direct", "synthesis").Plan(context.Background(), "q", "")
		if err != nil || !plan.NeedLocalKnowledge || plan.NeedWeb {
			t.Fatalf("plan=%#v err=%v", plan, err)
		}
	})
}

func TestAgentModelGenerateAnswer(t *testing.T) {
	fake := &fakeChatModel{generate: func(messages []*schema.Message) (*schema.Message, error) {
		if messages[0].Content != "synthesis" || messages[1].Content == "" {
			t.Fatalf("messages=%#v", messages)
		}
		return schema.AssistantMessage("answer", nil), nil
	}}
	answer, err := NewAgentModel(fake, "m", "plan", "research", "direct", "synthesis").GenerateAnswer(context.Background(), usecase.AnswerRequest{Route: usecase.RouteEvidence, Message: "q", Evidence: []usecase.Evidence{{Source: "web", Title: "A", Content: "fact"}}})
	if err != nil || answer.Text != "answer" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
}

func TestAgentModelDecompose(t *testing.T) {
	fake := &fakeChatModel{generate: func(messages []*schema.Message) (*schema.Message, error) {
		if messages[0].Content != "research" {
			t.Fatalf("messages=%#v", messages)
		}
		return schema.AssistantMessage(`{"needLocalKnowledge":true,"needWebSearch":true,"subQueries":["latest"],"notes":["fresh"]}`, nil), nil
	}}
	plan, err := NewAgentModel(fake, "m", "plan", "research", "direct", "synthesis").Decompose(context.Background(), usecase.Plan{Queries: []string{"original", "old"}})
	if err != nil || !plan.NeedLocalKnowledge || !plan.NeedWeb || len(plan.Queries) != 2 || plan.Queries[1] != "latest" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
}
