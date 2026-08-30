package usecase

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestAgentGenerate(t *testing.T) {
	t.Run("direct route skips research", func(t *testing.T) {
		research := &retrieverFake{}
		answerer := &answerModelFake{answer: Answer{Text: "hello", Model: "m"}}
		answer, err := NewAgent(plannerFake{plan: Plan{Route: RouteDirect, ResponseMode: "chat"}}, research, answerer).Generate(context.Background(), AssistantInput{Message: "hi", Context: "old"})
		if err != nil || answer.Text != "hello" || research.calls != 0 || answerer.request.Route != RouteDirect || answerer.request.Context != "" {
			t.Fatalf("answer=%#v research=%d request=%#v err=%v", answer, research.calls, answerer.request, err)
		}
	})
	t.Run("evidence route passes retrieved evidence", func(t *testing.T) {
		evidence := []Evidence{{Source: "knowledge", Content: "fact"}}
		research := &retrieverFake{evidence: evidence}
		answerer := &answerModelFake{answer: Answer{Text: "ok"}}
		_, err := NewAgent(plannerFake{plan: Plan{Route: RouteEvidence}}, research, answerer).Generate(context.Background(), AssistantInput{Message: "q", Context: "old"})
		if err != nil || !slices.Equal(answerer.request.Evidence, evidence) || answerer.request.Context != "old" {
			t.Fatalf("request=%#v err=%v", answerer.request, err)
		}
	})
	t.Run("planner error stops answer", func(t *testing.T) {
		want := errors.New("planner")
		answerer := &answerModelFake{}
		_, err := NewAgent(plannerFake{err: want}, &retrieverFake{}, answerer).Generate(context.Background(), AssistantInput{Message: "q"})
		if !errors.Is(err, want) || answerer.calls != 0 {
			t.Fatalf("calls=%d err=%v", answerer.calls, err)
		}
	})
}

func TestUniqueQueries(t *testing.T) {
	got := uniqueQueries([]string{" a ", "a", "b", "c", "d", "e", "f"}, 6)
	if !slices.Equal(got, []string{"a", "b", "c", "d", "e", "f"}) {
		t.Fatalf("queries=%v", got)
	}
}

func TestReciprocalRank(t *testing.T) {
	a := Evidence{Source: "web", Title: "A", URL: "https://a"}
	b := Evidence{Source: "knowledge", Title: "B"}
	got := reciprocalRank([][]Evidence{{a, b}, {a}}, 2)
	if len(got) != 2 || got[0].URL != "https://a" || got[0].Score <= got[1].Score {
		t.Fatalf("evidence=%#v", got)
	}
}

type plannerFake struct {
	plan Plan
	err  error
}

func (f plannerFake) Plan(context.Context, string, string) (Plan, error) { return f.plan, f.err }

type retrieverFake struct {
	calls    int
	evidence []Evidence
	err      error
}

func (f *retrieverFake) Retrieve(context.Context, Plan) ([]Evidence, error) {
	f.calls++
	return f.evidence, f.err
}

type answerModelFake struct {
	calls   int
	answer  Answer
	request AnswerRequest
}

func (f *answerModelFake) GenerateAnswer(_ context.Context, request AnswerRequest) (Answer, error) {
	f.calls++
	f.request = request
	return f.answer, nil
}
func (f *answerModelFake) StreamAnswer(context.Context, AnswerRequest) (AnswerStream, error) {
	return nil, nil
}
