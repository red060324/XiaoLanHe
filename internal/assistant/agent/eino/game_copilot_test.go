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

func TestGameCopilotResearchOnly(t *testing.T) {
	definition := copilotSkill(t, "research_guide")
	researcher := &researchWorkerFake{evidence: []entity.Evidence{{Source: "lightrag", Content: "genre guide"}}}
	planner := &planningWorkerFake{}
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage(`{"action":"research"}`, nil),
		schema.AssistantMessage(`{"action":"finish"}`, nil),
	}}
	copilot, _ := NewGameCopilot(model, researcher, planner, 4)
	result, err := copilot.Run(context.Background(), copilotInput(t, definition, entity.RouteResearch, entity.EmptyProfile(), copilotBudget(t, 12)))
	if err != nil || researcher.calls != 1 || planner.calls != 0 || len(result.Evidence) != 1 || result.Evidence[0].ID != "ev_1" || result.Plan != nil || result.Usage.ModelCalls != 2 {
		t.Fatalf("result=%+v research=%d planning=%d err=%v", result, researcher.calls, planner.calls, err)
	}
}

func TestGameCopilotPlanningStateMachineAndTypedProfile(t *testing.T) {
	maximum := int64(8800)
	profile := entity.Profile{FavoriteGenres: []string{"rpg"}, PreferredPlatforms: []string{"pc"}, DefaultRegion: "CN", MaxPriceMinor: &maximum, Currency: "CNY"}
	definition := copilotSkill(t, "recommend_games")
	researcher := &researchWorkerFake{evidence: []entity.Evidence{{Source: "lightrag", Content: "rpg guide"}}}
	planner := &planningWorkerFake{}
	model := &scriptedModel{responses: []*schema.Message{
		// Illegal ordering, duplicate research and unknown action all fail closed to the only legal transition.
		schema.AssistantMessage(`{"action":"planning"}`, nil),
		schema.AssistantMessage(`{"action":"research"}`, nil),
		schema.AssistantMessage(`{"action":"create_order"}`, nil),
	}}
	copilot, _ := NewGameCopilot(model, researcher, planner, 4)
	input := copilotInput(t, definition, entity.RoutePlanning, profile, copilotBudget(t, 12))
	input.Context = "[Assistant profile] region=US; max_price=1 USD"
	result, err := copilot.Run(context.Background(), input)
	if err != nil || researcher.calls != 1 || planner.calls != 1 || result.Plan == nil || planner.task.Sequence != 2 || planner.task.Constraints.Region != "CN" || planner.task.Constraints.Currency != "CNY" || planner.task.Constraints.MaxPriceMinor == nil || *planner.task.Constraints.MaxPriceMinor != maximum || len(planner.task.PreferenceProjection["favoriteGenres"]) != 1 {
		t.Fatalf("result=%+v planningTask=%+v err=%v", result, planner.task, err)
	}
}

func TestGameCopilotNoEvidenceDoesNotPlan(t *testing.T) {
	definition := copilotSkill(t, "recommend_games")
	researcher := &researchWorkerFake{}
	planner := &planningWorkerFake{}
	model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage(`{"action":"research"}`, nil), schema.AssistantMessage(`{"action":"planning"}`, nil)}}
	copilot, _ := NewGameCopilot(model, researcher, planner, 4)
	result, err := copilot.Run(context.Background(), copilotInput(t, definition, entity.RoutePlanning, entity.EmptyProfile(), copilotBudget(t, 12)))
	if err != nil || planner.calls != 0 || result.Plan != nil || len(result.Evidence) != 0 {
		t.Fatalf("result=%+v planning=%d err=%v", result, planner.calls, err)
	}
}

func TestGameCopilotRejectsForeignWorkerEvidence(t *testing.T) {
	definition := copilotSkill(t, "recommend_games")
	researcher := &researchWorkerFake{evidence: []entity.Evidence{{Source: "lightrag", Content: "rpg guide"}}}
	planner := &planningWorkerFake{foreignEvidence: true}
	model := &scriptedModel{responses: []*schema.Message{schema.AssistantMessage(`{"action":"research"}`, nil), schema.AssistantMessage(`{"action":"planning"}`, nil)}}
	copilot, _ := NewGameCopilot(model, researcher, planner, 4)
	if _, err := copilot.Run(context.Background(), copilotInput(t, definition, entity.RoutePlanning, entity.EmptyProfile(), copilotBudget(t, 12))); !errors.Is(err, entity.ErrInvalidAgentContract) {
		t.Fatalf("err=%v", err)
	}
}

func TestGameCopilotBudgetsAndCancellation(t *testing.T) {
	definition := copilotSkill(t, "research_guide")
	t.Run("model budget spans supervisor loop", func(t *testing.T) {
		copilot, _ := NewGameCopilot(&scriptedModel{responses: []*schema.Message{schema.AssistantMessage(`{"action":"research"}`, nil)}}, &researchWorkerFake{evidence: []entity.Evidence{{Source: "lightrag", Content: "guide"}}}, &planningWorkerFake{}, 4)
		if _, err := copilot.Run(context.Background(), copilotInput(t, definition, entity.RouteResearch, entity.EmptyProfile(), copilotBudget(t, 1))); !errors.Is(err, assistant.ErrModelBudget) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("cancellation reaches supervisor model", func(t *testing.T) {
		model := &scriptedModel{generate: func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		copilot, _ := NewGameCopilot(model, &researchWorkerFake{}, &planningWorkerFake{}, 4)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := copilot.Run(ctx, copilotInput(t, definition, entity.RouteResearch, entity.EmptyProfile(), copilotBudget(t, 12))); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
}

type researchWorkerFake struct {
	calls    int
	evidence []entity.Evidence
	err      error
}

func (f *researchWorkerFake) RunResearch(ctx context.Context, task entity.ResearchTask, _ entity.QueryPlan, budget *assistant.Budget) (assistant.ResearchWorkerResult, error) {
	f.calls++
	if f.err != nil {
		return assistant.ResearchWorkerResult{}, f.err
	}
	if err := budget.BeginDelegation(ctx, "research"); err != nil {
		return assistant.ResearchWorkerResult{}, err
	}
	covered, missing := fakeFacetCoverage(task.RequiredFacets, f.evidence)
	status, stop := entity.StatusComplete, "complete"
	if len(f.evidence) == 0 {
		status, stop = entity.StatusNoResult, "no_evidence"
	}
	return assistant.ResearchWorkerResult{Artifact: entity.ResearchArtifact{Envelope: task.Envelope, Status: status, CoveredFacets: covered, MissingFacets: missing, StopReason: stop}, Evidence: f.evidence}, nil
}

type planningWorkerFake struct {
	calls           int
	task            entity.PlanningTask
	foreignEvidence bool
	err             error
}

func (f *planningWorkerFake) RunPlanning(ctx context.Context, task entity.PlanningTask, budget *assistant.Budget, _ *assistant.EvidenceStore) (assistant.PlanningWorkerResult, error) {
	f.calls++
	f.task = task
	if f.err != nil {
		return assistant.PlanningWorkerResult{}, f.err
	}
	if err := budget.BeginDelegation(ctx, "planning"); err != nil {
		return assistant.PlanningWorkerResult{}, err
	}
	evidenceID := task.EvidenceIDs[0]
	if f.foreignEvidence {
		evidenceID = "ev_999"
	}
	return assistant.PlanningWorkerResult{Artifact: entity.PlanningArtifact{Envelope: task.Envelope, Status: entity.StatusComplete, StopReason: "complete", Items: []entity.PlanItem{{SubjectID: "game-one", Recommendation: "Try it", EvidenceIDs: []string{evidenceID}}}}}, nil
}

func copilotSkill(t *testing.T, id string) skill.Definition {
	t.Helper()
	registry, err := skill.Load(entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 45_000})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve(id, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func copilotInput(t *testing.T, definition skill.Definition, route entity.Route, profile entity.Profile, budget *assistant.Budget) assistant.CopilotInput {
	t.Helper()
	intent := "game_research"
	if route == entity.RoutePlanning {
		intent = "game_recommendation"
	}
	return assistant.CopilotInput{RunID: "12345678-1234-4123-8123-123456789abc", Message: "recommend", UserID: 7, Profile: profile, Decision: entity.RouterDecision{Route: route, Intent: intent, SkillID: definition.ID, SkillVersion: definition.Version, ResponseMode: "answer"}, Plan: entity.QueryPlan{SchemaVersion: 1, Units: []entity.QueryUnit{{ID: "q1", Text: "rpg guide", Sources: []entity.QuerySource{entity.SourceLightRAG}, LightRAGMode: entity.LightRAGMix, Freshness: "stable", RequiredFacets: []string{"genre"}}}}, Skill: definition, Budget: budget}
}

func copilotBudget(t *testing.T, modelCalls int) *assistant.Budget {
	t.Helper()
	budget, err := assistant.NewBudget(entity.BudgetLimit{ModelCalls: modelCalls, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 2000})
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func fakeFacetCoverage(required []string, evidence []entity.Evidence) ([]string, []string) {
	text := ""
	for _, item := range evidence {
		text += " " + strings.ToLower(item.Title+" "+item.Content)
	}
	covered, missing := []string{}, []string{}
	for _, facet := range required {
		if strings.Contains(text, strings.ToLower(facet)) {
			covered = append(covered, facet)
		} else {
			missing = append(missing, facet)
		}
	}
	return covered, missing
}
