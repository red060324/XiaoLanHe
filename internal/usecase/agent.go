package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Route string

const (
	RouteDirect   Route = "DIRECT_CHAT"
	RouteClarify  Route = "CLARIFY"
	RouteEvidence Route = "EVIDENCE_ANSWER"
)

type Plan struct {
	Route                       Route
	ResponseMode                string
	NeedLocalKnowledge, NeedWeb bool
	Queries                     []string
	Notes                       []string
}

type Evidence struct {
	Source, Title, Content, URL string
	Score                       float64
}

type AnswerRequest struct {
	Message, Context, ResponseMode string
	Route                          Route
	Evidence                       []Evidence
	Notes                          []string
}

type Planner interface {
	Plan(context.Context, string, string) (Plan, error)
}

type AnswerModel interface {
	GenerateAnswer(context.Context, AnswerRequest) (Answer, error)
	StreamAnswer(context.Context, AnswerRequest) (AnswerStream, error)
}

type EvidenceRetriever interface {
	Retrieve(context.Context, Plan) ([]Evidence, error)
}

type ResearchPlanner interface {
	Decompose(context.Context, Plan) (Plan, error)
}

type Agent struct {
	planner    Planner
	researcher EvidenceRetriever
	answerer   AnswerModel
}

func NewAgent(planner Planner, researcher EvidenceRetriever, answerer AnswerModel) *Agent {
	return &Agent{planner: planner, researcher: researcher, answerer: answerer}
}

func (a *Agent) Generate(ctx context.Context, input AssistantInput) (Answer, error) {
	request, err := a.prepare(ctx, input)
	if err != nil {
		return Answer{}, err
	}
	answer, err := a.answerer.GenerateAnswer(ctx, request)
	answer.Route = string(request.Route)
	return answer, err
}

func (a *Agent) Stream(ctx context.Context, input AssistantInput) (AnswerStream, error) {
	request, err := a.prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	stream, err := a.answerer.StreamAnswer(ctx, request)
	if err != nil {
		return nil, err
	}
	return &agentStream{AnswerStream: stream, route: string(request.Route)}, nil
}

type agentStream struct {
	AnswerStream
	route string
}

func (s *agentStream) Route() string { return s.route }

func (a *Agent) prepare(ctx context.Context, input AssistantInput) (AnswerRequest, error) {
	plan, err := a.planner.Plan(ctx, input.Message, input.Context)
	if err != nil {
		return AnswerRequest{}, fmt.Errorf("plan: %w", err)
	}
	request := AnswerRequest{Message: input.Message, Context: input.Context, ResponseMode: plan.ResponseMode, Route: plan.Route, Notes: plan.Notes}
	if plan.Route != RouteEvidence {
		request.Context = ""
		return request, nil
	}
	request.Evidence, err = a.researcher.Retrieve(ctx, plan)
	if err != nil {
		return AnswerRequest{}, fmt.Errorf("research: %w", err)
	}
	return request, nil
}

type Research struct {
	planner   ResearchPlanner
	knowledge *Knowledge
	web       *WebSearch
	parallel  int
}

func NewResearch(planner ResearchPlanner, knowledge *Knowledge, web *WebSearch) *Research {
	return &Research{planner: planner, knowledge: knowledge, web: web, parallel: 4}
}

func (r *Research) Retrieve(ctx context.Context, plan Plan) ([]Evidence, error) {
	if refined, err := r.planner.Decompose(ctx, plan); err == nil {
		plan = refined
	}
	queries := uniqueQueries(plan.Queries, 6)
	if len(queries) == 0 {
		queries = []string{""}
	}
	type result struct {
		items []Evidence
		err   error
	}
	results := make(chan result, len(queries)*2)
	sem := make(chan struct{}, r.parallel)
	var wg sync.WaitGroup
	run := func(fn func() ([]Evidence, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- result{err: ctx.Err()}
				return
			}
			items, err := fn()
			results <- result{items: items, err: err}
		}()
	}
	for _, query := range queries {
		query := query
		if plan.NeedLocalKnowledge {
			run(func() ([]Evidence, error) {
				items, err := r.knowledge.Search(ctx, query, "", "", 5)
				if err != nil {
					return nil, err
				}
				out := make([]Evidence, 0, len(items))
				for _, item := range items {
					out = append(out, Evidence{Source: "knowledge", Title: item.Title, Content: item.Text, URL: item.SourceURL, Score: float64(item.Score)})
				}
				return out, nil
			})
		}
		if plan.NeedWeb {
			run(func() ([]Evidence, error) {
				response, err := r.web.Run(ctx, query)
				if err != nil {
					return nil, err
				}
				out := make([]Evidence, 0, len(response.Items))
				for _, item := range response.Items {
					out = append(out, Evidence{Source: "web", Title: item.Title, Content: item.Snippet, URL: item.URL, Score: 50})
				}
				return out, nil
			})
		}
	}
	wg.Wait()
	close(results)
	lists := make([][]Evidence, 0)
	failures := 0
	calls := 0
	for result := range results {
		calls++
		if result.err != nil {
			failures++
			continue
		}
		lists = append(lists, result.items)
	}
	if calls > 0 && failures == calls {
		return nil, errorsJoin(ctx.Err(), "all evidence sources failed")
	}
	return reciprocalRank(lists, 5), nil
}

func uniqueQueries(values []string, limit int) []string {
	seen := map[string]bool{}
	result := make([]string, 0, limit)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func reciprocalRank(lists [][]Evidence, limit int) []Evidence {
	type ranked struct {
		item  Evidence
		score float64
	}
	merged := map[string]ranked{}
	for _, items := range lists {
		for rank, item := range items {
			key := strings.ToLower(item.Source + "\x00" + firstNonBlank(item.URL, item.Title+"\x00"+item.Content))
			value := merged[key]
			if value.item.Source == "" {
				value.item = item
			}
			value.score += 1 / float64(61+rank)
			merged[key] = value
		}
	}
	result := make([]ranked, 0, len(merged))
	for _, item := range merged {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].score == result[j].score {
			return evidenceKey(result[i].item) < evidenceKey(result[j].item)
		}
		return result[i].score > result[j].score
	})
	if len(result) > limit {
		result = result[:limit]
	}
	out := make([]Evidence, 0, len(result))
	for _, item := range result {
		item.item.Score = item.score
		out = append(out, item.item)
	}
	return out
}

func evidenceKey(item Evidence) string {
	return item.Source + "\x00" + item.URL + "\x00" + item.Title + "\x00" + item.Content
}
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func errorsJoin(cause error, message string) error {
	if cause != nil {
		return fmt.Errorf("%s: %w", message, cause)
	}
	return fmt.Errorf("%s", message)
}

var _ Assistant = (*Agent)(nil)
