package usecase

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

var ErrInvalidSummary = errors.New("invalid conversation summary")
var summaryPromptVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type MemoryStore interface {
	PrepareSummary(context.Context, int64, int, int) (entity.SummaryCandidate, bool, error)
	UpdateSummary(context.Context, int64, int64, int64, string, string) (bool, error)
}
type SummaryNode interface {
	Summarize(context.Context, entity.SummaryCandidate) (string, error)
}
type MemoryConfig struct {
	Threshold, SummaryCap, RecentWindow int
	PromptVersion                       string
	Timeout                             time.Duration
}
type MemoryService struct {
	store  MemoryStore
	node   SummaryNode
	config MemoryConfig
}

func NewMemoryService(store MemoryStore, node SummaryNode, config MemoryConfig) (*MemoryService, error) {
	if store == nil || node == nil || config.Threshold <= 0 || config.SummaryCap <= 0 || config.RecentWindow <= 0 || config.Timeout <= 0 || !summaryPromptVersionPattern.MatchString(config.PromptVersion) {
		return nil, ErrInvalidSummary
	}
	return &MemoryService{store: store, node: node, config: config}, nil
}
func (s *MemoryService) Refresh(ctx context.Context, sessionID int64) error {
	started := time.Now()
	outcome := "skipped"
	var refreshErr error
	workingSetRunes, messageCount := 0, 0
	defer func() {
		slog.InfoContext(ctx, "assistant summary refresh completed", "event", "assistant.memory", "operation", "summary_refresh", "session_id", sessionID, "prompt_version", s.config.PromptVersion, "outcome", outcome, "error_class", summaryErrorClass(refreshErr), "latency_ms", time.Since(started).Milliseconds())
		platformmetrics.Default().ObserveMemory(platformmetrics.MemoryObservation{Outcome: outcome, ErrorClass: summaryErrorClass(refreshErr), Duration: time.Since(started), WorkingSetRunes: workingSetRunes, MessageCount: messageCount})
	}()
	if sessionID <= 0 {
		refreshErr = ErrInvalidSummary
		outcome = "invalid"
		return refreshErr
	}
	refreshCtx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	candidate, needed, err := s.store.PrepareSummary(refreshCtx, sessionID, s.config.RecentWindow, s.config.Threshold)
	if err == nil {
		workingSetRunes = RuneCountMessages(candidate.Messages) + utf8.RuneCountInString(candidate.PriorSummary)
		messageCount = len(candidate.Messages)
	}
	if err != nil || !needed {
		refreshErr = err
		if err != nil {
			outcome = "error"
		}
		return refreshErr
	}
	summary, err := s.node.Summarize(refreshCtx, candidate)
	if err != nil {
		refreshErr = err
		outcome = "error"
		return refreshErr
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		refreshErr = ErrInvalidSummary
		outcome = "invalid"
		return refreshErr
	}
	runes := []rune(summary)
	if len(runes) > s.config.SummaryCap {
		summary = string(runes[:s.config.SummaryCap])
	}
	updated, err := s.store.UpdateSummary(refreshCtx, sessionID, candidate.PriorWatermark, candidate.ThroughMessageID, summary, s.config.PromptVersion)
	refreshErr = err
	switch {
	case err != nil:
		outcome = "error"
	case updated:
		outcome = "updated"
	default:
		outcome = "stale"
	}
	return refreshErr
}

func summaryErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrInvalidSummary):
		return "invalid_output"
	default:
		return "dependency"
	}
}
func SummaryInput(candidate entity.SummaryCandidate) string {
	var b strings.Builder
	if candidate.PriorSummary != "" {
		b.WriteString("【已有摘要】\n")
		b.WriteString(candidate.PriorSummary)
		b.WriteString("\n\n")
	}
	b.WriteString("【待归纳对话】\n")
	for _, message := range candidate.Messages {
		role := "用户"
		if message.Role == "assistant" {
			role = "助手"
		}
		b.WriteString(role)
		b.WriteString("：")
		b.WriteString(message.Content)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
func RuneCountMessages(messages []entity.Message) int {
	total := 0
	for _, message := range messages {
		total += utf8.RuneCountInString(message.Content)
	}
	return total
}
