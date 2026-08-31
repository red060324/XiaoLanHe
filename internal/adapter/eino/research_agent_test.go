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

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestResearchAgentResearch(t *testing.T) {
	t.Run("observes evidence and refines the query", func(t *testing.T) {
		store := &researchKnowledgeStore{search: func(_ context.Context, query string) ([]usecase.KnowledgeSnippet, error) {
			return []usecase.KnowledgeSnippet{{ChunkID: int64(len(query)), Title: query, Text: query + " fact"}}, nil
		}}
		var modelCalls atomic.Int32
		model := &fakeChatModel{generateContext: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
			switch modelCalls.Add(1) {
			case 1:
				return researchToolCall("1", "search_knowledge", `{"query":"first"}`), nil
			case 2:
				if !messagesContain(messages, "first fact") {
					return nil, errors.New("tool observation was not returned to the model")
				}
				return researchToolCall("2", "search_knowledge", `{"query":"refined"}`), nil
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
				{ID: "1", Function: schema.FunctionCall{Name: "search_knowledge", Arguments: `{"query":"guide"}`}},
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
		model := scriptedResearchModel(researchToolCall("1", "search_knowledge", `{"query":"missing"}`), schema.AssistantMessage("done", nil))
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
			{ID: "1", Function: schema.FunctionCall{Name: "search_knowledge", Arguments: `{"query":"one"}`}},
			{ID: "2", Function: schema.FunctionCall{Name: "search_knowledge", Arguments: `{"query":"two"}`}},
			{ID: "3", Function: schema.FunctionCall{Name: "search_knowledge", Arguments: `{"query":"three"}`}},
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
			return researchToolCall(fmt.Sprint(call), "search_knowledge", fmt.Sprintf(`{"query":"q%d"}`, call)), nil
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
		model := scriptedResearchModel(researchToolCall("1", "search_knowledge", `{"query":"slow"}`))
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
}

func newTestResearchAgent(t *testing.T, chatModel *fakeChatModel, store *researchKnowledgeStore, webClient usecase.WebSearchClient, webEnabled bool, limits ResearchLimits) *ResearchAgent {
	t.Helper()
	knowledge := usecase.NewKnowledge(store, unavailableTestEmbedder{})
	var web *usecase.WebSearch
	if webClient != nil {
		web = usecase.NewWebSearch(webClient)
	}
	agent, err := NewResearchAgent(context.Background(), chatModel, "research", knowledge, web, webEnabled, limits)
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
