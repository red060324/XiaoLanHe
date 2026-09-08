package einoadapter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	assistantentity "github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistantuc "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	communityentity "github.com/red060324/XiaoLanHe/internal/community/entity"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	knowledgeentity "github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestResearchAgentTypedTaskBindsQueryUnitAndBudget(t *testing.T) {
	knowledge := &modeKnowledgeStore{}
	model := scriptedResearchModel(
		researchToolCall("1", "search_lightrag", `{"queryUnitId":"q1"}`),
		schema.AssistantMessage("done", nil),
	)
	agent, err := NewResearchAgent(context.Background(), model, "research", ResearchCapabilities{Knowledge: knowledge, Catalog: &researchCatalog{}, Forum: &researchForum{}}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	task := assistantentity.ResearchTask{Envelope: assistantentity.Envelope{SchemaVersion: 1, RunID: "12345678-1234-4123-8123-123456789abc", Sequence: 1, SkillID: "research_guide", SkillVersion: "1.0.0"}, Objective: "research rpg", QueryUnitIDs: []string{"q1"}, RequiredFacets: []string{"genre"}, AllowedTools: []string{"search_lightrag"}}
	plan := assistantentity.QueryPlan{SchemaVersion: 1, Units: []assistantentity.QueryUnit{{ID: "q1", Text: "planned rpg query", Sources: []assistantentity.QuerySource{assistantentity.SourceLightRAG}, LightRAGMode: assistantentity.LightRAGHybrid, Freshness: "stable", Filters: assistantentity.QueryFilters{GameCode: "game-rpg", Region: "CN", Platforms: []string{"pc"}}, RequiredFacets: []string{"genre"}}}}
	budget, _ := assistantuc.NewBudget(assistantentity.BudgetLimit{ModelCalls: 6, ToolCalls: 8, Delegations: 1, TimeoutMilliseconds: 2000})
	result, err := agent.RunResearch(context.Background(), task, plan, budget)
	if err != nil || len(result.Evidence) != 1 || knowledge.calls != 1 || knowledge.query != "planned rpg query" || knowledge.gameCode != "game-rpg" || knowledge.regionCode != "CN" || knowledge.mode != "hybrid" || result.Artifact.Status != assistantentity.StatusComplete || !slices.Equal(result.Artifact.CoveredFacets, []string{"genre"}) || budget.Usage().ToolCalls != 1 {
		t.Fatalf("result=%+v knowledge=%+v usage=%+v err=%v", result, knowledge, budget.Usage(), err)
	}
}

func TestResearchAgentTypedTaskUsesOnlySupportedProviderFilters(t *testing.T) {
	catalogSearch := &researchCatalog{result: catalog.ListResult{Items: []catalogentity.Game{{Slug: "catalog-game", Name: "Catalog Game"}}}}
	forumSearch := &researchForum{result: community.PostPage{Items: []communityentity.Post{{ID: 7, Title: "Forum post", Content: "fact"}}}}
	var webQuery string
	web := usecase.NewWebSearch(&researchWebClient{search: func(_ context.Context, query string) (usecase.WebSearchResult, error) {
		webQuery = query
		return usecase.WebSearchResult{Items: []usecase.WebSearchItem{{Title: "Web result", URL: "https://example.com", Snippet: "fact"}}}, nil
	}})
	model := scriptedResearchModel(
		schema.AssistantMessage("", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "search_catalog", Arguments: `{"queryUnitId":"catalog-unit"}`}},
			{ID: "2", Function: schema.FunctionCall{Name: "search_forum", Arguments: `{"queryUnitId":"forum-unit"}`}},
			{ID: "3", Function: schema.FunctionCall{Name: "search_web", Arguments: `{"queryUnitId":"web-unit"}`}},
		}),
		schema.AssistantMessage("done", nil),
	)
	agent, err := NewResearchAgent(context.Background(), model, "research", ResearchCapabilities{Knowledge: &researchKnowledgeStore{}, Catalog: catalogSearch, Forum: forumSearch, Web: web, WebEnabled: true}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	filters := assistantentity.QueryFilters{GameCode: "game-rpg", Region: "CN", Platforms: []string{"pc"}}
	task := assistantentity.ResearchTask{Envelope: assistantentity.Envelope{SchemaVersion: 1, RunID: "12345678-1234-4123-8123-123456789abc", Sequence: 1, SkillID: "research_guide", SkillVersion: "1.0.0"}, Objective: "research providers", QueryUnitIDs: []string{"catalog-unit", "forum-unit", "web-unit"}, AllowedTools: []string{"search_catalog", "search_forum", "search_web"}}
	plan := assistantentity.QueryPlan{SchemaVersion: 1, Units: []assistantentity.QueryUnit{
		{ID: "catalog-unit", Text: "catalog exact text", Sources: []assistantentity.QuerySource{assistantentity.SourceCatalog}, Freshness: "stable", Filters: filters},
		{ID: "forum-unit", Text: "forum exact text", Sources: []assistantentity.QuerySource{assistantentity.SourceForum}, Freshness: "stable", Filters: filters},
		{ID: "web-unit", Text: "web exact text", Sources: []assistantentity.QuerySource{assistantentity.SourceWeb}, Freshness: "recent", Filters: filters},
	}}
	budget, _ := assistantuc.NewBudget(assistantentity.BudgetLimit{ModelCalls: 6, ToolCalls: 8, Delegations: 1, TimeoutMilliseconds: 2000})

	result, runErr := agent.RunResearch(context.Background(), task, plan, budget)

	if runErr != nil || len(result.Evidence) != 3 || catalogSearch.input.Query != "catalog exact text" || catalogSearch.input.Region != "CN" || forumSearch.input.Query != "forum exact text" || forumSearch.input.GameID != 0 || webQuery != "web exact text" {
		t.Fatalf("result=%+v catalog=%+v forum=%+v webQuery=%q err=%v", result, catalogSearch.input, forumSearch.input, webQuery, runErr)
	}
}

func TestResearchAgentTypedTaskRejectsQueryUnitTampering(t *testing.T) {
	tests := []struct {
		name, tool, arguments string
	}{
		{name: "missing unit id", tool: "search_lightrag", arguments: `{"query":"planned rpg query"}`},
		{name: "unknown unit id", tool: "search_lightrag", arguments: `{"queryUnitId":"q3"}`},
		{name: "provider from another unit", tool: "search_catalog", arguments: `{"queryUnitId":"q1"}`},
		{name: "query rewrite", tool: "search_lightrag", arguments: `{"queryUnitId":"q1","query":"different query"}`},
		{name: "game filter rewrite", tool: "search_lightrag", arguments: `{"queryUnitId":"q1","gameCode":"other-game"}`},
		{name: "region filter rewrite", tool: "search_lightrag", arguments: `{"queryUnitId":"q1","regionCode":"US"}`},
		{name: "platform filter rewrite", tool: "search_lightrag", arguments: `{"queryUnitId":"q1","platform":"xbox"}`},
		{name: "mode from another unit", tool: "search_lightrag", arguments: `{"queryUnitId":"q1","mode":"global"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledge := &modeKnowledgeStore{}
			catalogSearch := &researchCatalog{}
			model := scriptedResearchModel(researchToolCall("1", test.tool, test.arguments), schema.AssistantMessage("done", nil))
			agent, err := NewResearchAgent(context.Background(), model, "research", ResearchCapabilities{Knowledge: knowledge, Catalog: catalogSearch, Forum: &researchForum{}}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
			if err != nil {
				t.Fatal(err)
			}
			task := assistantentity.ResearchTask{Envelope: assistantentity.Envelope{SchemaVersion: 1, RunID: "12345678-1234-4123-8123-123456789abc", Sequence: 1, SkillID: "research_guide", SkillVersion: "1.0.0"}, Objective: "research rpg", QueryUnitIDs: []string{"q1", "q2"}, AllowedTools: []string{"search_lightrag", "search_catalog"}}
			plan := assistantentity.QueryPlan{SchemaVersion: 1, Units: []assistantentity.QueryUnit{
				{ID: "q1", Text: "planned rpg query", Sources: []assistantentity.QuerySource{assistantentity.SourceLightRAG}, LightRAGMode: assistantentity.LightRAGHybrid, Freshness: "stable", Filters: assistantentity.QueryFilters{GameCode: "game-rpg", Region: "CN", Platforms: []string{"pc"}}},
				{ID: "q2", Text: "catalog query", Sources: []assistantentity.QuerySource{assistantentity.SourceCatalog, assistantentity.SourceLightRAG}, LightRAGMode: assistantentity.LightRAGGlobal, Freshness: "stable"},
			}}
			budget, _ := assistantuc.NewBudget(assistantentity.BudgetLimit{ModelCalls: 6, ToolCalls: 8, Delegations: 1, TimeoutMilliseconds: 2000})

			result, runErr := agent.RunResearch(context.Background(), task, plan, budget)

			if runErr != nil || knowledge.calls != 0 || catalogSearch.input.Query != "" || len(result.Evidence) != 0 || result.Artifact.Status != assistantentity.StatusNoResult || budget.Usage().ToolCalls != 1 {
				t.Fatalf("result=%+v knowledge=%+v catalog=%+v usage=%+v err=%v", result, knowledge, catalogSearch.input, budget.Usage(), runErr)
			}
		})
	}
}

func TestResearchAgentTypedTaskFailsClosedOnSharedBudgetWithEvidence(t *testing.T) {
	tests := []struct {
		name       string
		modelCalls int
		toolCalls  int
		responses  []*schema.Message
		wantErr    error
		wantStop   string
		wantTools  int
	}{
		{
			name:       "model budget",
			modelCalls: 1,
			toolCalls:  8,
			responses:  []*schema.Message{researchToolCall("1", "search_lightrag", `{"queryUnitId":"q1"}`)},
			wantErr:    assistantuc.ErrModelBudget,
			wantStop:   "max_model_calls",
			wantTools:  1,
		},
		{
			name:       "tool budget",
			modelCalls: 6,
			toolCalls:  1,
			responses: []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{
				{ID: "1", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"queryUnitId":"q1"}`}},
				{ID: "2", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"queryUnitId":"q1"}`}},
			})},
			wantErr:   assistantuc.ErrToolBudget,
			wantStop:  "max_tool_calls",
			wantTools: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &researchKnowledgeStore{search: func(context.Context, string) ([]usecase.Evidence, error) {
				return []usecase.Evidence{{Source: "lightrag", Title: "RPG genre", Content: "genre fact"}}, nil
			}}
			agent, err := NewResearchAgent(context.Background(), scriptedResearchModel(test.responses...), "research", ResearchCapabilities{Knowledge: store, Catalog: &researchCatalog{}, Forum: &researchForum{}}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
			if err != nil {
				t.Fatal(err)
			}
			task, plan := typedResearchTaskAndPlan()
			budget, err := assistantuc.NewBudget(assistantentity.BudgetLimit{ModelCalls: test.modelCalls, ToolCalls: test.toolCalls, Delegations: 1, TimeoutMilliseconds: 2000})
			if err != nil {
				t.Fatal(err)
			}

			result, runErr := agent.RunResearch(context.Background(), task, plan, budget)

			if !errors.Is(runErr, test.wantErr) || result.Artifact.Status != assistantentity.StatusBounded || result.Artifact.StopReason != test.wantStop || len(result.Evidence) != 1 || len(store.queries()) != test.wantTools {
				t.Fatalf("result=%+v queries=%v usage=%+v err=%v", result, store.queries(), budget.Usage(), runErr)
			}
		})
	}
}

func TestResearchAgentProviderTerminalErrorsUseRealToolPath(t *testing.T) {
	tests := []struct {
		name, wantStop string
		providerErr    error
		wantIs         error
		wantTimeout    bool
	}{
		{name: "cancelled", providerErr: context.Canceled, wantIs: context.Canceled, wantStop: "cancelled"},
		{name: "deadline", providerErr: context.DeadlineExceeded, wantIs: context.DeadlineExceeded, wantStop: "deadline"},
		{name: "provider timeout", providerErr: researchProviderTimeoutError{}, wantStop: "deadline", wantTimeout: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &researchKnowledgeStore{search: func(context.Context, string) ([]usecase.Evidence, error) {
				return nil, test.providerErr
			}}
			agent := newTestResearchAgent(t, scriptedResearchModel(researchToolCall("1", "search_lightrag", `{"query":"guide"}`)), store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

			result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("err=%v want errors.Is(_, %v)", err, test.wantIs)
			}
			if test.wantTimeout {
				var timeout interface{ Timeout() bool }
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("err=%v does not preserve timeout error", err)
				}
			}
			if err == nil || result.StopReason != test.wantStop || result.Degraded || len(result.Evidence) != 0 {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestResearchAgentErrorClassification(t *testing.T) {
	tests := []struct {
		name, stop string
		err        error
		fails      bool
	}{
		{name: "model budget", err: assistantuc.ErrModelBudget, stop: "max_model_calls", fails: true},
		{name: "tool budget", err: assistantuc.ErrToolBudget, stop: "max_tool_calls", fails: true},
		{name: "delegation budget", err: assistantuc.ErrDelegationBudget, stop: "max_delegations", fails: true},
		{name: "research budget", err: usecase.ErrResearchBudgetExceeded, stop: "max_iterations", fails: true},
		{name: "iteration budget", err: adk.ErrExceedMaxIterations, stop: "max_iterations", fails: true},
		{name: "ordinary model error", err: errors.New("model unavailable"), stop: "model_error", fails: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := researchAgentErrorStopReason(test.err); got != test.stop || researchAgentErrorFailsClosed(test.err) != test.fails {
				t.Fatalf("stop=%q failsClosed=%v", got, researchAgentErrorFailsClosed(test.err))
			}
		})
	}
}

func TestResearchAgentResearch(t *testing.T) {
	t.Run("observes evidence and refines the query", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(_ context.Context, query string) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "lightrag", Title: query, Content: query + " fact"}}, nil
		}}
		var modelCalls atomic.Int32
		model := &fakeChatModel{generateContext: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
			switch modelCalls.Add(1) {
			case 1:
				return researchToolCall("1", "search_lightrag", `{"query":"first"}`), nil
			case 2:
				if !messagesContain(messages, "first fact") {
					return nil, errors.New("tool observation was not returned to the model")
				}
				return researchToolCall("2", "search_lightrag", `{"query":"refined"}`), nil
			default:
				return schema.AssistantMessage("done", nil), nil
			}
		}}
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if err != nil || !slices.Equal(store.queries(), []string{"first", "refined"}) || len(result.Evidence) != 2 || result.ToolCalls != 2 || result.Iterations != 3 || result.Status != usecase.ResearchComplete {
			t.Fatalf("result=%#v queries=%v err=%v", result, store.queries(), err)
		}
	})

	t.Run("keeps evidence and reports a partial provider failure", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(context.Context, string) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "lightrag", Title: "guide", Content: "fact"}}, nil
		}}
		web := &researchWebClient{search: func(context.Context, string) (usecase.WebSearchResult, error) {
			return usecase.WebSearchResult{}, errors.New("web unavailable")
		}}
		model := scriptedResearchModel(
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "1", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"guide"}`}},
				{ID: "2", Function: schema.FunctionCall{Name: "search_web", Arguments: `{"query":"latest"}`}},
			}),
			schema.AssistantMessage("done", nil),
		)
		agent := newTestResearchAgent(t, model, store, web, true, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true, NeedWeb: true})

		if err != nil || result.Status != usecase.ResearchPartial || !result.Degraded || len(result.Evidence) != 1 || len(result.Notes) == 0 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("distinguishes all providers failing", func(t *testing.T) {
		web := &researchWebClient{search: func(context.Context, string) (usecase.WebSearchResult, error) {
			return usecase.WebSearchResult{}, errors.New("web unavailable")
		}}
		model := scriptedResearchModel(researchToolCall("1", "search_web", `{"query":"latest"}`), schema.AssistantMessage("done", nil))
		agent := newTestResearchAgent(t, model, &researchKnowledgeStore{}, web, true, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedWeb: true})

		if !errors.Is(err, usecase.ErrAllResearchToolsFailed) || len(result.Evidence) != 0 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("does not report all providers failed when a planned provider was not attempted", func(t *testing.T) {
		web := &researchWebClient{search: func(context.Context, string) (usecase.WebSearchResult, error) {
			return usecase.WebSearchResult{}, errors.New("web unavailable")
		}}
		model := scriptedResearchModel(researchToolCall("1", "search_web", `{"query":"latest"}`), schema.AssistantMessage("done", nil))
		agent := newTestResearchAgent(t, model, &researchKnowledgeStore{}, web, true, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true, NeedWeb: true})

		if err != nil || result.Status != usecase.ResearchNoResult || len(result.Evidence) != 0 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("distinguishes a successful empty search", func(t *testing.T) {
		model := scriptedResearchModel(researchToolCall("1", "search_lightrag", `{"query":"missing"}`), schema.AssistantMessage("done", nil))
		agent := newTestResearchAgent(t, model, &researchKnowledgeStore{}, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if err != nil || result.Status != usecase.ResearchNoResult || result.Degraded || len(result.Evidence) != 0 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("fails closed at the tool call budget with partial evidence", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(_ context.Context, query string) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "lightrag", Title: query, Content: "fact"}}, nil
		}}
		model := scriptedResearchModel(schema.AssistantMessage("", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"one"}`}},
			{ID: "2", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"two"}`}},
			{ID: "3", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"three"}`}},
		}))
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 2})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if !errors.Is(err, usecase.ErrResearchBudgetExceeded) || result.Status != usecase.ResearchBounded || result.StopReason != "max_tool_calls" || result.ToolCalls != 2 || len(result.Evidence) != 2 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("fails closed at the model iteration budget with partial evidence", func(t *testing.T) {
		var calls atomic.Int32
		model := &fakeChatModel{generateContext: func(context.Context, []*schema.Message) (*schema.Message, error) {
			call := calls.Add(1)
			return researchToolCall(fmt.Sprint(call), "search_lightrag", fmt.Sprintf(`{"query":"q%d"}`, call)), nil
		}}
		store := &researchKnowledgeStore{search: func(_ context.Context, query string) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "lightrag", Title: query, Content: "fact"}}, nil
		}}
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 2, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if !errors.Is(err, usecase.ErrResearchBudgetExceeded) || result.Status != usecase.ResearchBounded || result.StopReason != "max_iterations" || result.Iterations != 2 || result.ToolCalls != 2 || len(result.Evidence) != 2 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("fails closed with prior evidence after an ordinary model error", func(t *testing.T) {
		modelErr := errors.New("model unavailable")
		var calls atomic.Int32
		model := &fakeChatModel{generateContext: func(context.Context, []*schema.Message) (*schema.Message, error) {
			if calls.Add(1) == 1 {
				return researchToolCall("1", "search_lightrag", `{"query":"guide"}`), nil
			}
			return nil, modelErr
		}}
		store := &researchKnowledgeStore{search: func(context.Context, string) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "lightrag", Content: "fact"}}, nil
		}}
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if !errors.Is(err, modelErr) || result.Status != usecase.ResearchPartial || !result.Degraded || result.StopReason != "model_error" || len(result.Evidence) != 1 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("propagates request cancellation into a tool", func(t *testing.T) {
		started := make(chan struct{})
		var once sync.Once
		store := &researchKnowledgeStore{search: func(ctx context.Context, _ string) ([]usecase.Evidence, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		model := scriptedResearchModel(researchToolCall("1", "search_lightrag", `{"query":"slow"}`))
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-started
			cancel()
		}()

		_, err := agent.Research(ctx, usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("propagates a tool-local deadline without evidence", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(ctx context.Context, _ string) ([]usecase.Evidence, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		model := scriptedResearchModel(researchToolCall("1", "search_lightrag", `{"query":"slow"}`))
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: 20 * time.Millisecond, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if !errors.Is(err, context.DeadlineExceeded) || len(result.Evidence) != 0 || result.StopReason != "deadline" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("fails closed after a tool-local deadline with prior evidence", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(context.Context, string) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "lightrag", Title: "guide", Content: "fact"}}, nil
		}}
		web := &researchWebClient{search: func(ctx context.Context, _ string) (usecase.WebSearchResult, error) {
			<-ctx.Done()
			return usecase.WebSearchResult{}, ctx.Err()
		}}
		model := scriptedResearchModel(schema.AssistantMessage("", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"guide"}`}},
			{ID: "2", Function: schema.FunctionCall{Name: "search_web", Arguments: `{"query":"slow"}`}},
		}))
		agent := newTestResearchAgent(t, model, store, web, true, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: 20 * time.Millisecond, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true, NeedWeb: true})

		if !errors.Is(err, context.DeadlineExceeded) || result.Status != usecase.ResearchPartial || !result.Degraded || len(result.Evidence) != 1 || result.StopReason != "deadline" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("enforces the total deadline", func(t *testing.T) {
		model := &fakeChatModel{generateContext: func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		agent := newTestResearchAgent(t, model, &researchKnowledgeStore{}, nil, false, ResearchLimits{TotalTimeout: 20 * time.Millisecond, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		_, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("collects catalog and forum citations", func(t *testing.T) {
		catalogSearch := &researchCatalog{result: catalog.ListResult{Items: []catalogentity.Game{{Slug: "example-game", Name: "Example Game", Summary: "catalog fact"}}}}
		forumSearch := &researchForum{result: community.PostPage{Items: []communityentity.Post{{ID: 42, Title: "Build guide", Content: "forum fact"}}}}
		model := scriptedResearchModel(
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "1", Function: schema.FunctionCall{Name: "search_catalog", Arguments: `{"query":" example ","region":"cn","currency":"cny"}`}},
				{ID: "2", Function: schema.FunctionCall{Name: "search_forum", Arguments: `{"query":" build ","gameId":7}`}},
			}),
			schema.AssistantMessage("done", nil),
		)
		agent, err := NewResearchAgent(context.Background(), model, "research", ResearchCapabilities{Knowledge: &researchKnowledgeStore{}, Catalog: catalogSearch, Forum: forumSearch}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
		if err != nil {
			t.Fatal(err)
		}

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if err != nil || len(result.Evidence) != 2 || result.Evidence[0].URL != "/api/games/example-game" || result.Evidence[1].URL != "/api/community/posts/42" || catalogSearch.input.Query != "example" || forumSearch.input.Query != "build" || forumSearch.input.GameID != 7 {
			t.Fatalf("result=%#v catalog=%#v forum=%#v err=%v", result, catalogSearch.input, forumSearch.input, err)
		}
	})

	t.Run("rejects a mutation-like tool request", func(t *testing.T) {
		store := &researchKnowledgeStore{}
		model := scriptedResearchModel(researchToolCall("1", "create_order", `{"editionId":1}`))
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"buy it"}, NeedLocalKnowledge: true})

		if err == nil || result.ToolCalls != 0 || len(store.queries()) != 0 {
			t.Fatalf("result=%#v queries=%v err=%v", result, store.queries(), err)
		}
	})
}

func TestResearchRunRunTool(t *testing.T) {
	t.Run("records successful evidence", func(t *testing.T) {
		state := &researchRun{maxTools: 1, toolTimeout: time.Second}
		observation, err := state.runTool(context.Background(), "knowledge", func(context.Context) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "knowledge", Title: "guide", Content: "fact"}}, nil
		})

		if err != nil || observation.Status != "ok" || state.successes != 1 || state.failures != 0 || len(state.evidence) != 1 {
			t.Fatalf("observation=%#v state=%#v err=%v", observation, state, err)
		}
	})

	t.Run("bounds evidence returned to the model", func(t *testing.T) {
		state := &researchRun{maxTools: 1, toolTimeout: time.Second}
		observation, err := state.runTool(context.Background(), "forum", func(context.Context) ([]usecase.Evidence, error) {
			return []usecase.Evidence{{Source: "forum", Content: strings.Repeat("界", 801)}}, nil
		})

		if err != nil || len([]rune(observation.Evidence[0].Content)) != 800 || len([]rune(state.evidence[0].Content)) != 800 {
			t.Fatalf("observation runes=%d stored runes=%d err=%v", len([]rune(observation.Evidence[0].Content)), len([]rune(state.evidence[0].Content)), err)
		}
	})

	t.Run("classifies deterministic input errors as invalid", func(t *testing.T) {
		for name, inputErr := range map[string]error{
			"missing query":         errInvalidQuery,
			"invalid search query":  usecase.ErrInvalidSearchQuery,
			"invalid catalog input": catalog.ErrInvalidInput,
			"invalid forum input":   community.ErrInvalidInput,
		} {
			t.Run(name, func(t *testing.T) {
				state := &researchRun{maxTools: 1, toolTimeout: time.Second}
				observation, err := state.runTool(context.Background(), "provider", func(context.Context) ([]usecase.Evidence, error) {
					return nil, inputErr
				})

				if err != nil || observation.Status != "invalid" || state.calls != 1 || state.successes != 0 || state.failures != 0 || state.allFailed() {
					t.Fatalf("observation=%#v state=%#v err=%v", observation, state, err)
				}
			})
		}
	})

	t.Run("records provider failures", func(t *testing.T) {
		state := &researchRun{maxTools: 2, toolTimeout: time.Second, plannedProviders: map[string]bool{"web": true, "lightrag": true}}
		observation, err := state.runTool(context.Background(), "web", func(context.Context) ([]usecase.Evidence, error) {
			return nil, errors.New("unavailable")
		})

		if err != nil || observation.Status != "failed" || state.successes != 0 || state.failures != 1 || state.allFailed() {
			t.Fatalf("observation=%#v state=%#v err=%v", observation, state, err)
		}
		_, err = state.runTool(context.Background(), "lightrag", func(context.Context) ([]usecase.Evidence, error) {
			return nil, errors.New("unavailable")
		})
		if err != nil || !state.allFailed() {
			t.Fatalf("state=%#v err=%v", state, err)
		}
	})

	t.Run("propagates provider contract failures", func(t *testing.T) {
		state := &researchRun{maxTools: 1, toolTimeout: time.Second, plannedProviders: map[string]bool{"lightrag": true}}
		observation, err := state.runTool(context.Background(), "lightrag", func(context.Context) ([]usecase.Evidence, error) {
			return nil, knowledgeentity.ErrContract
		})

		if !errors.Is(err, knowledgeentity.ErrContract) || observation.Status != "" || state.failures != 0 || state.allFailed() {
			t.Fatalf("observation=%#v state=%#v err=%v", observation, state, err)
		}
	})

	t.Run("rejects results returned after the tool-local deadline", func(t *testing.T) {
		state := &researchRun{maxTools: 1, toolTimeout: 10 * time.Millisecond, plannedProviders: map[string]bool{"web": true}}
		observation, err := state.runTool(context.Background(), "web", func(ctx context.Context) ([]usecase.Evidence, error) {
			<-ctx.Done()
			return []usecase.Evidence{{Source: "web", Content: "late"}}, nil
		})

		if !errors.Is(err, context.DeadlineExceeded) || observation.Status != "" || len(state.evidence) != 0 {
			t.Fatalf("observation=%#v state=%#v err=%v", observation, state, err)
		}
	})
}

func newTestResearchAgent(t *testing.T, chatModel *fakeChatModel, store *researchKnowledgeStore, webClient usecase.WebSearchClient, webEnabled bool, limits ResearchLimits) *ResearchAgent {
	t.Helper()
	var web *usecase.WebSearch
	if webClient != nil {
		web = usecase.NewWebSearch(webClient)
	}
	agent, err := NewResearchAgent(context.Background(), chatModel, "research", ResearchCapabilities{Knowledge: store, Catalog: &researchCatalog{}, Forum: &researchForum{}, Web: web, WebEnabled: webEnabled}, limits)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func scriptedResearchModel(messages ...*schema.Message) *fakeChatModel {
	var mu sync.Mutex
	index := 0
	return &fakeChatModel{generateContext: func(context.Context, []*schema.Message) (*schema.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(messages) {
			return schema.AssistantMessage("done", nil), nil
		}
		message := messages[index]
		index++
		return message, nil
	}}
}

func researchToolCall(id, name, arguments string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Function: schema.FunctionCall{Name: name, Arguments: arguments}}})
}

func typedResearchTaskAndPlan() (assistantentity.ResearchTask, assistantentity.QueryPlan) {
	task := assistantentity.ResearchTask{Envelope: assistantentity.Envelope{SchemaVersion: 1, RunID: "12345678-1234-4123-8123-123456789abc", Sequence: 1, SkillID: "research_guide", SkillVersion: "1.0.0"}, Objective: "research rpg", QueryUnitIDs: []string{"q1"}, RequiredFacets: []string{"genre"}, AllowedTools: []string{"search_lightrag"}}
	plan := assistantentity.QueryPlan{SchemaVersion: 1, Units: []assistantentity.QueryUnit{{ID: "q1", Text: "planned rpg query", Sources: []assistantentity.QuerySource{assistantentity.SourceLightRAG}, LightRAGMode: assistantentity.LightRAGHybrid, Freshness: "stable", RequiredFacets: []string{"genre"}}}}
	return task, plan
}

type researchProviderTimeoutError struct{}

func (researchProviderTimeoutError) Error() string { return "provider timeout" }
func (researchProviderTimeoutError) Timeout() bool { return true }

func messagesContain(messages []*schema.Message, value string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, value) {
			return true
		}
	}
	return false
}

type researchKnowledgeStore struct {
	mu     sync.Mutex
	seen   []string
	search func(context.Context, string) ([]usecase.Evidence, error)
}

type modeKnowledgeStore struct {
	calls                       int
	query, gameCode, regionCode string
	mode                        string
}

func (s *modeKnowledgeStore) SearchEvidence(_ context.Context, query, gameCode, regionCode, mode string, _ int) ([]usecase.Evidence, error) {
	s.calls++
	s.query = query
	s.gameCode = gameCode
	s.regionCode = regionCode
	s.mode = mode
	return []usecase.Evidence{{Source: "lightrag", Title: "RPG genre", Content: "genre fact"}}, nil
}

func (s *researchKnowledgeStore) SearchEvidence(ctx context.Context, query, _, _, _ string, _ int) ([]usecase.Evidence, error) {
	s.mu.Lock()
	s.seen = append(s.seen, query)
	s.mu.Unlock()
	if s.search == nil {
		return nil, nil
	}
	return s.search(ctx, query)
}

func (s *researchKnowledgeStore) queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

type researchWebClient struct {
	search func(context.Context, string) (usecase.WebSearchResult, error)
}

func (c *researchWebClient) Search(ctx context.Context, query string) (usecase.WebSearchResult, error) {
	return c.search(ctx, query)
}

type researchCatalog struct {
	input  catalog.ListInput
	result catalog.ListResult
	err    error
}

func (c *researchCatalog) List(_ context.Context, input catalog.ListInput) (catalog.ListResult, error) {
	c.input = input
	return c.result, c.err
}

type researchForum struct {
	input  community.ListPostsInput
	result community.PostPage
	err    error
}

func (f *researchForum) ListPosts(_ context.Context, input community.ListPostsInput) (community.PostPage, error) {
	f.input = input
	return f.result, f.err
}
