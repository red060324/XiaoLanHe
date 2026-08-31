package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

type Route string

const (
	RouteDirect   Route = "DIRECT_CHAT"
	RouteClarify  Route = "CLARIFY"
	RouteEvidence Route = "EVIDENCE_ANSWER"
)

type RouteDecision struct {
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

type ResearchStatus string

const (
	ResearchComplete ResearchStatus = "complete"
	ResearchNoResult ResearchStatus = "no_result"
	ResearchPartial  ResearchStatus = "partial"
	ResearchBounded  ResearchStatus = "bounded"
)

var (
	ErrAllResearchToolsFailed = errors.New("all research tools failed")
	ErrResearchBudgetExceeded = errors.New("research budget exceeded")
)

type ResearchResult struct {
	Evidence              []Evidence
	Notes                 []string
	Status                ResearchStatus
	StopReason            string
	Iterations, ToolCalls int
	Degraded              bool
}

type AnswerRequest struct {
	Message, Context, ResponseMode string
	Route                          Route
	Evidence                       []Evidence
	Notes                          []string
}

type RouterNode interface {
	Route(context.Context, string, string) (RouteDecision, error)
}

type ResearchAgent interface {
	Research(context.Context, RouteDecision) (ResearchResult, error)
}

type AnswerNode interface {
	GenerateAnswer(context.Context, AnswerRequest) (Answer, error)
	StreamAnswer(context.Context, AnswerRequest) (AnswerStream, error)
}

type AssistantFlow struct {
	router     RouterNode
	researcher ResearchAgent
	answerer   AnswerNode
}

func NewAssistantFlow(router RouterNode, researcher ResearchAgent, answerer AnswerNode) *AssistantFlow {
	return &AssistantFlow{router: router, researcher: researcher, answerer: answerer}
}

func (a *AssistantFlow) Generate(ctx context.Context, input AssistantInput) (Answer, error) {
	request, err := a.prepare(ctx, input)
	if err != nil {
		return Answer{}, err
	}
	answer, err := a.answerer.GenerateAnswer(ctx, request)
	answer.Route = string(request.Route)
	return answer, err
}

func (a *AssistantFlow) Stream(ctx context.Context, input AssistantInput) (AnswerStream, error) {
	request, err := a.prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	stream, err := a.answerer.StreamAnswer(ctx, request)
	if err != nil {
		return nil, err
	}
	return &assistantStream{AnswerStream: stream, route: string(request.Route)}, nil
}

type assistantStream struct {
	AnswerStream
	route string
}

func (s *assistantStream) Route() string { return s.route }

func (a *AssistantFlow) prepare(ctx context.Context, input AssistantInput) (AnswerRequest, error) {
	decision, err := a.router.Route(ctx, input.Message, input.Context)
	if err != nil {
		return AnswerRequest{}, fmt.Errorf("route: %w", err)
	}
	slog.InfoContext(ctx, "assistant routed", "route", decision.Route)
	request := AnswerRequest{Message: input.Message, Context: input.Context, ResponseMode: decision.ResponseMode, Route: decision.Route, Notes: decision.Notes}
	if decision.Route != RouteEvidence {
		request.Context = ""
		return request, nil
	}
	result, err := a.researcher.Research(ctx, decision)
	if err != nil {
		return AnswerRequest{}, fmt.Errorf("research: %w", err)
	}
	request.Evidence = result.Evidence
	request.Notes = append(request.Notes, result.Notes...)
	return request, nil
}

var _ Assistant = (*AssistantFlow)(nil)
