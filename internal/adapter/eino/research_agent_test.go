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

	"github.com/cloudwego/eino/schema"

	assistantentity "github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistantuc "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	communityentity "github.com/red060324/XiaoLanHe/internal/community/entity"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestResearchAgentTypedTaskEnforcesPlanAndBudget(t *testing.T) {
	knowledge := &modeKnowledgeStore{}
	model := scriptedResearchModel(
		schema.AssistantMessage("", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"rpg","mode":"hybrid"}`}},
			{ID: "2", Function: schema.FunctionCall{Name: "search_catalog", Arguments: `{"query":"forbidden"}`}},
		}),
	)
	agent, err := NewResearchAgent(context.Background(), model, "research", ResearchCapabilities{Knowledge: knowledge, Catalog: &researchCatalog{}, Forum: &researchForum{}}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	task := assistantentity.ResearchTask{Envelope: assistantentity.Envelope{SchemaVersion: 1, RunID: "12345678-1234-4123-8123-123456789abc", Sequence: 1, SkillID: "research_guide", SkillVersion: "1.0.0"}, Objective: "research rpg", QueryUnitIDs: []string{"q1"}, RequiredFacets: []string{"genre"}, AllowedTools: []string{"search_lightrag", "search_catalog"}}
	plan := assistantentity.QueryPlan{SchemaVersion: 1, Units: []assistantentity.QueryUnit{{ID: "q1", Text: "rpg", Sources: []assistantentity.QuerySource{assistantentity.SourceLightRAG}, LightRAGMode: assistantentity.LightRAGHybrid, Freshness: "stable", RequiredFacets: []string{"genre"}}}}
	budget, _ := assistantuc.NewBudget(assistantentity.BudgetLimit{ModelCalls: 6, ToolCalls: 8, Delegations: 1, TimeoutMilliseconds: 2000})
	result, err := agent.RunResearch(context.Background(), task, plan, budget)
	if err != nil || len(result.Evidence) != 1 || knowledge.calls != 1 || knowledge.mode != "hybrid" || result.Artifact.Status != assistantentity.StatusPartial || !slices.Equal(result.Artifact.CoveredFacets, []string{"genre"}) || budget.Usage().ToolCalls != 1 {
		t.Fatalf("result=%+v knowledge=%+v usage=%+v err=%v", result, knowledge, budget.Usage(), err)
	}
}

func TestResearchAgentTypedTaskRejectsUnplannedMode(t *testing.T) {
	knowledge := &modeKnowledgeStore{}
	model := scriptedResearchModel(researchToolCall("1", "search_lightrag", `{"query":"rpg","mode":"global"}`), schema.AssistantMessage("done", nil))
	agent, _ := NewResearchAgent(context.Background(), model, "research", ResearchCapabilities{Knowledge: knowledge, Catalog: &researchCatalog{}, Forum: &researchForum{}}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
	task := assistantentity.ResearchTask{Envelope: assistantentity.Envelope{SchemaVersion: 1, RunID: "12345678-1234-4123-8123-123456789abc", Sequence: 1, SkillID: "research_guide", SkillVersion: "1.0.0"}, Objective: "research rpg", QueryUnitIDs: []string{"q1"}, RequiredFacets: []string{"genre"}, AllowedTools: []string{"search_lightrag"}}
	plan := assistantentity.QueryPlan{SchemaVersion: 1, Units: []assistantentity.QueryUnit{{ID: "q1", Text: "rpg", Sources: []assistantentity.QuerySource{assistantentity.SourceLightRAG}, LightRAGMode: assistantentity.LightRAGHybrid, Freshness: "stable", RequiredFacets: []string{"genre"}}}}
	budget, _ := assistantuc.NewBudget(assistantentity.BudgetLimit{ModelCalls: 6, ToolCalls: 8, Delegations: 1, TimeoutMilliseconds: 2000})
	result, err := agent.RunResearch(context.Background(), task, plan, budget)
	if err != nil || knowledge.calls != 0 || len(result.Evidence) != 0 || result.Artifact.Status != assistantentity.StatusNoResult {
		t.Fatalf("result=%+v knowledge=%+v err=%v", result, knowledge, err)
	}
}

func TestResearchAgentResearch(t *testing.T) {
	t.Run("observes evidence and refines the query", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(_ context.Context, query string) ([]usecase.KnowledgeSnippet, error) {
			return []usecase.KnowledgeSnippet{{ChunkID: int64(len(query)), Title: query, Text: query + " fact"}}, nil
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
		store := &researchKnowledgeStore{search: func(context.Context, string) ([]usecase.KnowledgeSnippet, error) {
			return []usecase.KnowledgeSnippet{{ChunkID: 1, Title: "guide", Text: "fact"}}, nil
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

	t.Run("distinguishes a successful empty search", func(t *testing.T) {
		model := scriptedResearchModel(researchToolCall("1", "search_lightrag", `{"query":"missing"}`), schema.AssistantMessage("done", nil))
		agent := newTestResearchAgent(t, model, &researchKnowledgeStore{}, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if err != nil || result.Status != usecase.ResearchNoResult || result.Degraded || len(result.Evidence) != 0 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("stops at the tool call budget with partial evidence", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(_ context.Context, query string) ([]usecase.KnowledgeSnippet, error) {
			return []usecase.KnowledgeSnippet{{ChunkID: int64(len(query)), Title: query, Text: "fact"}}, nil
		}}
		model := scriptedResearchModel(schema.AssistantMessage("", []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"one"}`}},
			{ID: "2", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"two"}`}},
			{ID: "3", Function: schema.FunctionCall{Name: "search_lightrag", Arguments: `{"query":"three"}`}},
		}))
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 2})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if err != nil || result.Status != usecase.ResearchBounded || result.StopReason != "max_tool_calls" || result.ToolCalls != 2 || len(result.Evidence) != 2 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("stops at the model iteration budget", func(t *testing.T) {
		var calls atomic.Int32
		model := &fakeChatModel{generateContext: func(context.Context, []*schema.Message) (*schema.Message, error) {
			call := calls.Add(1)
			return researchToolCall(fmt.Sprint(call), "search_lightrag", fmt.Sprintf(`{"query":"q%d"}`, call)), nil
		}}
		store := &researchKnowledgeStore{search: func(context.Context, string) ([]usecase.KnowledgeSnippet, error) {
			return []usecase.KnowledgeSnippet{{ChunkID: 1, Text: "fact"}}, nil
		}}
		agent := newTestResearchAgent(t, model, store, nil, false, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 2, MaxToolCalls: 8})

		result, err := agent.Research(context.Background(), usecase.RouteDecision{Queries: []string{"question"}, NeedLocalKnowledge: true})

		if err != nil || result.Status != usecase.ResearchBounded || result.StopReason != "max_iterations" || result.Iterations != 2 || result.ToolCalls != 2 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("propagates request cancellation into a tool", func(t *testing.T) {
		started := make(chan struct{})
		var once sync.Once
		store := &researchKnowledgeStore{search: func(ctx context.Context, _ string) ([]usecase.KnowledgeSnippet, error) {
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
		knowledge := usecase.NewKnowledge(&researchKnowledgeStore{}, unavailableTestEmbedder{})
		agent, err := NewResearchAgent(context.Background(), model, "research", ResearchCapabilities{Knowledge: knowledge, Catalog: catalogSearch, Forum: forumSearch}, ResearchLimits{TotalTimeout: time.Second, ToolTimeout: time.Second, MaxIterations: 6, MaxToolCalls: 8})
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
		state := &researchRun{maxTools: 1, toolTimeout: time.Second}
		observation, err := state.runTool(context.Background(), "web", func(context.Context) ([]usecase.Evidence, error) {
			return nil, errors.New("unavailable")
		})

		if err != nil || observation.Status != "failed" || state.successes != 0 || state.failures != 1 || !state.allFailed() {
			t.Fatalf("observation=%#v state=%#v err=%v", observation, state, err)
		}
	})
}

func newTestResearchAgent(t *testing.T, chatModel *fakeChatModel, store *researchKnowledgeStore, webClient usecase.WebSearchClient, webEnabled bool, limits ResearchLimits) *ResearchAgent {
	t.Helper()
	knowledge := usecase.NewKnowledge(store, unavailableTestEmbedder{})
	var web *usecase.WebSearch
	if webClient != nil {
		web = usecase.NewWebSearch(webClient)
	}
	agent, err := NewResearchAgent(context.Background(), chatModel, "research", ResearchCapabilities{Knowledge: knowledge, Catalog: &researchCatalog{}, Forum: &researchForum{}, Web: web, WebEnabled: webEnabled}, limits)
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
	search func(context.Context, string) ([]usecase.KnowledgeSnippet, error)
}

type modeKnowledgeStore struct {
	calls int
	mode  string
}

func (s *modeKnowledgeStore) SearchEvidence(_ context.Context, _, _, _, mode string, _ int) ([]usecase.Evidence, error) {
	s.calls++
	s.mode = mode
	return []usecase.Evidence{{Source: "lightrag", Title: "RPG genre", Content: "genre fact"}}, nil
}

func (*researchKnowledgeStore) CreateDocument(context.Context, usecase.KnowledgeDocument, []string, [][]float32) (int64, error) {
	return 0, nil
}

func (s *researchKnowledgeStore) SearchKeyword(ctx context.Context, query, _, _ string, _ int) ([]usecase.KnowledgeSnippet, error) {
	s.mu.Lock()
	s.seen = append(s.seen, query)
	s.mu.Unlock()
	if s.search == nil {
		return nil, nil
	}
	return s.search(ctx, query)
}

func (*researchKnowledgeStore) SearchVector(context.Context, []float32, string, string, int) ([]usecase.KnowledgeSnippet, error) {
	return nil, nil
}

func (s *researchKnowledgeStore) queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

type unavailableTestEmbedder struct{}

func (unavailableTestEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, usecase.ErrEmbeddingUnavailable
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
