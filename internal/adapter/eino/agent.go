package einoadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

type AgentModel struct {
	model                                       model.BaseChatModel
	name, planning, research, direct, synthesis string
}

func NewAgentModel(chatModel model.BaseChatModel, name, planning, research, direct, synthesis string) *AgentModel {
	return &AgentModel{model: chatModel, name: name, planning: planning, research: research, direct: direct, synthesis: synthesis}
}

func (m *AgentModel) Plan(ctx context.Context, message, contextText string) (usecase.Plan, error) {
	input := fmt.Sprintf("【用户问题】\n%s\n\n【轻量上下文】\n%s", message, firstText(contextText, "无"))
	response, err := m.model.Generate(ctx, []*schema.Message{schema.SystemMessage(m.planning), schema.UserMessage(input)})
	if err != nil {
		return fallbackPlan(message), nil
	}
	var payload struct {
		RouteType          string   `json:"routeType"`
		ResponseMode       string   `json:"responseMode"`
		NeedLocalKnowledge bool     `json:"needLocalKnowledge"`
		NeedWebSearch      bool     `json:"needWebSearch"`
		SubQueries         []string `json:"subQueries"`
		Notes              []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(extractJSON(response.Content)), &payload); err != nil {
		return fallbackPlan(message), nil
	}
	route := usecase.Route(payload.RouteType)
	if route != usecase.RouteDirect && route != usecase.RouteClarify && route != usecase.RouteEvidence {
		return fallbackPlan(message), nil
	}
	if route == usecase.RouteEvidence && !payload.NeedLocalKnowledge && !payload.NeedWebSearch {
		payload.NeedLocalKnowledge = true
	}
	queries := append([]string{message}, payload.SubQueries...)
	return usecase.Plan{Route: route, ResponseMode: firstText(payload.ResponseMode, "qa"), NeedLocalKnowledge: route == usecase.RouteEvidence && payload.NeedLocalKnowledge, NeedWeb: route == usecase.RouteEvidence && payload.NeedWebSearch, Queries: queries, Notes: payload.Notes}, nil
}

func (m *AgentModel) Decompose(ctx context.Context, plan usecase.Plan) (usecase.Plan, error) {
	input, _ := json.Marshal(plan)
	response, err := m.model.Generate(ctx, []*schema.Message{schema.SystemMessage(m.research), schema.UserMessage(string(input))})
	if err != nil {
		return plan, nil
	}
	var payload struct {
		NeedLocalKnowledge bool     `json:"needLocalKnowledge"`
		NeedWebSearch      bool     `json:"needWebSearch"`
		SubQueries         []string `json:"subQueries"`
		Notes              []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(extractJSON(response.Content)), &payload); err != nil {
		return plan, nil
	}
	if payload.NeedLocalKnowledge || payload.NeedWebSearch {
		plan.NeedLocalKnowledge = payload.NeedLocalKnowledge
		plan.NeedWeb = payload.NeedWebSearch
	}
	baseQueries := plan.Queries
	if len(baseQueries) > 1 {
		baseQueries = baseQueries[:1]
	}
	plan.Queries = append(baseQueries, payload.SubQueries...)
	plan.Notes = append(plan.Notes, payload.Notes...)
	return plan, nil
}

func (m *AgentModel) GenerateAnswer(ctx context.Context, request usecase.AnswerRequest) (usecase.Answer, error) {
	response, err := m.model.Generate(ctx, m.answerMessages(request))
	if err != nil {
		return usecase.Answer{}, err
	}
	text := response.Content
	if text == "" {
		text = emptyReply
	}
	return usecase.Answer{Text: text, Model: m.name}, nil
}

func (m *AgentModel) StreamAnswer(ctx context.Context, request usecase.AnswerRequest) (usecase.AnswerStream, error) {
	stream, err := m.model.Stream(ctx, m.answerMessages(request))
	if err != nil {
		return nil, err
	}
	return &directStream{stream: stream, model: m.name}, nil
}

func (m *AgentModel) answerMessages(request usecase.AnswerRequest) []*schema.Message {
	prompt := m.direct
	if request.Route == usecase.RouteEvidence {
		prompt = m.synthesis
	}
	var evidence strings.Builder
	for i, item := range request.Evidence {
		fmt.Fprintf(&evidence, "%d. [%s] %s\n%s\n", i+1, item.Source, item.Title, item.Content)
		if item.URL != "" {
			fmt.Fprintf(&evidence, "来源：%s\n", item.URL)
		}
	}
	input := fmt.Sprintf("【主路由】\n%s\n\n【输出模式】\n%s\n\n【用户问题】\n%s\n\n【规划备注】\n%s\n\n【上下文】\n%s\n\n【证据材料】\n%s", request.Route, request.ResponseMode, request.Message, strings.Join(request.Notes, " | "), firstText(request.Context, "无"), firstText(evidence.String(), "无"))
	return []*schema.Message{schema.SystemMessage(prompt), schema.UserMessage(input)}
}

func fallbackPlan(message string) usecase.Plan {
	return usecase.Plan{Route: usecase.RouteEvidence, ResponseMode: "qa", NeedLocalKnowledge: true, Queries: []string{message}, Notes: []string{"规划失败，回退到本地知识检索。"}}
}

func extractJSON(value string) string {
	value = strings.TrimSpace(value)
	start, end := strings.IndexByte(value, '{'), strings.LastIndexByte(value, '}')
	if start >= 0 && end > start {
		return value[start : end+1]
	}
	return value
}

func firstText(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var _ usecase.Planner = (*AgentModel)(nil)
var _ usecase.ResearchPlanner = (*AgentModel)(nil)
var _ usecase.AnswerModel = (*AgentModel)(nil)
