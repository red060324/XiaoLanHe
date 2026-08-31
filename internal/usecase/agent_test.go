package usecase

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestAssistantFlowGenerate(t *testing.T) {
	t.Run("direct route preserves context and skips research", func(t *testing.T) {
		research := &researchFake{}
		answerer := &answerNodeFake{answer: Answer{Text: "hello", Model: "m"}}
		answer, err := NewAssistantFlow(routerFake{decision: RouteDecision{Route: RouteDirect, ResponseMode: "chat"}}, research, answerer).Generate(context.Background(), AssistantInput{Message: "hi", Context: "old"})
		if err != nil || answer.Text != "hello" || research.calls != 0 || answerer.request.Route != RouteDirect || answerer.request.Context != "old" {
			t.Fatalf("answer=%#v research=%d request=%#v err=%v", answer, research.calls, answerer.request, err)
		}
	})

	t.Run("evidence route passes research result and degradation notes", func(t *testing.T) {
		evidence := []Evidence{{Source: "knowledge", Content: "fact"}}
		research := &researchFake{result: ResearchResult{Evidence: evidence, Status: ResearchPartial, Degraded: true, Notes: []string{"partial"}}}
		answerer := &answerNodeFake{answer: Answer{Text: "ok"}}
		_, err := NewAssistantFlow(routerFake{decision: RouteDecision{Route: RouteEvidence, Notes: []string{"route"}}}, research, answerer).Generate(context.Background(), AssistantInput{Message: "q", Context: "old"})
		if err != nil || !slices.Equal(answerer.request.Evidence, evidence) || !slices.Equal(answerer.request.Notes, []string{"route", "partial"}) || answerer.request.Context != "old" {
			t.Fatalf("request=%#v err=%v", answerer.request, err)
		}
	})

	t.Run("router error stops answer", func(t *testing.T) {
		want := errors.New("router")
		answerer := &answerNodeFake{}
		_, err := NewAssistantFlow(routerFake{err: want}, &researchFake{}, answerer).Generate(context.Background(), AssistantInput{Message: "q"})
		if !errors.Is(err, want) || answerer.calls != 0 {
			t.Fatalf("calls=%d err=%v", answerer.calls, err)
		}
	})

	t.Run("research error stops answer", func(t *testing.T) {
		want := errors.New("research")
		answerer := &answerNodeFake{}
		_, err := NewAssistantFlow(routerFake{decision: RouteDecision{Route: RouteEvidence}}, &researchFake{err: want}, answerer).Generate(context.Background(), AssistantInput{Message: "q"})
		if !errors.Is(err, want) || answerer.calls != 0 {
			t.Fatalf("calls=%d err=%v", answerer.calls, err)
		}
	})
}

type routerFake struct {
	decision RouteDecision
	err      error
}

func (f routerFake) Route(context.Context, string, string) (RouteDecision, error) {
	return f.decision, f.err
}

type researchFake struct {
	calls  int
	result ResearchResult
	err    error
}

func (f *researchFake) Research(context.Context, RouteDecision) (ResearchResult, error) {
	f.calls++
	return f.result, f.err
}

type answerNodeFake struct {
	calls   int
	answer  Answer
	request AnswerRequest
}

func (f *answerNodeFake) GenerateAnswer(_ context.Context, request AnswerRequest) (Answer, error) {
	f.calls++
	f.request = request
	return f.answer, nil
}

func (f *answerNodeFake) StreamAnswer(context.Context, AnswerRequest) (AnswerStream, error) {
	return nil, nil
}
