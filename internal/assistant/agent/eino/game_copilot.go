package eino

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

const copilotInstruction = "You are XiaoLanHe's Game Copilot supervisor Agent. Return strict JSON only: {action:research|planning|finish}. Select one supplied next action. RESEARCH must precede PLANNING. Do not answer the user, call business tools, repeat delegates, or include reasoning or extra fields."

type GameCopilot struct {
	model         model.BaseChatModel
	researcher    assistant.ResearchWorker
	planner       assistant.PlanningWorker
	maxIterations int
}

func NewGameCopilot(chatModel model.BaseChatModel, researcher assistant.ResearchWorker, planner assistant.PlanningWorker, maxIterations int) (*GameCopilot, error) {
	if chatModel == nil || researcher == nil || planner == nil || maxIterations < 1 || maxIterations > 4 {
		return nil, errors.New("invalid Game Copilot configuration")
	}
	return &GameCopilot{model: chatModel, researcher: researcher, planner: planner, maxIterations: maxIterations}, nil
}

func (a *GameCopilot) Run(ctx context.Context, input assistant.CopilotInput) (result assistant.CopilotResult, runErr error) {
	started := time.Now()
	defer func() {
		usage := entity.BudgetUsage{}
		if input.Budget != nil {
			usage = input.Budget.Usage()
		}
		outcome := "ok"
		if runErr != nil {
			outcome = "error"
		}
		assistant.LogRunEvent(ctx, "assistant.agent", input.RunID, "game_copilot", "supervise", outcome, assistant.StopReason(runErr), started, usage, slog.String("error_class", assistant.ErrorClass(runErr)), slog.String("route", string(input.Decision.Route)), slog.String("skill_id", input.Skill.ID), slog.Int("evidence_count", len(result.Evidence)), slog.Bool("has_plan", result.Plan != nil))
	}()
	baseEnvelope := entity.Envelope{SchemaVersion: entity.AgentSchemaVersion, RunID: input.RunID, Sequence: 1, SkillID: input.Skill.ID, SkillVersion: input.Skill.Version}
	if input.Budget == nil || baseEnvelope.Validate() != nil || input.Decision.Validate() != nil || !input.Skill.Supports(input.Decision) || input.Plan.Validate(input.Skill.AllowedSources(), input.Skill.AllowedModes(), input.WebEnabled) != nil {
		return result, entity.ErrInvalidAgentContract
	}
	evidenceStore := assistant.NewEvidenceStore()
	sequence := 1
	completed := map[string]bool{}
	for range a.maxIterations {
		selectionStarted := time.Now()
		delegate, err := a.nextAction(ctx, input, completed, len(evidenceStore.IDs()), result.Plan != nil)
		if err != nil {
			return result, err
		}
		slog.InfoContext(ctx, "assistant delegation selected", "event", "assistant.delegate", "run_id", input.RunID, "agent_role", "game_copilot", "delegate", delegate, "sequence", sequence, "evidence_count", len(evidenceStore.IDs()), "outcome", "selected")
		assistant.RecordAssistantEvent("assistant.delegate", "game_copilot", "select", "selected", "complete", string(input.Decision.Route), input.Skill.ID, selectionStarted)
		if delegate == "finish" {
			result.Usage = input.Budget.Usage()
			return result, nil
		}
		envelope := entity.Envelope{SchemaVersion: entity.AgentSchemaVersion, RunID: input.RunID, Sequence: sequence, SkillID: input.Skill.ID, SkillVersion: input.Skill.Version}
		sequence++
		switch delegate {
		case "research":
			task := entity.ResearchTask{Envelope: envelope, Objective: truncateCopilotText(input.Message, 500), QueryUnitIDs: unitIDs(input.Plan), RequiredFacets: planFacets(input.Plan), AllowedTools: delegateTools(input.Skill.Tools, "search_")}
			workerResult, workerErr := a.researcher.RunResearch(ctx, task, input.Plan, input.Budget)
			if workerErr != nil {
				return result, workerErr
			}
			for _, value := range workerResult.Evidence {
				result.Evidence = append(result.Evidence, evidenceStore.Add(value))
			}
			workerResult.Artifact.EvidenceIDs = evidenceStore.IDs()
			known := evidenceMap(result.Evidence)
			if workerResult.Artifact.StopReason == "" {
				workerResult.Artifact.StopReason = "complete"
			}
			if validateErr := workerResult.Artifact.Validate(task, known); validateErr != nil {
				return result, validateErr
			}
			result.Notes = append(result.Notes, workerResult.Artifact.Assumptions...)
		case "planning":
			if len(evidenceStore.IDs()) == 0 {
				result.Notes = append(result.Notes, "研究阶段没有形成可验证证据，未生成无依据的规划。")
				result.Usage = input.Budget.Usage()
				return result, nil
			}
			task := entity.PlanningTask{Envelope: envelope, Goal: truncateCopilotText(input.Message, 500), Constraints: constraintsFromProfile(input.Profile), PreferenceProjection: preferenceProjection(input.Profile), EvidenceIDs: evidenceStore.IDs(), AllowedTools: delegateTools(input.Skill.Tools, "read_", "score_"), UserID: input.UserID}
			workerResult, workerErr := a.planner.RunPlanning(ctx, task, input.Budget, evidenceStore)
			if workerErr != nil {
				return result, workerErr
			}
			if validateErr := workerResult.Artifact.Validate(task, evidenceMap(result.Evidence)); validateErr != nil {
				return result, validateErr
			}
			result.Plan = &workerResult.Artifact
		}
		completed[delegate] = true
	}
	result.Usage = input.Budget.Usage()
	if completed["research"] && (input.Decision.Route != entity.RoutePlanning || completed["planning"]) {
		return result, nil
	}
	return result, assistant.ErrModelBudget
}

func (a *GameCopilot) nextAction(ctx context.Context, input assistant.CopilotInput, completed map[string]bool, evidenceCount int, hasPlan bool) (string, error) {
	if err := input.Budget.TakeModel(ctx); err != nil {
		return "", err
	}
	allowed := legalCopilotActions(input, completed, evidenceCount)
	payload, _ := json.Marshal(map[string]any{
		"route": input.Decision.Route, "allowedActions": allowed, "queryPlan": input.Plan,
		"progress": map[string]any{"researchComplete": completed["research"], "planningComplete": completed["planning"], "evidenceCount": evidenceCount, "hasPlan": hasPlan},
	})
	response, err := a.model.Generate(ctx, []*schema.Message{schema.SystemMessage(copilotInstruction), schema.UserMessage(string(payload))})
	if err == nil {
		var output struct {
			Action string `json:"action"`
		}
		if decodeStrict(response.Content, &output) == nil && stringSliceContains(allowed, output.Action) {
			return output.Action, nil
		}
	}
	return safeCopilotAction(input, completed, evidenceCount), nil
}

func legalCopilotActions(input assistant.CopilotInput, completed map[string]bool, evidenceCount int) []string {
	result := []string{}
	if !completed["research"] && input.Skill.AllowsDelegate("research") {
		result = append(result, "research")
	}
	if completed["research"] && evidenceCount > 0 && !completed["planning"] && input.Decision.Route == entity.RoutePlanning && input.Skill.AllowsDelegate("planning") {
		result = append(result, "planning")
	}
	if completed["research"] && (input.Decision.Route != entity.RoutePlanning || completed["planning"] || evidenceCount == 0) {
		result = append(result, "finish")
	}
	return result
}

func safeCopilotAction(input assistant.CopilotInput, completed map[string]bool, evidenceCount int) string {
	allowed := legalCopilotActions(input, completed, evidenceCount)
	for _, action := range []string{"research", "planning", "finish"} {
		if stringSliceContains(allowed, action) {
			return action
		}
	}
	return "finish"
}

func unitIDs(plan entity.QueryPlan) []string {
	result := make([]string, 0, len(plan.Units))
	for _, unit := range plan.Units {
		result = append(result, unit.ID)
	}
	return result
}
func planFacets(plan entity.QueryPlan) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, unit := range plan.Units {
		for _, facet := range unit.RequiredFacets {
			if !seen[facet] {
				seen[facet] = true
				result = append(result, facet)
			}
		}
	}
	return result
}
func delegateTools(tools []string, prefixes ...string) []string {
	result := []string{}
	for _, name := range tools {
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				result = append(result, name)
				break
			}
		}
	}
	return result
}
func evidenceMap(values []entity.Evidence) map[string]entity.Evidence {
	result := make(map[string]entity.Evidence, len(values))
	for _, value := range values {
		result[value.ID] = value
	}
	return result
}
func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func truncateCopilotText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return string(runes)
}

func constraintsFromProfile(profile entity.Profile) entity.Constraints {
	return entity.Constraints{Region: profile.DefaultRegion, Platforms: append([]string(nil), profile.PreferredPlatforms...), MaxPriceMinor: profile.MaxPriceMinor, Currency: profile.Currency}
}
func preferenceProjection(profile entity.Profile) map[string][]string {
	return map[string][]string{"favoriteGenres": append([]string(nil), profile.FavoriteGenres...), "preferredPlatforms": append([]string(nil), profile.PreferredPlatforms...)}
}

var _ assistant.Copilot = (*GameCopilot)(nil)
