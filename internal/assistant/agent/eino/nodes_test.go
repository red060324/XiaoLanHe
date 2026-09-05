package eino

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

func TestAdvancedNodesRouteAndFallback(t *testing.T) {
	valid := schema.AssistantMessage("{\"route\":\"PLANNING\",\"intent\":\"game_recommendation\",\"skillId\":\"recommend_games\",\"skillVersion\":\"1.0.0\",\"responseMode\":\"ranked_recommendation\"}", nil)
	decision, err := NewAdvancedNodes(&scriptedModel{responses: []*schema.Message{valid}}).Route(context.Background(), "recommend", "profile", nodeBudget(t))
	if err != nil || decision.Route != entity.RoutePlanning || decision.SkillID != "recommend_games" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}

	malformed := schema.AssistantMessage("{\"route\":\"WRITE\",\"reasoning\":\"secret\"}", nil)
	decision, err = NewAdvancedNodes(&scriptedModel{responses: []*schema.Message{malformed}}).Route(context.Background(), "推荐游戏", "", nodeBudget(t))
	if err != nil || decision.Route != entity.RoutePlanning || decision.SkillID != "recommend_games" {
		t.Fatalf("fallback=%+v err=%v", decision, err)
	}
}

func TestAdvancedNodesPlanValidationAndFallback(t *testing.T) {
	registry, err := skill.Load(entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 45_000})
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Resolve("recommend_games", "1.0.0")
	valid := "{\"schemaVersion\":1,\"units\":[{\"id\":\"q1\",\"text\":\"co-op RPG\",\"sources\":[\"lightrag\",\"catalog\"],\"lightragMode\":\"mix\",\"freshness\":\"stable\",\"filters\":{\"region\":\"CN\",\"platforms\":[\"pc\"]},\"requiredFacets\":[\"genre\"]}]}"
	plan, fallback, err := NewAdvancedNodes(&scriptedModel{responses: []*schema.Message{schema.AssistantMessage(valid, nil)}}).Plan(context.Background(), "recommend", "", definition, false, nodeBudget(t))
	if err != nil || fallback || len(plan.Units) != 1 || plan.Units[0].LightRAGMode != entity.LightRAGMix {
		t.Fatalf("plan=%+v fallback=%v err=%v", plan, fallback, err)
	}

	unsafe := strings.Replace(valid, "\"mix\"", "\"bypass\"", 1)
	plan, fallback, err = NewAdvancedNodes(&scriptedModel{responses: []*schema.Message{schema.AssistantMessage(unsafe, nil)}}).Plan(context.Background(), "推荐游戏", "", definition, false, nodeBudget(t))
	if err != nil || !fallback || len(plan.Units) != 1 {
		t.Fatalf("fallback plan=%+v fallback=%v err=%v", plan, fallback, err)
	}
}

func TestAdvancedNodesRespectBudget(t *testing.T) {
	budget, _ := assistant.NewBudget(entity.BudgetLimit{ModelCalls: 1, ToolCalls: 1, Delegations: 1, TimeoutMilliseconds: 1000})
	nodes := NewAdvancedNodes(&scriptedModel{responses: []*schema.Message{schema.AssistantMessage("bad", nil)}})
	_, _ = nodes.Route(context.Background(), "q", "", budget)
	if _, err := nodes.Route(context.Background(), "q", "", budget); !errors.Is(err, assistant.ErrModelBudget) {
		t.Fatalf("err=%v", err)
	}
}

func nodeBudget(t *testing.T) *assistant.Budget {
	t.Helper()
	budget, err := assistant.NewBudget(entity.BudgetLimit{ModelCalls: 4, ToolCalls: 4, Delegations: 2, TimeoutMilliseconds: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return budget
}
