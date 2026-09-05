package eino

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

const summaryInstruction = "你是会话摘要节点。只保留后续对话所需的用户明确偏好、约束、已确认事实、未决问题和重要结论；不要添加推测，不要输出标题、Markdown 或过程解释。"

type SummaryNode struct{ model model.BaseChatModel }

func NewSummaryNode(chatModel model.BaseChatModel) *SummaryNode {
	return &SummaryNode{model: chatModel}
}
func (s *SummaryNode) Summarize(ctx context.Context, candidate entity.SummaryCandidate) (string, error) {
	response, err := s.model.Generate(ctx, []*schema.Message{schema.SystemMessage(summaryInstruction), schema.UserMessage(assistant.SummaryInput(candidate))})
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(response.Content)
	if summary == "" {
		return "", errors.New("summary model returned empty content")
	}
	return summary, nil
}

var _ assistant.SummaryNode = (*SummaryNode)(nil)
