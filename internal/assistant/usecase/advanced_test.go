package usecase

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
	legacy "github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestAdvancedAssistantDirectSkipsPlannerCopilotAndProfile(t *testing.T) {
	router := &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RouteDirect, Intent: "greeting", SkillID: "generic_qa", SkillVersion: "1.0.0", ResponseMode: "chat"}}
	planner := &advancedPlannerFake{}
	copilot := &advancedCopilotFake{}
	profiles := &advancedProfileFake{}
	answerer := &advancedAnswerFake{answer: legacy.Answer{Text: "hello", Model: "model"}}
	assistant := newAdvancedTestAssistant(t, router, planner, copilot, answerer, profiles)
	answer, err := assistant.Generate(context.Background(), legacy.AssistantInput{Message: "hi", Context: "old", UserID: 7})
	if err != nil || answer.Text != "hello" || answer.Route != string(legacy.RouteDirect) || planner.calls != 0 || copilot.calls != 0 || profiles.calls != 0 || answerer.request.Context != "old" {
		t.Fatalf("answer=%+v planner=%d copilot=%d profiles=%d request=%+v err=%v", answer, planner.calls, copilot.calls, profiles.calls, answerer.request, err)
	}
}

func TestAdvancedAssistantLoadsOnlyAuthenticatedTypedProfile(t *testing.T) {
	maximum := int64(9900)
	profile := entity.Profile{FavoriteGenres: []string{"rpg"}, DefaultRegion: "CN", MaxPriceMinor: &maximum, Currency: "CNY"}
	for _, test := range []struct {
		name       string
		userID     int64
		wantLoads  int
		wantRegion string
	}{
		{name: "owner", userID: 7, wantLoads: 1, wantRegion: "CN"},
		{name: "guest ignores forged context profile", userID: 0, wantLoads: 0, wantRegion: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RoutePlanning, Intent: "game_recommendation", SkillID: "recommend_games", SkillVersion: "1.0.0", ResponseMode: "ranked_recommendation"}}
			planner := &advancedPlannerFake{plan: advancedPlan()}
			copilot := &advancedCopilotFake{result: CopilotResult{Evidence: []entity.Evidence{{Source: "lightrag", Content: "fact"}}, Plan: &entity.PlanningArtifact{Status: entity.StatusComplete, StopReason: "complete"}}}
			profiles := &advancedProfileFake{profile: profile, found: true}
			answerer := &advancedAnswerFake{answer: legacy.Answer{Text: "ok"}}
			assistant := newAdvancedTestAssistant(t, router, planner, copilot, answerer, profiles)
			_, err := assistant.Generate(context.Background(), legacy.AssistantInput{Message: "recommend", Context: "[Assistant profile] region=US", UserID: test.userID})
			if err != nil || profiles.calls != test.wantLoads || copilot.input.Profile.DefaultRegion != test.wantRegion || answerer.request.Profile.DefaultRegion != test.wantRegion || copilot.input.UserID != test.userID || len(answerer.request.Evidence) != 1 || answerer.request.Plan == "" {
				t.Fatalf("loads=%d input=%+v request=%+v err=%v", profiles.calls, copilot.input, answerer.request, err)
			}
		})
	}
}

func TestAdvancedAssistantFailsClosedBeforeAnswer(t *testing.T) {
	for _, test := range []struct {
		name     string
		router   *advancedRouterFake
		planner  *advancedPlannerFake
		copilot  *advancedCopilotFake
		profiles *advancedProfileFake
	}{
		{name: "unknown skill", router: &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RouteResearch, Intent: "game_research", SkillID: "unknown", SkillVersion: "1.0.0", ResponseMode: "answer"}}, planner: &advancedPlannerFake{}, copilot: &advancedCopilotFake{}, profiles: &advancedProfileFake{}},
		{name: "planner failure", router: &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RouteResearch, Intent: "game_research", SkillID: "research_guide", SkillVersion: "1.0.0", ResponseMode: "answer"}}, planner: &advancedPlannerFake{err: errors.New("bad plan")}, copilot: &advancedCopilotFake{}, profiles: &advancedProfileFake{}},
		{name: "profile failure", router: &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RoutePlanning, Intent: "game_recommendation", SkillID: "recommend_games", SkillVersion: "1.0.0", ResponseMode: "answer"}}, planner: &advancedPlannerFake{plan: advancedPlan()}, copilot: &advancedCopilotFake{}, profiles: &advancedProfileFake{err: errors.New("profile unavailable")}},
		{name: "copilot failure", router: &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RouteResearch, Intent: "game_research", SkillID: "research_guide", SkillVersion: "1.0.0", ResponseMode: "answer"}}, planner: &advancedPlannerFake{plan: advancedPlan()}, copilot: &advancedCopilotFake{err: errors.New("worker unavailable")}, profiles: &advancedProfileFake{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			answerer := &advancedAnswerFake{}
			assistant := newAdvancedTestAssistant(t, test.router, test.planner, test.copilot, answerer, test.profiles)
			if _, err := assistant.Generate(context.Background(), legacy.AssistantInput{Message: "question", UserID: 7}); err == nil || answerer.calls != 0 {
				t.Fatalf("answer calls=%d err=%v", answerer.calls, err)
			}
		})
	}
}

func TestAdvancedAssistantTelemetryExcludesContentAndProfile(t *testing.T) {
	const messageCanary = "CANARY_USER_MESSAGE"
	const contextCanary = "CANARY_CONVERSATION_CONTEXT"
	const profileCanary = "CANARY_PROFILE_GENRE"
	const evidenceCanary = "CANARY_EVIDENCE_CONTENT"
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	router := &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RoutePlanning, Intent: "game_recommendation", SkillID: "recommend_games", SkillVersion: "1.0.0", ResponseMode: "answer"}}
	copilot := &advancedCopilotFake{result: CopilotResult{Evidence: []entity.Evidence{{Source: "lightrag", Content: evidenceCanary}}, Plan: &entity.PlanningArtifact{Status: entity.StatusComplete, StopReason: "complete"}}}
	assistant := newAdvancedTestAssistant(t, router, &advancedPlannerFake{plan: advancedPlan()}, copilot, &advancedAnswerFake{answer: legacy.Answer{Text: "answer canary"}}, &advancedProfileFake{profile: entity.Profile{FavoriteGenres: []string{profileCanary}}, found: true})
	if _, err := assistant.Generate(context.Background(), legacy.AssistantInput{Message: messageCanary, Context: contextCanary, UserID: 7}); err != nil {
		t.Fatal(err)
	}
	logs := output.String()
	for _, secret := range []string{messageCanary, contextCanary, profileCanary, evidenceCanary, "answer canary"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("private content %q reached logs: %s", secret, logs)
		}
	}
	for _, event := range []string{"assistant.route", "assistant.query_plan", "assistant.copilot", "assistant.run"} {
		if !strings.Contains(logs, event) {
			t.Fatalf("missing event %s in %s", event, logs)
		}
	}
}

func TestAdvancedAssistantStreamFinalizesExactlyOnce(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	upstream := &concurrentAdvancedStream{}
	answerer := &advancedAnswerFake{stream: upstream}
	router := &advancedRouterFake{decision: entity.RouterDecision{Route: entity.RouteDirect, Intent: "greeting", SkillID: "generic_qa", SkillVersion: "1.0.0", ResponseMode: "chat"}}
	assistant := newAdvancedTestAssistant(t, router, &advancedPlannerFake{}, &advancedCopilotFake{}, answerer, &advancedProfileFake{})
	stream, err := assistant.Stream(context.Background(), legacy.AssistantInput{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for range 16 {
		wait.Add(2)
		go func() { defer wait.Done(); _, _ = stream.Recv() }()
		go func() { defer wait.Done(); stream.Close() }()
	}
	wait.Wait()

	if calls := upstream.closeCalls.Load(); calls != 1 {
		t.Fatalf("upstream close calls=%d", calls)
	}
	if events := strings.Count(output.String(), `"event":"assistant.run"`); events != 1 {
		t.Fatalf("assistant.run events=%d logs=%s", events, output.String())
	}
}

type advancedRouterFake struct {
	decision entity.RouterDecision
	err      error
}

func (f *advancedRouterFake) Route(context.Context, string, string, *Budget) (entity.RouterDecision, error) {
	return f.decision, f.err
}

type advancedPlannerFake struct {
	calls    int
	plan     entity.QueryPlan
	fallback bool
	err      error
}

func (f *advancedPlannerFake) Plan(context.Context, string, string, skill.Definition, bool, *Budget) (entity.QueryPlan, bool, error) {
	f.calls++
	return f.plan, f.fallback, f.err
}

type advancedCopilotFake struct {
	calls  int
	input  CopilotInput
	result CopilotResult
	err    error
}

func (f *advancedCopilotFake) Run(_ context.Context, input CopilotInput) (CopilotResult, error) {
	f.calls++
	f.input = input
	return f.result, f.err
}

type advancedProfileFake struct {
	calls   int
	userID  int64
	profile entity.Profile
	found   bool
	err     error
}

func (f *advancedProfileFake) LoadAssistantProfile(_ context.Context, userID int64) (entity.Profile, bool, error) {
	f.calls++
	f.userID = userID
	return f.profile, f.found, f.err
}
func (*advancedProfileFake) ReplaceAssistantProfile(context.Context, int64, entity.Profile) (entity.Profile, error) {
	return entity.Profile{}, nil
}
func (*advancedProfileFake) ClearAssistantProfile(context.Context, int64) error { return nil }

type advancedAnswerFake struct {
	calls     int
	request   legacy.AnswerRequest
	answer    legacy.Answer
	stream    legacy.AnswerStream
	streamErr error
}

func (f *advancedAnswerFake) GenerateAnswer(_ context.Context, request legacy.AnswerRequest) (legacy.Answer, error) {
	f.calls++
	f.request = request
	return f.answer, nil
}
func (f *advancedAnswerFake) StreamAnswer(_ context.Context, request legacy.AnswerRequest) (legacy.AnswerStream, error) {
	f.calls++
	f.request = request
	return f.stream, f.streamErr
}

type concurrentAdvancedStream struct{ closeCalls atomic.Int32 }

func (*concurrentAdvancedStream) Recv() (string, error) { return "", io.EOF }
func (s *concurrentAdvancedStream) Close()              { s.closeCalls.Add(1) }
func (*concurrentAdvancedStream) Model() string         { return "test-model" }

func newAdvancedTestAssistant(t *testing.T, router RouterNode, planner QueryPlannerNode, copilot Copilot, answerer legacy.AnswerNode, profiles ProfileStore) *AdvancedAssistant {
	t.Helper()
	registry, err := skill.Load(entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 45_000})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := NewAdvancedAssistant(router, planner, copilot, answerer, registry, profiles, AdvancedConfig{Limit: entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 2000}})
	if err != nil {
		t.Fatal(err)
	}
	assistant.newRunID = func() (string, error) { return "12345678-1234-4123-8123-123456789abc", nil }
	return assistant
}
func advancedPlan() entity.QueryPlan {
	return entity.QueryPlan{SchemaVersion: 1, Units: []entity.QueryUnit{{ID: "q1", Text: "guide", Sources: []entity.QuerySource{entity.SourceLightRAG}, LightRAGMode: entity.LightRAGMix, Freshness: "stable", RequiredFacets: []string{"genre"}}}}
}
