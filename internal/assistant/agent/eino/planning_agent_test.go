package eino

import (
	"context"
	"errors"
	"fmt"
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
	tests := []struct {
		name             string
		skillID          string
		response         string
		games            map[string]catalogentity.Game
		wantStatus       entity.ArtifactStatus
		wantStopReason   string
		wantSubjects     []string
		wantCatalogCalls int
		wantPriceFailure bool
	}{
		{
			name:     "mixed recommendation candidates exclude owned games",
			skillID:  "recommend_games",
			response: `{"status":"complete","items":[{"subjectId":"game-owned","recommendation":"Owned","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]},{"subjectId":"game-open","recommendation":"Try it","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`,
			games: map[string]catalogentity.Game{
				"game-owned": {Slug: "game-owned", Owned: true},
				"game-open":  {Slug: "game-open", Editions: []catalogentity.Edition{{Prices: []catalogentity.Price{{AmountMinor: 5000}}}}},
			},
			wantStatus:       entity.StatusComplete,
			wantStopReason:   "complete",
			wantSubjects:     []string{"game-open"},
			wantCatalogCalls: 2,
			wantPriceFailure: true,
		},
		{
			name:     "all recommendation candidates owned produces no result",
			skillID:  "recommend_games",
			response: `{"status":"complete","items":[{"subjectId":"game-one","recommendation":"One","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]},{"subjectId":"game-two","recommendation":"Two","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`,
			games: map[string]catalogentity.Game{
				"game-one": {Slug: "game-one", Owned: true},
				"game-two": {Slug: "game-two", Owned: true},
			},
			wantStatus:       entity.StatusNoResult,
			wantStopReason:   "no_evidence",
			wantCatalogCalls: 2,
		},
		{
			name:     "unowned recommendation candidate is preserved",
			skillID:  "recommend_games",
			response: `{"status":"complete","items":[{"subjectId":"game-open","recommendation":"Try it","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`,
			games: map[string]catalogentity.Game{
				"game-open": {Slug: "game-open", Editions: []catalogentity.Edition{{Prices: []catalogentity.Price{{AmountMinor: 2000}}}}},
			},
			wantStatus:       entity.StatusComplete,
			wantStopReason:   "complete",
			wantSubjects:     []string{"game-open"},
			wantCatalogCalls: 1,
		},
		{
			name:             "build team keeps evidence subjects without catalog lookup",
			skillID:          "build_team",
			response:         `{"status":"complete","items":[{"subjectId":"tank-role","recommendation":"Use this role","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`,
			wantStatus:       entity.StatusComplete,
			wantStopReason:   "complete",
			wantSubjects:     []string{"tank-role"},
			wantCatalogCalls: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, evidence := planningEvidence()
			task := planningTask(evidence.ID)
			task.SkillID = test.skillID
			task.Constraints.MaxPriceMinor = &maximum
			if test.skillID == "build_team" {
				task.AllowedTools = []string{"read_catalog", "score_constraints"}
			}
			catalogStore := &planningCatalogFake{games: test.games}
			model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage(test.response, nil)}}
			agent, err := NewPlanningAgent(model, catalogStore, 4, 4, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			result, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store)
			if err != nil {
				t.Fatalf("RunPlanning() error = %v", err)
			}
			if err := result.Artifact.Validate(task, map[string]entity.Evidence{evidence.ID: evidence}); err != nil {
				t.Fatalf("returned invalid artifact: %+v err=%v", result.Artifact, err)
			}
			if result.Artifact.Status != test.wantStatus || result.Artifact.StopReason != test.wantStopReason || catalogStore.getCalls != test.wantCatalogCalls {
				t.Fatalf("artifact=%+v catalog=%+v", result.Artifact, catalogStore)
			}
			if len(result.Artifact.Items) != len(test.wantSubjects) {
				t.Fatalf("items=%+v want subjects=%v", result.Artifact.Items, test.wantSubjects)
			}
			for i, subject := range test.wantSubjects {
				if result.Artifact.Items[i].SubjectID != subject {
					t.Fatalf("item[%d]=%+v want subject=%q", i, result.Artifact.Items[i], subject)
				}
			}
			if len(result.Artifact.Items) > 0 {
				item := result.Artifact.Items[0]
				if containsString(item.MatchedConstraints, "owned") || containsString(item.UnmetConstraints, "max_price") != test.wantPriceFailure {
					t.Fatalf("item=%+v", item)
				}
			}
		})
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

func TestPlanningAgentRevalidateClassifiesCatalogErrors(t *testing.T) {
	catalogDependencyErr := errors.New("catalog unavailable")
	tests := []struct {
		name       string
		catalogErr error
		wantIs     error
		wantNotIs  error
	}{
		{name: "cancellation propagates", catalogErr: errors.Join(errors.New("catalog request cancelled"), context.Canceled), wantIs: context.Canceled, wantNotIs: entity.ErrInvalidAgentContract},
		{name: "deadline propagates", catalogErr: errors.Join(errors.New("catalog request timed out"), context.DeadlineExceeded), wantIs: context.DeadlineExceeded, wantNotIs: entity.ErrInvalidAgentContract},
		{name: "missing model subject is invalid contract", catalogErr: fmt.Errorf("lookup game: %w", catalog.ErrNotFound), wantIs: entity.ErrInvalidAgentContract, wantNotIs: catalog.ErrNotFound},
		{name: "dependency error propagates", catalogErr: catalogDependencyErr, wantIs: catalogDependencyErr, wantNotIs: entity.ErrInvalidAgentContract},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, evidence := planningEvidence()
			task := planningTask(evidence.ID)
			model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage(`{"status":"complete","items":[{"subjectId":"game-one","recommendation":"Try it","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`, nil)}}
			catalogStore := &planningCatalogFake{getErr: test.catalogErr}
			agent, err := NewPlanningAgent(model, catalogStore, 4, 4, time.Second)
			if err != nil {
				t.Fatal(err)
			}

			_, runErr := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store)

			if !errors.Is(runErr, test.wantIs) || test.wantNotIs != nil && errors.Is(runErr, test.wantNotIs) || catalogStore.getCalls != 1 {
				t.Fatalf("err=%v wantIs=%v wantNotIs=%v catalog=%+v", runErr, test.wantIs, test.wantNotIs, catalogStore)
			}
		})
	}
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
		schema.AssistantMessage(`{"status":"complete","items":[{"subjectId":"game-two","recommendation":"Try it","matchedConstraints":[],"unmetConstraints":[],"assumptions":[],"alternatives":[],"evidenceIds":["ev_1"]}],"stopReason":"complete"}`, nil),
	}}
	catalogStore := &planningCatalogFake{
		list: catalog.ListResult{Items: []catalogentity.Game{{ID: 10, Slug: "game-one", Owned: true}}},
		games: map[string]catalogentity.Game{
			"game-one": {Slug: "game-one", Owned: true, Editions: []catalogentity.Edition{{ID: 20, Owned: true}}},
			"game-two": {Slug: "game-two"},
		},
	}
	agent, _ := NewPlanningAgent(model, catalogStore, 4, 4, time.Second)
	if _, err := agent.RunPlanning(context.Background(), task, planningBudget(t, 4), store); err != nil || catalogStore.listCalls != 1 || catalogStore.getCalls != 2 {
		t.Fatalf("catalog=%+v err=%v", catalogStore, err)
	}
}

type planningCatalogFake struct {
	list                catalog.ListResult
	game                catalogentity.Game
	games               map[string]catalogentity.Game
	listErr, getErr     error
	listCalls, getCalls int
}

func (f *planningCatalogFake) List(context.Context, catalog.ListInput) (catalog.ListResult, error) {
	f.listCalls++
	return f.list, f.listErr
}
func (f *planningCatalogFake) Get(_ context.Context, slug, _, _ string, _ int64) (catalogentity.Game, error) {
	f.getCalls++
	if f.getErr != nil {
		return catalogentity.Game{}, f.getErr
	}
	if f.games != nil {
		game, ok := f.games[slug]
		if !ok {
			return catalogentity.Game{}, catalog.ErrNotFound
		}
		return game, nil
	}
	return f.game, nil
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
