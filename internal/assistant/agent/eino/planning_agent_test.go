package eino

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
)

func TestPlanningAgentValidatesAndRechecksFacts(t *testing.T) {
	maximum := int64(3000)
	store, evidence := planningEvidence()
	task := planningTask(evidence.ID)
	task.Constraints.MaxPriceMinor = &maximum
	catalogStore := &planningCatalogFake{game: catalogentity.Game{Slug: "game-one", Owned: true, Editions: []catalogentity.Edition{{Prices: []catalogentity.Price{{AmountMinor: 5000}}}}}}
	model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage(`{"status":"complete","items":[{"subjectId":"game-one","recommendation":"Try it","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`, nil)}}
	agent, err := NewPlanningAgent(model, catalogStore, 4, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store)
	if err != nil || len(result.Artifact.Items) != 1 || !containsString(result.Artifact.Items[0].MatchedConstraints, "owned") || !containsString(result.Artifact.Items[0].UnmetConstraints, "max_price") || catalogStore.getCalls != 1 {
		t.Fatalf("result=%+v catalog=%+v err=%v", result, catalogStore, err)
	}
}

func TestPlanningAgentRejectsForeignEvidenceAndWriteTools(t *testing.T) {
	store, evidence := planningEvidence()
	task := planningTask(evidence.ID)
	t.Run("foreign evidence", func(t *testing.T) {
		model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage(`{"status":"complete","items":[{"subjectId":"game-one","recommendation":"Try it","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_999"]}],"stopReason":"complete"}`, nil)}}
		agent, _ := NewPlanningAgent(model, &planningCatalogFake{}, 4, 4, time.Second)
		if _, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store); !errors.Is(err, entity.ErrInvalidAgentContract) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("write tool", func(t *testing.T) {
		model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "1", Function: schema.FunctionCall{Name: "create_order", Arguments: `{}`}}})}}
		catalogStore := &planningCatalogFake{}
		agent, _ := NewPlanningAgent(model, catalogStore, 4, 4, time.Second)
		if _, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store); err == nil || catalogStore.listCalls != 0 || catalogStore.getCalls != 0 {
			t.Fatalf("catalog=%+v err=%v", catalogStore, err)
		}
	})
}

func TestPlanningAgentEnforcesLocalToolAndDeadlineLimits(t *testing.T) {
	store, evidence := planningEvidence()
	task := planningTask(evidence.ID)
	t.Run("tool limit", func(t *testing.T) {
		model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "read_catalog", Arguments: `{"query":"one"}`}},
			{ID: "2", Function: schema.FunctionCall{Name: "read_catalog", Arguments: `{"query":"two"}`}},
		})}}
		catalogStore := &planningCatalogFake{}
		agent, _ := NewPlanningAgent(model, catalogStore, 4, 1, time.Second)
		if _, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 8), store); !errors.Is(err, assistant.ErrToolBudget) || catalogStore.listCalls != 1 {
			t.Fatalf("calls=%d err=%v", catalogStore.listCalls, err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		model := &scriptedModel{generate: func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		agent, _ := NewPlanningAgent(model, &planningCatalogFake{}, 4, 4, 10*time.Millisecond)
		if _, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPlanningEntitlementsLoadOwnedEditionDetails(t *testing.T) {
	store, evidence := planningEvidence()
	task := planningTask(evidence.ID)
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "1", Function: schema.FunctionCall{Name: "read_entitlements", Arguments: `{"query":"owned","region":"CN","currency":"CNY"}`}}}),
		schema.AssistantMessage(`{"status":"complete","items":[{"subjectId":"game-one","recommendation":"Already owned","matchedConstraints":["owned"],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`, nil),
	}}
	catalogStore := &planningCatalogFake{list: catalog.ListResult{Items: []catalogentity.Game{{ID: 10, Slug: "game-one", Owned: true}}}, game: catalogentity.Game{Slug: "game-one", Owned: true, Editions: []catalogentity.Edition{{ID: 20, Owned: true}}}}
	agent, _ := NewPlanningAgent(model, catalogStore, 4, 4, time.Second)
	if _, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store); err != nil || catalogStore.listCalls != 1 || catalogStore.getCalls != 2 {
		t.Fatalf("catalog=%+v err=%v", catalogStore, err)
	}
}

type planningCatalogFake struct {
	list                catalog.ListResult
	game                catalogentity.Game
	listErr, getErr     error
	listCalls, getCalls int
}

func (f *planningCatalogFake) List(context.Context, catalog.ListInput) (catalog.ListResult, error) {
	f.listCalls++
	return f.list, f.listErr
}
func (f *planningCatalogFake) Get(context.Context, string, string, string, int64) (catalogentity.Game, error) {
	f.getCalls++
	return f.game, f.getErr
}

func planningEvidence() (*assistant.EvidenceStore, entity.Evidence) {
	store := assistant.NewEvidenceStore()
	return store, store.Add(entity.Evidence{Source: "lightrag", Content: "genre RPG"})
}
func planningTask(evidenceID string) entity.PlanningTask {
	return entity.PlanningTask{Envelope: testEnvelope(2, "recommend_games"), Goal: "recommend", EvidenceIDs: []string{evidenceID}, AllowedTools: []string{"read_catalog", "read_entitlements", "score_constraints"}, UserID: 7}
}
func planningBudget(t *testing.T, tools int) *assistant.Budget {
	t.Helper()
	budget, err := assistant.NewBudget(entity.BudgetLimit{ModelCalls: 12, ToolCalls: tools, Delegations: 3, TimeoutMilliseconds: 2000})
	if err != nil {
		t.Fatal(err)
	}
	return budget
}
func testEnvelope(sequence int, skillID string) entity.Envelope {
	return entity.Envelope{SchemaVersion: 1, RunID: "12345678-1234-4123-8123-123456789abc", Sequence: sequence, SkillID: skillID, SkillVersion: "1.0.0"}
}
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
