package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
)

const planningInstruction = "You are XiaoLanHe's Planning Agent. Use only the supplied read-only tools. Return one strict JSON object with status, items and stopReason. Each item has subjectId, recommendation, matchedConstraints, unmetConstraints, assumptions, alternatives and evidenceIds. Never invent evidence IDs. Never call commerce, coupon, order, payment, flash-sale, post or comment operations. Do not output reasoning or markdown."

type PlanningCatalog interface {
	List(context.Context, catalog.ListInput) (catalog.ListResult, error)
	Get(context.Context, string, string, string, int64) (catalogentity.Game, error)
}

type PlanningAgent struct {
	model         model.ToolCallingChatModel
	catalog       PlanningCatalog
	maxIterations int
	maxToolCalls  int
	timeout       time.Duration
}

func NewPlanningAgent(chatModel model.ToolCallingChatModel, catalogSearch PlanningCatalog, maxIterations, maxToolCalls int, timeout time.Duration) (*PlanningAgent, error) {
	if chatModel == nil || catalogSearch == nil || maxIterations < 1 || maxIterations > 4 || maxToolCalls < 1 || maxToolCalls > 4 || timeout <= 0 {
		return nil, errors.New("invalid planning agent configuration")
	}
	return &PlanningAgent{model: chatModel, catalog: catalogSearch, maxIterations: maxIterations, maxToolCalls: maxToolCalls, timeout: timeout}, nil
}

func (a *PlanningAgent) RunPlanning(ctx context.Context, task entity.PlanningTask, budget *assistant.Budget, evidenceStore *assistant.EvidenceStore) (result assistant.PlanningWorkerResult, runErr error) {
	started := time.Now()
	toolCalls := 0
	defer func() {
		usage := entity.BudgetUsage{}
		if budget != nil {
			usage = budget.Usage()
		}
		outcome := "ok"
		if runErr != nil {
			outcome = "error"
		}
		assistant.LogRunEvent(ctx, "assistant.agent", task.RunID, "planning", "plan", outcome, assistant.StopReason(runErr), started, usage, slog.String("error_class", assistant.ErrorClass(runErr)), slog.String("skill_id", task.SkillID), slog.Int("local_tool_calls", toolCalls), slog.Int("evidence_count", len(task.EvidenceIDs)))
	}()
	if budget == nil || evidenceStore == nil {
		return assistant.PlanningWorkerResult{}, entity.ErrInvalidAgentContract
	}
	knownValues, err := evidenceStore.Get(task.EvidenceIDs)
	if err != nil {
		return assistant.PlanningWorkerResult{}, err
	}
	known := make(map[string]entity.Evidence, len(knownValues))
	for _, item := range knownValues {
		known[item.ID] = item
	}
	if err := task.Validate(known); err != nil {
		return assistant.PlanningWorkerResult{}, err
	}
	if err := budget.BeginDelegation(ctx, "planning"); err != nil {
		return assistant.PlanningWorkerResult{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	tools, err := a.tools(task)
	if err != nil {
		return assistant.PlanningWorkerResult{}, err
	}
	infos, err := planningToolInfos(runCtx, tools)
	if err != nil {
		return assistant.PlanningWorkerResult{}, err
	}
	bound, err := a.model.WithTools(infos)
	if err != nil {
		return assistant.PlanningWorkerResult{}, err
	}
	payload, _ := json.Marshal(struct {
		Goal                 string              `json:"goal"`
		Constraints          entity.Constraints  `json:"constraints"`
		PreferenceProjection map[string][]string `json:"preferenceProjection"`
		Evidence             []entity.Evidence   `json:"evidence"`
	}{task.Goal, task.Constraints, task.PreferenceProjection, knownValues})
	messages := []*schema.Message{schema.SystemMessage(planningInstruction), schema.UserMessage(string(payload))}
	for range a.maxIterations {
		if err := budget.TakeModel(runCtx); err != nil {
			return assistant.PlanningWorkerResult{}, err
		}
		response, modelErr := bound.Generate(runCtx, messages)
		if modelErr != nil {
			return assistant.PlanningWorkerResult{}, modelErr
		}
		messages = append(messages, response)
		if len(response.ToolCalls) == 0 {
			artifact, parseErr := parsePlanningArtifact(response.Content, task.Envelope)
			if parseErr != nil {
				return assistant.PlanningWorkerResult{}, parseErr
			}
			if validateErr := artifact.Validate(task, known); validateErr != nil {
				return assistant.PlanningWorkerResult{}, validateErr
			}
			if validateErr := a.revalidate(runCtx, task, &artifact); validateErr != nil {
				return assistant.PlanningWorkerResult{}, validateErr
			}
			artifact.Usage = budget.Usage()
			return assistant.PlanningWorkerResult{Artifact: artifact, Evidence: knownValues}, nil
		}
		for _, call := range response.ToolCalls {
			toolStarted := time.Now()
			if toolCalls >= a.maxToolCalls {
				return assistant.PlanningWorkerResult{}, assistant.ErrToolBudget
			}
			invokable, ok := tools[call.Function.Name]
			if !ok {
				return assistant.PlanningWorkerResult{}, fmt.Errorf("planning tool %s is not allowed", call.Function.Name)
			}
			if err := budget.TakeTool(runCtx); err != nil {
				return assistant.PlanningWorkerResult{}, err
			}
			toolCalls++
			observation, invokeErr := invokable.InvokableRun(runCtx, call.Function.Arguments)
			if invokeErr != nil {
				assistant.RecordAssistantEvent("assistant.tool", "planning", call.Function.Name, "failed", "dependency_unavailable", "planning", task.SkillID, toolStarted)
				return assistant.PlanningWorkerResult{}, invokeErr
			}
			assistant.RecordAssistantEvent("assistant.tool", "planning", call.Function.Name, "ok", "complete", "planning", task.SkillID, toolStarted)
			messages = append(messages, schema.ToolMessage(observation, call.ID, schema.WithToolName(call.Function.Name)))
		}
	}
	return assistant.PlanningWorkerResult{}, assistant.ErrModelBudget
}

type planningCatalogInput struct {
	Query, Region, Currency string
	Limit                   int
}

type planningScoreInput struct {
	PriceMinor *int64   `json:"priceMinor"`
	Region     string   `json:"region"`
	Platforms  []string `json:"platforms"`
}

func (a *PlanningAgent) tools(task entity.PlanningTask) (map[string]tool.InvokableTool, error) {
	allowed := make(map[string]bool, len(task.AllowedTools))
	for _, name := range task.AllowedTools {
		allowed[name] = true
	}
	result := make(map[string]tool.InvokableTool)
	if allowed["read_catalog"] {
		item, err := toolutils.InferTool("read_catalog", "Read current catalog, edition, price and ownership facts.", func(ctx context.Context, input planningCatalogInput) (catalog.ListResult, error) {
			limit := input.Limit
			if limit == 0 {
				limit = 10
			}
			if limit > 10 {
				limit = 10
			}
			return a.catalog.List(ctx, catalog.ListInput{Query: strings.TrimSpace(input.Query), Region: input.Region, Currency: input.Currency, Limit: limit, ViewerID: task.UserID})
		})
		if err != nil {
			return nil, err
		}
		result["read_catalog"] = item
	}
	if allowed["read_entitlements"] {
		item, err := toolutils.InferTool("read_entitlements", "Read owned game and edition IDs from the current catalog view.", func(ctx context.Context, input planningCatalogInput) (map[string][]string, error) {
			value, err := a.catalog.List(ctx, catalog.ListInput{Query: strings.TrimSpace(input.Query), Region: input.Region, Currency: input.Currency, Limit: 10, ViewerID: task.UserID})
			if err != nil {
				return nil, err
			}
			gameIDs, editionIDs := []string{}, []string{}
			for _, game := range value.Items {
				if !game.Owned {
					continue
				}
				gameIDs = append(gameIDs, strconv.FormatInt(game.ID, 10))
				detail, detailErr := a.catalog.Get(ctx, game.Slug, input.Region, input.Currency, task.UserID)
				if detailErr != nil {
					return nil, detailErr
				}
				for _, edition := range detail.Editions {
					if edition.Owned {
						editionIDs = append(editionIDs, strconv.FormatInt(edition.ID, 10))
					}
				}
			}
			return map[string][]string{"gameIds": gameIDs, "editionIds": editionIDs}, nil
		})
		if err != nil {
			return nil, err
		}
		result["read_entitlements"] = item
	}
	if allowed["score_constraints"] {
		item, err := toolutils.InferTool("score_constraints", "Deterministically evaluate candidate facts against trusted constraints.", func(_ context.Context, input planningScoreInput) (map[string]any, error) {
			matched, unmet := []string{}, []string{}
			if task.Constraints.MaxPriceMinor != nil && input.PriceMinor != nil {
				if *input.PriceMinor <= *task.Constraints.MaxPriceMinor {
					matched = append(matched, "max_price")
				} else {
					unmet = append(unmet, "max_price")
				}
			}
			if task.Constraints.Region != "" {
				if strings.EqualFold(input.Region, task.Constraints.Region) {
					matched = append(matched, "region")
				} else {
					unmet = append(unmet, "region")
				}
			}
			return map[string]any{"matched": matched, "unmet": unmet, "score": len(matched) - len(unmet)}, nil
		})
		if err != nil {
			return nil, err
		}
		result["score_constraints"] = item
	}
	if len(result) == 0 {
		return nil, entity.ErrInvalidAgentContract
	}
	return result, nil
}

func (a *PlanningAgent) revalidate(ctx context.Context, task entity.PlanningTask, artifact *entity.PlanningArtifact) error {
	for i := range artifact.Items {
		item := &artifact.Items[i]
		game, err := a.catalog.Get(ctx, strings.ToLower(item.SubjectID), task.Constraints.Region, task.Constraints.Currency, task.UserID)
		if err != nil {
			return entity.ErrInvalidAgentContract
		}
		item.SubjectID = game.Slug
		if game.Owned {
			item.MatchedConstraints = appendUnique(item.MatchedConstraints, "owned")
		}
		if task.Constraints.MaxPriceMinor != nil && !hasAffordableEdition(game, *task.Constraints.MaxPriceMinor) {
			item.UnmetConstraints = appendUnique(item.UnmetConstraints, "max_price")
		}
	}
	return nil
}

func hasAffordableEdition(game catalogentity.Game, maximum int64) bool {
	for _, edition := range game.Editions {
		for _, price := range edition.Prices {
			if price.AmountMinor <= maximum {
				return true
			}
		}
	}
	return false
}
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
func planningToolInfos(ctx context.Context, values map[string]tool.InvokableTool) ([]*schema.ToolInfo, error) {
	result := make([]*schema.ToolInfo, 0, len(values))
	for _, name := range []string{"read_catalog", "read_entitlements", "score_constraints"} {
		if item := values[name]; item != nil {
			info, err := item.Info(ctx)
			if err != nil {
				return nil, err
			}
			result = append(result, info)
		}
	}
	return result, nil
}

type planningPayload struct {
	Status     entity.ArtifactStatus `json:"status"`
	Items      []planningItem        `json:"items"`
	StopReason string                `json:"stopReason"`
}
type planningItem struct {
	SubjectID          string   `json:"subjectId"`
	Recommendation     string   `json:"recommendation"`
	MatchedConstraints []string `json:"matchedConstraints"`
	UnmetConstraints   []string `json:"unmetConstraints"`
	Assumptions        []string `json:"assumptions"`
	Alternatives       []string `json:"alternatives"`
	EvidenceIDs        []string `json:"evidenceIds"`
}

func parsePlanningArtifact(raw string, envelope entity.Envelope) (entity.PlanningArtifact, error) {
	if len(raw) == 0 || len(raw) > maxStructuredOutputBytes || utf8.RuneCountInString(raw) > maxStructuredOutputBytes || strings.TrimSpace(raw) != raw {
		return entity.PlanningArtifact{}, entity.ErrInvalidAgentContract
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var payload planningPayload
	if err := decoder.Decode(&payload); err != nil {
		return entity.PlanningArtifact{}, entity.ErrInvalidAgentContract
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return entity.PlanningArtifact{}, entity.ErrInvalidAgentContract
	}
	artifact := entity.PlanningArtifact{Envelope: envelope, Status: payload.Status, StopReason: payload.StopReason}
	for _, item := range payload.Items {
		artifact.Items = append(artifact.Items, entity.PlanItem{SubjectID: item.SubjectID, Recommendation: item.Recommendation, MatchedConstraints: item.MatchedConstraints, UnmetConstraints: item.UnmetConstraints, Assumptions: item.Assumptions, Alternatives: item.Alternatives, EvidenceIDs: item.EvidenceIDs})
	}
	return artifact, nil
}

var _ assistant.PlanningWorker = (*PlanningAgent)(nil)
