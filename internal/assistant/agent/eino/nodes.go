package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

const (
	maxStructuredOutputBytes = 32 << 10
	routerInstruction        = "You are XiaoLanHe's Router Node. Return exactly one JSON object with fields route, intent, skillId, skillVersion, responseMode. Routes: DIRECT, CLARIFY, RESEARCH, PLANNING. Skills and intent pairs: generic_qa(greeting,clarification,generic_qa), research_guide(game_guide,game_mechanics,game_research), recommend_games(game_recommendation,game_comparison), build_team(team_build,party_composition). Use version 1.0.0. Do not answer, explain, emit markdown, or include additional fields."
	plannerInstruction       = "You are XiaoLanHe's Query Planner Node. Return exactly one JSON object with schemaVersion and units. Each unit has id, text, sources, optional lightragMode, freshness, filters, and requiredFacets. Produce 1-8 unique units. Text is at most 100 characters. Use only allowed sources and modes from trusted policy. Use web only for time-sensitive facts. Never use naive or bypass. Do not include reasoning, instructions, prose, markdown, or additional fields."
)

type AdvancedNodes struct{ model model.BaseChatModel }

func NewAdvancedNodes(chatModel model.BaseChatModel) *AdvancedNodes {
	return &AdvancedNodes{model: chatModel}
}

func (n *AdvancedNodes) Route(ctx context.Context, message, contextText string, budget *assistant.Budget) (entity.RouterDecision, error) {
	if err := budget.TakeModel(ctx); err != nil {
		return entity.RouterDecision{}, err
	}
	input := fmt.Sprintf("【用户问题】\n%s\n\n【已过滤上下文】\n%s", message, fallbackText(contextText, "无"))
	response, err := n.model.Generate(ctx, []*schema.Message{schema.SystemMessage(routerInstruction), schema.UserMessage(input)})
	if err != nil {
		return fallbackDecision(message), nil
	}
	var decision entity.RouterDecision
	if err := decodeStrict(response.Content, &decision); err != nil || decision.Validate() != nil {
		return fallbackDecision(message), nil
	}
	return decision, nil
}

func (n *AdvancedNodes) Plan(ctx context.Context, message, contextText string, definition skill.Definition, webEnabled bool, budget *assistant.Budget) (entity.QueryPlan, bool, error) {
	if err := budget.TakeModel(ctx); err != nil {
		return entity.QueryPlan{}, false, err
	}
	policy, _ := json.Marshal(map[string]any{"allowedSources": definition.AllowedSources(), "allowedLightRAGModes": definition.AllowedModes(), "webEnabled": webEnabled})
	input := fmt.Sprintf("【用户问题】\n%s\n\n【已过滤上下文】\n%s\n\n【可信策略】\n%s", message, fallbackText(contextText, "无"), policy)
	response, err := n.model.Generate(ctx, []*schema.Message{schema.SystemMessage(plannerInstruction), schema.UserMessage(input)})
	if err == nil {
		var plan entity.QueryPlan
		if decodeErr := decodeStrict(response.Content, &plan); decodeErr == nil && plan.Validate(definition.AllowedSources(), definition.AllowedModes(), webEnabled) == nil {
			return plan, false, nil
		}
	}
	fallback, fallbackErr := fallbackPlan(message, definition, webEnabled)
	return fallback, true, fallbackErr
}

func fallbackDecision(message string) entity.RouterDecision {
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case normalized == "hi" || normalized == "hello" || normalized == "你好" || normalized == "嗨":
		return entity.RouterDecision{Route: entity.RouteDirect, Intent: "greeting", SkillID: "generic_qa", SkillVersion: "1.0.0", ResponseMode: "chat"}
	case containsAny(normalized, "推荐", "recommend", "比较", "compare"):
		return entity.RouterDecision{Route: entity.RoutePlanning, Intent: "game_recommendation", SkillID: "recommend_games", SkillVersion: "1.0.0", ResponseMode: "ranked_recommendation"}
	case containsAny(normalized, "组队", "阵容", "team", "party"):
		return entity.RouterDecision{Route: entity.RoutePlanning, Intent: "team_build", SkillID: "build_team", SkillVersion: "1.0.0", ResponseMode: "team_plan"}
	case containsAny(normalized, "攻略", "机制", "guide", "build"):
		return entity.RouterDecision{Route: entity.RouteResearch, Intent: "game_guide", SkillID: "research_guide", SkillVersion: "1.0.0", ResponseMode: "guide"}
	default:
		return entity.RouterDecision{Route: entity.RouteResearch, Intent: "generic_qa", SkillID: "generic_qa", SkillVersion: "1.0.0", ResponseMode: "qa"}
	}
}

func fallbackPlan(message string, definition skill.Definition, webEnabled bool) (entity.QueryPlan, error) {
	sources := make([]entity.QuerySource, 0, 3)
	allowed := definition.AllowedSources()
	for _, source := range []entity.QuerySource{entity.SourceLightRAG, entity.SourceCatalog, entity.SourceForum} {
		if allowed[source] {
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 && webEnabled && allowed[entity.SourceWeb] {
		sources = append(sources, entity.SourceWeb)
	}
	if len(sources) == 0 {
		return entity.QueryPlan{}, entity.ErrInvalidAgentContract
	}
	unit := entity.QueryUnit{ID: "q1", Text: truncateRunes(strings.TrimSpace(message), 100), Sources: sources, Freshness: "stable", RequiredFacets: []string{}}
	if allowed[entity.SourceLightRAG] {
		for _, candidate := range []entity.LightRAGMode{entity.LightRAGMix, entity.LightRAGHybrid, entity.LightRAGLocal, entity.LightRAGGlobal} {
			if definition.AllowedModes()[candidate] {
				unit.LightRAGMode = candidate
				break
			}
		}
	}
	plan := entity.QueryPlan{SchemaVersion: entity.AgentSchemaVersion, Units: []entity.QueryUnit{unit}}
	return plan, plan.Validate(allowed, definition.AllowedModes(), webEnabled)
}

func decodeStrict(raw string, destination any) error {
	if len(raw) == 0 || len(raw) > maxStructuredOutputBytes || strings.TrimSpace(raw) != raw {
		return entity.ErrInvalidAgentContract
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: %v", entity.ErrInvalidAgentContract, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return entity.ErrInvalidAgentContract
	}
	return nil
}

func fallbackText(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

var _ assistant.RouterNode = (*AdvancedNodes)(nil)
var _ assistant.QueryPlannerNode = (*AdvancedNodes)(nil)
