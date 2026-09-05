package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

func TestNewMemoryServiceRejectsInvalidConfiguration(t *testing.T) {
	valid := MemoryConfig{Threshold: 12_000, SummaryCap: 2_000, RecentWindow: 8, PromptVersion: "summary-v1", Timeout: time.Second}
	store := &memoryStoreFake{}
	node := &summaryNodeFake{}
	cases := map[string]struct {
		store MemoryStore
		node  SummaryNode
		cfg   MemoryConfig
	}{
		"nil store":        {nil, node, valid},
		"nil node":         {store, nil, valid},
		"zero threshold":   {store, node, mutateMemoryConfig(valid, func(c *MemoryConfig) { c.Threshold = 0 })},
		"zero cap":         {store, node, mutateMemoryConfig(valid, func(c *MemoryConfig) { c.SummaryCap = 0 })},
		"zero window":      {store, node, mutateMemoryConfig(valid, func(c *MemoryConfig) { c.RecentWindow = 0 })},
		"zero timeout":     {store, node, mutateMemoryConfig(valid, func(c *MemoryConfig) { c.Timeout = 0 })},
		"unsafe version":   {store, node, mutateMemoryConfig(valid, func(c *MemoryConfig) { c.PromptVersion = "bad version" })},
		"oversize version": {store, node, mutateMemoryConfig(valid, func(c *MemoryConfig) { c.PromptVersion = strings.Repeat("v", 65) })},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewMemoryService(tc.store, tc.node, tc.cfg); !errors.Is(err, ErrInvalidSummary) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestMemoryServiceRefresh(t *testing.T) {
	config := MemoryConfig{Threshold: 12_000, SummaryCap: 4, RecentWindow: 8, PromptVersion: "summary-v1", Timeout: time.Second}
	candidate := entity.SummaryCandidate{
		PriorSummary: "偏好策略游戏", PriorWatermark: 10, ThroughMessageID: 20,
		Messages: []entity.Message{{ID: 11, Role: "user", Content: "预算有限"}, {ID: 20, Role: "assistant", Content: "已记录"}},
	}

	t.Run("caps unicode output and advances the exact watermark", func(t *testing.T) {
		store := &memoryStoreFake{candidate: candidate, needed: true}
		node := &summaryNodeFake{summary: " 甲乙丙丁戊 "}
		service, err := NewMemoryService(store, node, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Refresh(context.Background(), 7); err != nil {
			t.Fatal(err)
		}
		if store.prepareSession != 7 || store.prepareWindow != 8 || store.prepareThreshold != 12_000 {
			t.Fatalf("prepare session=%d window=%d threshold=%d", store.prepareSession, store.prepareWindow, store.prepareThreshold)
		}
		if node.calls != 1 || node.candidate.ThroughMessageID != 20 {
			t.Fatalf("node calls=%d candidate=%+v", node.calls, node.candidate)
		}
		if store.update != (memoryUpdate{sessionID: 7, prior: 10, through: 20, summary: "甲乙丙丁", version: "summary-v1"}) {
			t.Fatalf("update=%+v", store.update)
		}
	})

	t.Run("does nothing below threshold", func(t *testing.T) {
		store := &memoryStoreFake{}
		node := &summaryNodeFake{summary: "unused"}
		service, _ := NewMemoryService(store, node, config)
		if err := service.Refresh(context.Background(), 7); err != nil {
			t.Fatal(err)
		}
		if node.calls != 0 || store.update.sessionID != 0 {
			t.Fatalf("node calls=%d update=%+v", node.calls, store.update)
		}
	})

	t.Run("rejects an empty model result", func(t *testing.T) {
		store := &memoryStoreFake{candidate: candidate, needed: true}
		service, _ := NewMemoryService(store, &summaryNodeFake{summary: "  "}, config)
		if err := service.Refresh(context.Background(), 7); !errors.Is(err, ErrInvalidSummary) {
			t.Fatalf("err=%v", err)
		}
		if store.update.sessionID != 0 {
			t.Fatalf("unexpected update=%+v", store.update)
		}
	})

	t.Run("propagates repository and node failures", func(t *testing.T) {
		prepareErr := errors.New("prepare failed")
		service, _ := NewMemoryService(&memoryStoreFake{prepareErr: prepareErr}, &summaryNodeFake{}, config)
		if err := service.Refresh(context.Background(), 7); !errors.Is(err, prepareErr) {
			t.Fatalf("prepare err=%v", err)
		}

		nodeErr := errors.New("model failed")
		service, _ = NewMemoryService(&memoryStoreFake{candidate: candidate, needed: true}, &summaryNodeFake{err: nodeErr}, config)
		if err := service.Refresh(context.Background(), 7); !errors.Is(err, nodeErr) {
			t.Fatalf("node err=%v", err)
		}
	})

	t.Run("applies one timeout to the whole refresh", func(t *testing.T) {
		store := &memoryStoreFake{prepare: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}
		short := config
		short.Timeout = time.Millisecond
		service, _ := NewMemoryService(store, &summaryNodeFake{}, short)
		if err := service.Refresh(context.Background(), 7); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})

	if service, _ := NewMemoryService(&memoryStoreFake{}, &summaryNodeFake{}, config); !errors.Is(service.Refresh(context.Background(), 0), ErrInvalidSummary) {
		t.Fatal("expected invalid session id")
	}
}

func TestSummaryInputAndRuneCount(t *testing.T) {
	candidate := entity.SummaryCandidate{
		PriorSummary: "已有结论",
		Messages:     []entity.Message{{Role: "user", Content: "你好"}, {Role: "assistant", Content: "hello"}},
	}
	want := "【已有摘要】\n已有结论\n\n【待归纳对话】\n用户：你好\n助手：hello"
	if got := SummaryInput(candidate); got != want {
		t.Fatalf("input=%q", got)
	}
	if got := RuneCountMessages(candidate.Messages); got != 7 {
		t.Fatalf("runes=%d", got)
	}
}

func mutateMemoryConfig(config MemoryConfig, mutate func(*MemoryConfig)) MemoryConfig {
	mutate(&config)
	return config
}

type memoryUpdate struct {
	sessionID, prior, through int64
	summary, version          string
}

type memoryStoreFake struct {
	candidate                       entity.SummaryCandidate
	needed                          bool
	prepareErr, updateErr           error
	prepare                         func(context.Context) error
	prepareSession                  int64
	prepareWindow, prepareThreshold int
	update                          memoryUpdate
}

func (s *memoryStoreFake) PrepareSummary(ctx context.Context, sessionID int64, window, threshold int) (entity.SummaryCandidate, bool, error) {
	s.prepareSession, s.prepareWindow, s.prepareThreshold = sessionID, window, threshold
	if s.prepare != nil {
		if err := s.prepare(ctx); err != nil {
			return entity.SummaryCandidate{}, false, err
		}
	}
	return s.candidate, s.needed, s.prepareErr
}

func (s *memoryStoreFake) UpdateSummary(_ context.Context, sessionID, prior, through int64, summary, version string) (bool, error) {
	s.update = memoryUpdate{sessionID: sessionID, prior: prior, through: through, summary: summary, version: version}
	return s.updateErr == nil, s.updateErr
}

type summaryNodeFake struct {
	summary   string
	err       error
	calls     int
	candidate entity.SummaryCandidate
}

func (n *summaryNodeFake) Summarize(_ context.Context, candidate entity.SummaryCandidate) (string, error) {
	n.calls++
	n.candidate = candidate
	return n.summary, n.err
}
