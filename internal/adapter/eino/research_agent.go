package einoadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

var (
	errToolCallLimit = errors.New("research tool call limit reached")
	errInvalidQuery  = errors.New("query is required")
)

type ResearchLimits struct {
	TotalTimeout, ToolTimeout   time.Duration
	MaxIterations, MaxToolCalls int
}

type ResearchAgent struct {
	runner *adk.Runner
	limits ResearchLimits
}

func NewResearchAgent(ctx context.Context, chatModel model.ToolCallingChatModel, prompt string, knowledge *usecase.Knowledge, web *usecase.WebSearch, webEnabled bool, limits ResearchLimits) (*ResearchAgent, error) {
	if limits.TotalTimeout <= 0 || limits.ToolTimeout <= 0 || limits.MaxIterations <= 0 || limits.MaxToolCalls <= 0 {
		return nil, errors.New("research limits must be positive")
	}
	if knowledge == nil || webEnabled && web == nil {
		return nil, errors.New("enabled research tools require a capability")
	}
	knowledgeTool, err := toolutils.InferTool("search_knowledge", "Search the local game-guide knowledge base. This tool is read-only.", func(ctx context.Context, input knowledgeQuery) (toolObservation, error) {
		query := strings.TrimSpace(input.Query)
		return runTool(ctx, "knowledge", func(toolCtx context.Context) ([]usecase.Evidence, error) {
			if query == "" {
				return nil, errInvalidQuery
			}
			items, err := knowledge.Search(toolCtx, query, strings.TrimSpace(input.GameCode), strings.TrimSpace(input.RegionCode), 5)
			if err != nil {
				return nil, err
			}
			evidence := make([]usecase.Evidence, 0, len(items))
			for _, item := range items {
				evidence = append(evidence, usecase.Evidence{Source: "knowledge", Title: item.Title, Content: item.Text, URL: item.SourceURL, Score: float64(item.Score)})
			}
			return evidence, nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create knowledge tool: %w", err)
	}
	tools := []tool.BaseTool{knowledgeTool}
	if webEnabled {
		webTool, err := toolutils.InferTool("search_web", "Search the public Web for time-sensitive game information. This tool is read-only.", func(ctx context.Context, input webQuery) (toolObservation, error) {
			query := strings.TrimSpace(input.Query)
			return runTool(ctx, "web", func(toolCtx context.Context) ([]usecase.Evidence, error) {
				if query == "" {
					return nil, errInvalidQuery
				}
				response, err := web.Run(toolCtx, query)
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
				slog.InfoContext(ctx, "research iteration", "iteration", state.addIteration())
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
	runCtx, cancel := context.WithTimeout(ctx, a.limits.TotalTimeout)
	defer cancel()
	state := &researchRun{maxTools: a.limits.MaxToolCalls, toolTimeout: a.limits.ToolTimeout}
	runCtx = context.WithValue(runCtx, researchRunKey{}, state)
	payload, _ := json.Marshal(struct {
		Queries            []string `json:"queries"`
		Notes              []string `json:"notes,omitempty"`
		PreferKnowledge    bool     `json:"preferKnowledge"`
		AllowTimeSensitive bool     `json:"allowTimeSensitiveWeb"`
	}{decision.Queries, decision.Notes, decision.NeedLocalKnowledge, decision.NeedWeb})

	var runErr error
	iterator := a.runner.Query(runCtx, string(payload))
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			runErr = event.Err
			break
		}
	}

	stopReason := "complete"
	switch {
	case errors.Is(runErr, errToolCallLimit):
		stopReason = "max_tool_calls"
	case errors.Is(runErr, adk.ErrExceedMaxIterations):
		stopReason = "max_iterations"
	case ctx.Err() != nil:
		stopReason = "cancelled"
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		stopReason = "deadline"
	case runErr != nil:
		stopReason = "model_error"
	}
	result := state.result(stopReason)
	slog.InfoContext(ctx, "research finished", "status", result.Status, "stop_reason", result.StopReason, "iterations", result.Iterations, "tool_calls", result.ToolCalls)

	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return result, context.DeadlineExceeded
	}
	if errors.Is(runErr, errToolCallLimit) || errors.Is(runErr, adk.ErrExceedMaxIterations) {
		result.Status = usecase.ResearchBounded
		result.Degraded = true
		result.Notes = append(result.Notes, "检索已达到安全预算，以下结果可能不完整。")
		if len(result.Evidence) > 0 {
			return result, nil
		}
		return result, fmt.Errorf("%w: %s", usecase.ErrResearchBudgetExceeded, stopReason)
	}
	if runErr != nil {
		if len(result.Evidence) > 0 {
			result.Status = usecase.ResearchPartial
			result.Degraded = true
			result.Notes = append(result.Notes, "研究模型提前停止，以下结果可能不完整。")
			return result, nil
		}
		return result, runErr
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
}

type webQuery struct {
	Query string `json:"query"`
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
	r.mu.Lock()
	if r.calls >= r.maxTools {
		r.mu.Unlock()
		logTool(ctx, provider, r.maxTools+1, "bounded", started)
		return toolObservation{}, errToolCallLimit
	}
	r.calls++
	call := r.calls
	r.mu.Unlock()

	toolCtx, cancel := context.WithTimeout(ctx, r.toolTimeout)
	defer cancel()
	evidence, err := fn(toolCtx)
	if err != nil {
		if ctx.Err() != nil {
			logTool(ctx, provider, call, "cancelled", started)
			return toolObservation{}, ctx.Err()
		}
		if errors.Is(err, errInvalidQuery) {
			logTool(ctx, provider, call, "invalid", started)
			return toolObservation{Status: "invalid", Provider: provider, Note: "query is required"}, nil
		}
		r.mu.Lock()
		r.failures++
		r.mu.Unlock()
		logTool(ctx, provider, call, "failed", started)
		return toolObservation{Status: "failed", Provider: provider, Note: "provider temporarily unavailable"}, nil
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
	logTool(ctx, provider, call, observation.Status, started)
	for _, item := range evidence {
		observation.Evidence = append(observation.Evidence, toolEvidence{Source: item.Source, Title: item.Title, Content: item.Content, URL: item.URL})
	}
	return observation, nil
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

func logTool(ctx context.Context, provider string, call int, result string, started time.Time) {
	slog.InfoContext(ctx, "research tool completed", "tool", "search_"+provider, "provider", provider, "tool_call", call, "result", result, "latency_ms", time.Since(started).Milliseconds())
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
