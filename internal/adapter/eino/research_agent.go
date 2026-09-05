package einoadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"

	assistantentity "github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistantuc "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

var (
	errToolCallLimit = errors.New("research tool call limit reached")
	errInvalidQuery  = errors.New("query is required")
)

const maxToolEvidenceRunes = 800

type ResearchLimits struct {
	TotalTimeout, ToolTimeout   time.Duration
	MaxIterations, MaxToolCalls int
}

type ResearchAgent struct {
	runner *adk.Runner
	limits ResearchLimits
}

type CatalogSearch interface {
	List(context.Context, catalog.ListInput) (catalog.ListResult, error)
}

type ForumSearch interface {
	ListPosts(context.Context, community.ListPostsInput) (community.PostPage, error)
}

type KnowledgeSearch interface {
	SearchEvidence(context.Context, string, string, string, string, int) ([]usecase.Evidence, error)
}

type ResearchCapabilities struct {
	Knowledge  KnowledgeSearch
	Catalog    CatalogSearch
	Forum      ForumSearch
	Web        *usecase.WebSearch
	WebEnabled bool
}

func NewResearchAgent(ctx context.Context, chatModel model.ToolCallingChatModel, prompt string, capabilities ResearchCapabilities, limits ResearchLimits) (*ResearchAgent, error) {
	if limits.TotalTimeout <= 0 || limits.ToolTimeout <= 0 || limits.MaxIterations <= 0 || limits.MaxToolCalls <= 0 {
		return nil, errors.New("research limits must be positive")
	}
	if capabilities.Knowledge == nil || capabilities.Catalog == nil || capabilities.Forum == nil || capabilities.WebEnabled && capabilities.Web == nil {
		return nil, errors.New("enabled research tools require a capability")
	}
	knowledgeTool, err := toolutils.InferTool("search_lightrag", "Search the managed game-guide knowledge base through the configured read-only provider.", func(ctx context.Context, input knowledgeQuery) (toolObservation, error) {
		query := strings.TrimSpace(input.Query)
		return runTool(ctx, "lightrag", func(toolCtx context.Context) ([]usecase.Evidence, error) {
			if query == "" {
				return nil, errInvalidQuery
			}
			mode := firstText(strings.TrimSpace(input.Mode), "mix")
			if state := researchState(ctx); state != nil && !state.allowsMode(mode) {
				return nil, errInvalidQuery
			}
			return capabilities.Knowledge.SearchEvidence(toolCtx, query, strings.TrimSpace(input.GameCode), strings.TrimSpace(input.RegionCode), mode, 5)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create knowledge tool: %w", err)
	}
	catalogTool, err := toolutils.InferTool("search_catalog", "Search the game catalog by name or slug. This tool is read-only.", func(ctx context.Context, input catalogQuery) (toolObservation, error) {
		query := strings.TrimSpace(input.Query)
		return runTool(ctx, "catalog", func(toolCtx context.Context) ([]usecase.Evidence, error) {
			if query == "" {
				return nil, errInvalidQuery
			}
			result, err := capabilities.Catalog.List(toolCtx, catalog.ListInput{Query: query, Region: input.Region, Currency: input.Currency, Limit: 5})
			if err != nil {
				return nil, err
			}
			evidence := make([]usecase.Evidence, 0, len(result.Items))
			for _, item := range result.Items {
				evidence = append(evidence, usecase.Evidence{Source: "catalog", Title: item.Name, Content: firstText(item.Summary, item.Name), URL: "/api/games/" + url.PathEscape(item.Slug)})
			}
			return evidence, nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create catalog tool: %w", err)
	}
	forumTool, err := toolutils.InferTool("search_forum", "Search published game-community posts. This tool is read-only.", func(ctx context.Context, input forumQuery) (toolObservation, error) {
		query := strings.TrimSpace(input.Query)
		return runTool(ctx, "forum", func(toolCtx context.Context) ([]usecase.Evidence, error) {
			if query == "" {
				return nil, errInvalidQuery
			}
			result, err := capabilities.Forum.ListPosts(toolCtx, community.ListPostsInput{GameID: input.GameID, Query: query, Limit: 5})
			if err != nil {
				return nil, err
			}
			evidence := make([]usecase.Evidence, 0, len(result.Items))
			for _, item := range result.Items {
				evidence = append(evidence, usecase.Evidence{Source: "forum", Title: item.Title, Content: item.Content, URL: fmt.Sprintf("/api/community/posts/%d", item.ID)})
			}
			return evidence, nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create forum tool: %w", err)
	}
	tools := []tool.BaseTool{knowledgeTool, catalogTool, forumTool}
	if capabilities.WebEnabled {
		webTool, err := toolutils.InferTool("search_web", "Search the public Web for time-sensitive game information. This tool is read-only.", func(ctx context.Context, input webQuery) (toolObservation, error) {
			query := strings.TrimSpace(input.Query)
			return runTool(ctx, "web", func(toolCtx context.Context) ([]usecase.Evidence, error) {
				if query == "" {
					return nil, errInvalidQuery
				}
				response, err := capabilities.Web.Run(toolCtx, query)
				if err != nil {
					return nil, err
				}
				evidence := make([]usecase.Evidence, 0, len(response.Items))
				for _, item := range response.Items {
					evidence = append(evidence, usecase.Evidence{Source: firstText(item.Source, "web"), Title: item.Title, Content: item.Snippet, URL: item.URL, Score: 50})
				}
				return evidence, nil
			})
		})
		if err != nil {
			return nil, fmt.Errorf("create web tool: %w", err)
		}
		tools = append(tools, webTool)
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "research",
		Description:   "Collects game evidence with read-only tools.",
		Instruction:   prompt,
		Model:         chatModel,
		MaxIterations: limits.MaxIterations,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			ExecuteSequentially: true,
		}},
		Middlewares: []adk.AgentMiddleware{{BeforeChatModel: func(ctx context.Context, _ *adk.ChatModelAgentState) error {
			if state := researchState(ctx); state != nil {
				if state.budget != nil {
					if err := state.budget.TakeModel(ctx); err != nil {
						return err
					}
				}
				slog.InfoContext(ctx, "research iteration", "event", "assistant.model", "run_id", state.runID, "skill_id", state.skillID, "agent_role", "research", "operation", "iterate", "iteration", state.addIteration())
			}
			return nil
		}}},
	})
	if err != nil {
		return nil, fmt.Errorf("create research agent: %w", err)
	}
	return &ResearchAgent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}), limits: limits}, nil
}

func (a *ResearchAgent) Research(ctx context.Context, decision usecase.RouteDecision) (usecase.ResearchResult, error) {
	return a.run(ctx, decision, nil, nil, nil, nil, "unavailable", "legacy")
}

func (a *ResearchAgent) RunResearch(ctx context.Context, task assistantentity.ResearchTask, plan assistantentity.QueryPlan, budget *assistantuc.Budget) (assistantuc.ResearchWorkerResult, error) {
	if budget == nil || task.Validate(plan) != nil {
		return assistantuc.ResearchWorkerResult{}, assistantentity.ErrInvalidAgentContract
	}
	if err := budget.BeginDelegation(ctx, "research"); err != nil {
		return assistantuc.ResearchWorkerResult{}, err
	}
	units := make(map[string]assistantentity.QueryUnit, len(plan.Units))
	for _, unit := range plan.Units {
		units[unit.ID] = unit
	}
	queries := make([]string, 0, len(task.QueryUnitIDs))
	modes := make(map[string]bool)
	for _, id := range task.QueryUnitIDs {
		unit := units[id]
		queries = append(queries, unit.Text)
		if unit.LightRAGMode != "" {
			modes[string(unit.LightRAGMode)] = true
		}
	}
	plannedTools := make(map[string]bool)
	for _, id := range task.QueryUnitIDs {
		for _, source := range units[id].Sources {
			plannedTools["search_"+string(source)] = true
		}
	}
	allowedTools := make(map[string]bool, len(task.AllowedTools))
	for _, name := range task.AllowedTools {
		allowedTools[name] = plannedTools[name]
	}
	legacyResult, err := a.run(ctx, usecase.RouteDecision{Route: usecase.RouteEvidence, ResponseMode: "qa", NeedLocalKnowledge: true, Queries: queries}, budget, allowedTools, modes, unitsForTask(task, units), task.RunID, task.SkillID)
	result := assistantuc.ResearchWorkerResult{Artifact: assistantentity.ResearchArtifact{Envelope: task.Envelope, Status: researchArtifactStatus(legacyResult.Status), Assumptions: append([]string(nil), legacyResult.Notes...), StopReason: researchStopReason(legacyResult.StopReason)}}
	for _, item := range legacyResult.Evidence {
		result.Evidence = append(result.Evidence, assistantentity.Evidence{Source: item.Source, Title: item.Title, Content: item.Content, URL: item.URL, Score: item.Score})
	}
	result.Artifact.CoveredFacets, result.Artifact.MissingFacets = researchFacetCoverage(task.RequiredFacets, result.Evidence)
	if len(result.Artifact.MissingFacets) > 0 && len(result.Evidence) > 0 && result.Artifact.Status == assistantentity.StatusComplete {
		result.Artifact.Status = assistantentity.StatusPartial
	}
	if len(result.Artifact.Assumptions) > 8 {
		result.Artifact.Assumptions = result.Artifact.Assumptions[:8]
	}
	return result, err
}

func researchFacetCoverage(required []string, evidence []assistantentity.Evidence) ([]string, []string) {
	combined := strings.Builder{}
	for _, item := range evidence {
		combined.WriteString(strings.ToLower(item.Title))
		combined.WriteByte(' ')
		combined.WriteString(strings.ToLower(item.Content))
		combined.WriteByte(' ')
	}
	haystack := combined.String()
	covered, missing := []string{}, []string{}
	for _, facet := range required {
		if strings.Contains(haystack, strings.ToLower(strings.TrimSpace(facet))) {
			covered = append(covered, facet)
		} else {
			missing = append(missing, facet)
		}
	}
	return covered, missing
}

func researchArtifactStatus(status usecase.ResearchStatus) assistantentity.ArtifactStatus {
	switch status {
	case usecase.ResearchComplete:
		return assistantentity.StatusComplete
	case usecase.ResearchPartial:
		return assistantentity.StatusPartial
	case usecase.ResearchBounded:
		return assistantentity.StatusBounded
	default:
		return assistantentity.StatusNoResult
	}
}

func researchStopReason(value string) string {
	switch value {
	case "model_error":
		return "invalid_output"
	case "":
		return "complete"
	default:
		return value
	}
}

func (a *ResearchAgent) run(ctx context.Context, decision usecase.RouteDecision, budget *assistantuc.Budget, allowedTools, allowedModes map[string]bool, queryUnits []assistantentity.QueryUnit, runID, skillID string) (result usecase.ResearchResult, runErr error) {
	started := time.Now()
	defer func() {
		usage := assistantentity.BudgetUsage{}
		if budget != nil {
			usage = budget.Usage()
		}
		outcome := "ok"
		if runErr != nil {
			outcome = "error"
		}
		assistantuc.LogRunEvent(ctx, "assistant.agent", runID, "research", "retrieve", outcome, firstText(result.StopReason, assistantuc.StopReason(runErr)), started, usage, slog.String("error_class", assistantuc.ErrorClass(runErr)), slog.String("skill_id", skillID), slog.Int("iterations", result.Iterations), slog.Int("local_tool_calls", result.ToolCalls), slog.Int("evidence_count", len(result.Evidence)), slog.Bool("degraded", result.Degraded))
	}()
	runCtx, cancel := context.WithTimeout(ctx, a.limits.TotalTimeout)
	defer cancel()
	state := &researchRun{maxTools: a.limits.MaxToolCalls, toolTimeout: a.limits.ToolTimeout, budget: budget, allowedTools: allowedTools, allowedModes: allowedModes, runID: runID, skillID: skillID}
	runCtx = context.WithValue(runCtx, researchRunKey{}, state)
	payload, _ := json.Marshal(struct {
		Queries            []string                    `json:"queries"`
		Notes              []string                    `json:"notes,omitempty"`
		PreferKnowledge    bool                        `json:"preferKnowledge"`
		AllowTimeSensitive bool                        `json:"allowTimeSensitiveWeb"`
		QueryUnits         []assistantentity.QueryUnit `json:"queryUnits,omitempty"`
		AllowedTools       []string                    `json:"allowedTools,omitempty"`
		AllowedModes       []string                    `json:"allowedLightRAGModes,omitempty"`
	}{decision.Queries, decision.Notes, decision.NeedLocalKnowledge, decision.NeedWeb, queryUnits, sortedKeys(allowedTools), sortedKeys(allowedModes)})

	var agentErr error
	iterator := a.runner.Query(runCtx, string(payload))
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			agentErr = event.Err
			break
		}
	}

	stopReason := "complete"
	switch {
	case errors.Is(agentErr, errToolCallLimit):
		stopReason = "max_tool_calls"
	case errors.Is(agentErr, adk.ErrExceedMaxIterations):
		stopReason = "max_iterations"
	case ctx.Err() != nil:
		stopReason = "cancelled"
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		stopReason = "deadline"
	case agentErr != nil:
		stopReason = "model_error"
	}
	result = state.result(stopReason)

	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return result, context.DeadlineExceeded
	}
	if errors.Is(agentErr, errToolCallLimit) || errors.Is(agentErr, adk.ErrExceedMaxIterations) {
		result.Status = usecase.ResearchBounded
		result.Degraded = true
		result.Notes = append(result.Notes, "检索已达到安全预算，以下结果可能不完整。")
		if len(result.Evidence) > 0 {
			return result, nil
		}
		return result, fmt.Errorf("%w: %s", usecase.ErrResearchBudgetExceeded, stopReason)
	}
	if agentErr != nil {
		if len(result.Evidence) > 0 {
			result.Status = usecase.ResearchPartial
			result.Degraded = true
			result.Notes = append(result.Notes, "研究模型提前停止，以下结果可能不完整。")
			return result, nil
		}
		return result, agentErr
	}
	if state.allFailed() {
		return result, usecase.ErrAllResearchToolsFailed
	}
	return result, nil
}

type knowledgeQuery struct {
	Query      string `json:"query"`
	GameCode   string `json:"gameCode,omitempty"`
	RegionCode string `json:"regionCode,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

type LegacyKnowledgeSearch struct{ Knowledge *usecase.Knowledge }

func (s LegacyKnowledgeSearch) SearchEvidence(ctx context.Context, query, gameCode, regionCode, _ string, limit int) ([]usecase.Evidence, error) {
	items, err := s.Knowledge.Search(ctx, query, gameCode, regionCode, limit)
	if err != nil {
		return nil, err
	}
	evidence := make([]usecase.Evidence, 0, len(items))
	for _, item := range items {
		evidence = append(evidence, usecase.Evidence{Source: "legacy_local", Title: item.Title, Content: item.Text, URL: item.SourceURL, Score: float64(item.Score)})
	}
	return evidence, nil
}

type webQuery struct {
	Query string `json:"query"`
}

type catalogQuery struct {
	Query    string `json:"query"`
	Region   string `json:"region,omitempty"`
	Currency string `json:"currency,omitempty"`
}

type forumQuery struct {
	Query  string `json:"query"`
	GameID int64  `json:"gameId,omitempty"`
}

type toolEvidence struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	Content string `json:"content"`
	URL     string `json:"url,omitempty"`
}

type toolObservation struct {
	Status   string         `json:"status"`
	Provider string         `json:"provider"`
	Note     string         `json:"note,omitempty"`
	Evidence []toolEvidence `json:"evidence,omitempty"`
}

type researchRunKey struct{}

type researchRun struct {
	mu                          sync.Mutex
	maxTools, iterations, calls int
	successes, failures         int
	toolTimeout                 time.Duration
	evidence                    []usecase.Evidence
	budget                      *assistantuc.Budget
	allowedTools                map[string]bool
	allowedModes                map[string]bool
	runID, skillID              string
}

func researchState(ctx context.Context) *researchRun {
	state, _ := ctx.Value(researchRunKey{}).(*researchRun)
	return state
}

func runTool(ctx context.Context, provider string, fn func(context.Context) ([]usecase.Evidence, error)) (toolObservation, error) {
	state := researchState(ctx)
	if state == nil {
		return toolObservation{}, errors.New("research run state is missing")
	}
	return state.runTool(ctx, provider, fn)
}

func (r *researchRun) runTool(ctx context.Context, provider string, fn func(context.Context) ([]usecase.Evidence, error)) (toolObservation, error) {
	started := time.Now()
	if r.allowedTools != nil && !r.allowedTools["search_"+provider] {
		return toolObservation{}, fmt.Errorf("tool search_%s is not allowed", provider)
	}
	r.mu.Lock()
	if r.calls >= r.maxTools {
		r.mu.Unlock()
		r.logTool(ctx, provider, r.maxTools+1, "bounded", started)
		return toolObservation{}, errToolCallLimit
	}
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if r.budget != nil {
		if err := r.budget.TakeTool(ctx); err != nil {
			r.logTool(ctx, provider, call, "bounded", started)
			return toolObservation{}, err
		}
	}

	toolCtx, cancel := context.WithTimeout(ctx, r.toolTimeout)
	defer cancel()
	evidence, err := fn(toolCtx)
	if err != nil {
		if ctx.Err() != nil {
			r.logTool(ctx, provider, call, "cancelled", started)
			return toolObservation{}, ctx.Err()
		}
		if errors.Is(err, errInvalidQuery) || errors.Is(err, usecase.ErrInvalidSearchQuery) || errors.Is(err, catalog.ErrInvalidInput) || errors.Is(err, community.ErrInvalidInput) {
			r.logTool(ctx, provider, call, "invalid", started)
			return toolObservation{Status: "invalid", Provider: provider, Note: "tool input is invalid"}, nil
		}
		r.mu.Lock()
		r.failures++
		r.mu.Unlock()
		r.logTool(ctx, provider, call, "failed", started)
		return toolObservation{Status: "failed", Provider: provider, Note: "provider temporarily unavailable"}, nil
	}
	for i := range evidence {
		if content := []rune(evidence[i].Content); len(content) > maxToolEvidenceRunes {
			evidence[i].Content = string(content[:maxToolEvidenceRunes-1]) + "…"
		}
	}

	r.mu.Lock()
	r.successes++
	r.evidence = append(r.evidence, evidence...)
	r.mu.Unlock()
	observation := toolObservation{Status: "ok", Provider: provider, Evidence: make([]toolEvidence, 0, len(evidence))}
	if len(evidence) == 0 {
		observation.Status = "no_result"
		observation.Note = "no matching evidence"
	}
	r.logTool(ctx, provider, call, observation.Status, started)
	for _, item := range evidence {
		observation.Evidence = append(observation.Evidence, toolEvidence{Source: item.Source, Title: item.Title, Content: item.Content, URL: item.URL})
	}
	return observation, nil
}

func (r *researchRun) allowsMode(mode string) bool {
	return r.allowedModes == nil || r.allowedModes[mode]
}

func unitsForTask(task assistantentity.ResearchTask, values map[string]assistantentity.QueryUnit) []assistantentity.QueryUnit {
	result := make([]assistantentity.QueryUnit, 0, len(task.QueryUnitIDs))
	for _, id := range task.QueryUnitIDs {
		result = append(result, values[id])
	}
	return result
}

func sortedKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for key, enabled := range values {
		if enabled {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func (r *researchRun) addIteration() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.iterations++
	return r.iterations
}

func (r *researchRun) allFailed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures > 0 && r.successes == 0
}

func (r *researchRun) logTool(ctx context.Context, provider string, call int, result string, started time.Time) {
	slog.InfoContext(ctx, "research tool completed", "event", "assistant.tool", "run_id", r.runID, "skill_id", r.skillID, "agent_role", "research", "tool", "search_"+provider, "provider", provider, "tool_call", call, "outcome", result, "latency_ms", time.Since(started).Milliseconds())
	assistantuc.RecordAssistantEvent("assistant.tool", "research", "search_"+provider, result, "complete", "research", r.skillID, started)
}

func (r *researchRun) result(stopReason string) usecase.ResearchResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := usecase.ResearchResult{Evidence: uniqueEvidence(r.evidence, 8), Status: usecase.ResearchComplete, StopReason: stopReason, Iterations: r.iterations, ToolCalls: r.calls}
	if len(result.Evidence) == 0 {
		result.Status = usecase.ResearchNoResult
		result.Notes = append(result.Notes, "未检索到可用证据。")
	}
	if r.failures > 0 && r.successes > 0 {
		result.Status = usecase.ResearchPartial
		result.Degraded = true
		result.Notes = append(result.Notes, fmt.Sprintf("有 %d 个检索调用暂时不可用，答案可能不完整。", r.failures))
	}
	return result
}

func uniqueEvidence(values []usecase.Evidence, limit int) []usecase.Evidence {
	seen := make(map[string]bool, len(values))
	result := make([]usecase.Evidence, 0, min(len(values), limit))
	for _, item := range values {
		key := strings.ToLower(item.Source + "\x00" + firstText(item.URL, item.Title+"\x00"+item.Content))
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result
}

var _ usecase.ResearchAgent = (*ResearchAgent)(nil)
