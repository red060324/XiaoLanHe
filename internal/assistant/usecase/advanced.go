package usecase

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
	legacy "github.com/red060324/XiaoLanHe/internal/usecase"
)

type AdvancedConfig struct {
	Limit      entity.BudgetLimit
	WebEnabled bool
}

type AdvancedAssistant struct {
	router   RouterNode
	planner  QueryPlannerNode
	copilot  Copilot
	answerer legacy.AnswerNode
	skills   *skill.Registry
	profiles ProfileStore
	config   AdvancedConfig
	newRunID func() (string, error)
}

type runIDContextKey struct{}

func NewAdvancedAssistant(router RouterNode, planner QueryPlannerNode, copilot Copilot, answerer legacy.AnswerNode, skills *skill.Registry, profiles ProfileStore, config AdvancedConfig) (*AdvancedAssistant, error) {
	if router == nil || planner == nil || copilot == nil || answerer == nil || skills == nil || profiles == nil {
		return nil, entity.ErrInvalidAgentContract
	}
	if _, err := NewBudget(config.Limit); err != nil {
		return nil, err
	}
	return &AdvancedAssistant{router: router, planner: planner, copilot: copilot, answerer: answerer, skills: skills, profiles: profiles, config: config, newRunID: newAgentRunID}, nil
}

func (a *AdvancedAssistant) Generate(ctx context.Context, input legacy.AssistantInput) (legacy.Answer, error) {
	started := time.Now()
	request, budget, runCtx, cancel, err := a.prepare(ctx, input)
	if cancel != nil {
		defer cancel()
	}
	if err != nil {
		LogRunEvent(ctx, "assistant.run", "unavailable", "game_copilot", "prepare", "error", StopReason(err), started, entity.BudgetUsage{}, slog.String("error_class", ErrorClass(err)))
		return legacy.Answer{}, err
	}
	if err := budget.TakeModel(runCtx); err != nil {
		LogRunEvent(ctx, "assistant.run", runIDFromContext(runCtx), "game_copilot", "answer", "bounded", StopReason(err), started, budget.Usage(), slog.String("error_class", ErrorClass(err)), slog.String("route", string(request.Route)))
		return legacy.Answer{}, err
	}
	answer, err := a.answerer.GenerateAnswer(runCtx, request)
	answer.Route = string(request.Route)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	LogRunEvent(ctx, "assistant.run", runIDFromContext(runCtx), "game_copilot", "answer", outcome, StopReason(err), started, budget.Usage(), slog.String("error_class", ErrorClass(err)), slog.String("route", string(request.Route)), slog.Int("evidence_count", len(request.Evidence)), slog.Bool("has_plan", request.Plan != ""))
	return answer, err
}

func (a *AdvancedAssistant) Stream(ctx context.Context, input legacy.AssistantInput) (legacy.AnswerStream, error) {
	started := time.Now()
	request, budget, runCtx, cancel, err := a.prepare(ctx, input)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		LogRunEvent(ctx, "assistant.run", "unavailable", "game_copilot", "stream_prepare", "error", StopReason(err), started, entity.BudgetUsage{}, slog.String("error_class", ErrorClass(err)))
		return nil, err
	}
	if err := budget.TakeModel(runCtx); err != nil {
		LogRunEvent(ctx, "assistant.run", runIDFromContext(runCtx), "game_copilot", "stream_answer", "bounded", StopReason(err), started, budget.Usage(), slog.String("error_class", ErrorClass(err)), slog.String("route", string(request.Route)))
		cancel()
		return nil, err
	}
	stream, err := a.answerer.StreamAnswer(runCtx, request)
	if err != nil {
		LogRunEvent(ctx, "assistant.run", runIDFromContext(runCtx), "game_copilot", "stream_answer", "error", StopReason(err), started, budget.Usage(), slog.String("error_class", ErrorClass(err)), slog.String("route", string(request.Route)))
		cancel()
		return nil, err
	}
	return &advancedStream{AnswerStream: stream, ctx: ctx, route: string(request.Route), runID: runIDFromContext(runCtx), budget: budget, started: started, evidenceCount: len(request.Evidence), hasPlan: request.Plan != "", cancel: cancel}, nil
}

func (a *AdvancedAssistant) prepare(parent context.Context, input legacy.AssistantInput) (legacy.AnswerRequest, *Budget, context.Context, context.CancelFunc, error) {
	runID, err := a.newRunID()
	if err != nil {
		return legacy.AnswerRequest{}, nil, nil, nil, err
	}
	requestBudget, err := NewBudget(a.config.Limit)
	if err != nil {
		return legacy.AnswerRequest{}, nil, nil, nil, err
	}
	runCtx, cancel := requestBudget.Context(parent)
	runCtx = context.WithValue(runCtx, runIDContextKey{}, runID)
	routeStarted := time.Now()
	decision, err := a.router.Route(runCtx, input.Message, input.Context, requestBudget)
	if err != nil {
		cancel()
		return legacy.AnswerRequest{}, nil, nil, nil, fmt.Errorf("route: %w", err)
	}
	slog.InfoContext(runCtx, "assistant route completed", "event", "assistant.route", "run_id", runID, "route", decision.Route, "skill_id", decision.SkillID, "skill_version", decision.SkillVersion, "outcome", "ok")
	RecordAssistantEvent("assistant.route", "router", "route", "ok", "complete", string(decision.Route), decision.SkillID, routeStarted)
	definition, err := a.skills.Resolve(decision.SkillID, decision.SkillVersion)
	if err != nil || !definition.Supports(decision) {
		cancel()
		return legacy.AnswerRequest{}, nil, nil, nil, entity.ErrInvalidAgentContract
	}
	if err := requestBudget.Constrain(definition.Budget); err != nil {
		cancel()
		return legacy.AnswerRequest{}, nil, nil, nil, err
	}
	request := legacy.AnswerRequest{Message: input.Message, Context: input.Context, ResponseMode: decision.ResponseMode, Route: legacyRoute(decision.Route)}
	if decision.Route == entity.RouteDirect || decision.Route == entity.RouteClarify {
		return request, requestBudget, runCtx, cancel, nil
	}
	planStarted := time.Now()
	plan, fallback, err := a.planner.Plan(runCtx, input.Message, input.Context, definition, a.config.WebEnabled, requestBudget)
	if err != nil {
		cancel()
		return legacy.AnswerRequest{}, nil, nil, nil, fmt.Errorf("plan: %w", err)
	}
	slog.InfoContext(runCtx, "assistant query plan completed", "event", "assistant.query_plan", "run_id", runID, "skill_id", definition.ID, "skill_version", definition.Version, "query_units", len(plan.Units), "fallback", fallback, "outcome", "ok")
	RecordAssistantEvent("assistant.query_plan", "query_planner", "plan", "ok", "complete", string(decision.Route), definition.ID, planStarted)
	if fallback {
		request.Notes = append(request.Notes, "查询规划使用了受限安全回退。")
	}
	profile := entity.EmptyProfile()
	if input.UserID > 0 {
		if loaded, found, profileErr := a.profiles.LoadAssistantProfile(runCtx, input.UserID); profileErr != nil {
			cancel()
			return legacy.AnswerRequest{}, nil, nil, nil, fmt.Errorf("load assistant profile: %w", profileErr)
		} else if found {
			profile = loaded
		}
	}
	request.Profile = legacy.AssistantProfile{
		FavoriteGenres: append([]string(nil), profile.FavoriteGenres...), PreferredPlatforms: append([]string(nil), profile.PreferredPlatforms...),
		DefaultRegion: profile.DefaultRegion, PreferredLanguages: append([]string(nil), profile.PreferredLanguages...),
		MaxPriceMinor: profile.MaxPriceMinor, Currency: profile.Currency,
	}
	copilotStarted := time.Now()
	result, err := a.copilot.Run(runCtx, CopilotInput{RunID: runID, Message: input.Message, Context: input.Context, UserID: input.UserID, Profile: profile, Decision: decision, Plan: plan, Skill: definition, Budget: requestBudget, WebEnabled: a.config.WebEnabled})
	if err != nil {
		cancel()
		return legacy.AnswerRequest{}, nil, nil, nil, fmt.Errorf("copilot: %w", err)
	}
	slog.InfoContext(runCtx, "assistant copilot completed", "event", "assistant.copilot", "run_id", runID, "skill_id", definition.ID, "skill_version", definition.Version, "evidence_count", len(result.Evidence), "has_plan", result.Plan != nil, "model_calls", result.Usage.ModelCalls, "tool_calls", result.Usage.ToolCalls, "delegations", result.Usage.Delegations, "outcome", "ok")
	RecordAssistantEvent("assistant.copilot", "game_copilot", "supervise", "ok", "complete", string(decision.Route), definition.ID, copilotStarted)
	for _, value := range result.Evidence {
		request.Evidence = append(request.Evidence, legacy.Evidence{Source: value.Source, Title: value.Title, Content: value.Content, URL: value.URL, Score: value.Score})
	}
	request.Notes = append(request.Notes, result.Notes...)
	if result.Plan != nil {
		encoded, _ := json.Marshal(result.Plan)
		request.Plan = string(encoded)
	}
	return request, requestBudget, runCtx, cancel, nil
}

func runIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(runIDContextKey{}).(string)
	if value == "" {
		return "unavailable"
	}
	return value
}

type advancedStream struct {
	legacy.AnswerStream
	ctx           context.Context
	route, runID  string
	budget        *Budget
	started       time.Time
	evidenceCount int
	hasPlan       bool
	cancel        context.CancelFunc
	closeOnce     sync.Once
	finishOnce    sync.Once
}

func (s *advancedStream) Route() string { return s.route }
func (s *advancedStream) Recv() (string, error) {
	chunk, err := s.AnswerStream.Recv()
	if err != nil {
		s.closeWithError(err)
	}
	return chunk, err
}
func (s *advancedStream) Close() {
	s.closeOnce.Do(s.AnswerStream.Close)
	s.closeWithError(context.Canceled)
}
func (s *advancedStream) closeWithError(err error) {
	s.finishOnce.Do(func() {
		outcome := "ok"
		stopReason := "complete"
		errorClass := "none"
		if err != nil && !errors.Is(err, io.EOF) {
			outcome, stopReason, errorClass = "error", StopReason(err), ErrorClass(err)
		}
		LogRunEvent(s.ctx, "assistant.run", s.runID, "game_copilot", "stream_answer", outcome, stopReason, s.started, s.budget.Usage(), slog.String("error_class", errorClass), slog.String("route", s.route), slog.Int("evidence_count", s.evidenceCount), slog.Bool("has_plan", s.hasPlan))
		s.cancel()
	})
}

func legacyRoute(route entity.Route) legacy.Route {
	if route == entity.RouteDirect {
		return legacy.RouteDirect
	}
	if route == entity.RouteClarify {
		return legacy.RouteClarify
	}
	return legacy.RouteEvidence
}

func newAgentRunID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

var _ legacy.Assistant = (*AdvancedAssistant)(nil)
