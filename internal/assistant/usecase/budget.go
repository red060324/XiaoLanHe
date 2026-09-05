package usecase

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

var (
	ErrModelBudget      = errors.New("assistant model-call budget exceeded")
	ErrToolBudget       = errors.New("assistant tool-call budget exceeded")
	ErrDelegationBudget = errors.New("assistant delegation budget exceeded")
	ErrDelegationCycle  = errors.New("assistant delegation cycle or duplicate")
)

type Budget struct {
	mu        sync.Mutex
	limit     entity.BudgetLimit
	usage     entity.BudgetUsage
	delegated map[string]bool
}

func NewBudget(limit entity.BudgetLimit) (*Budget, error) {
	if limit.ModelCalls < 1 || limit.ToolCalls < 0 || limit.Delegations < 0 || limit.TimeoutMilliseconds < 1 {
		return nil, entity.ErrInvalidAgentContract
	}
	return &Budget{limit: limit, delegated: make(map[string]bool)}, nil
}

func (b *Budget) Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, time.Duration(b.limit.TimeoutMilliseconds)*time.Millisecond)
}

func (b *Budget) Constrain(limit entity.BudgetLimit) error {
	if limit.ModelCalls < 1 || limit.ToolCalls < 0 || limit.Delegations < 0 || limit.TimeoutMilliseconds < 1 {
		return entity.ErrInvalidAgentContract
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limit.ModelCalls = min(b.limit.ModelCalls, limit.ModelCalls)
	b.limit.ToolCalls = min(b.limit.ToolCalls, limit.ToolCalls)
	b.limit.Delegations = min(b.limit.Delegations, limit.Delegations)
	b.limit.TimeoutMilliseconds = min(b.limit.TimeoutMilliseconds, limit.TimeoutMilliseconds)
	if b.usage.ModelCalls > b.limit.ModelCalls || b.usage.ToolCalls > b.limit.ToolCalls || b.usage.Delegations > b.limit.Delegations {
		return entity.ErrInvalidAgentContract
	}
	return nil
}

func (b *Budget) TakeModel(ctx context.Context) error {
	return b.take(ctx, &b.usage.ModelCalls, b.limit.ModelCalls, ErrModelBudget)
}

func (b *Budget) TakeTool(ctx context.Context) error {
	return b.take(ctx, &b.usage.ToolCalls, b.limit.ToolCalls, ErrToolBudget)
}

func (b *Budget) BeginDelegation(ctx context.Context, delegate string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.delegated[delegate] {
		return ErrDelegationCycle
	}
	if b.usage.Delegations >= b.limit.Delegations {
		return ErrDelegationBudget
	}
	b.delegated[delegate] = true
	b.usage.Delegations++
	return nil
}

func (b *Budget) Remaining() entity.BudgetLimit {
	b.mu.Lock()
	defer b.mu.Unlock()
	return entity.BudgetLimit{
		ModelCalls:          max(0, b.limit.ModelCalls-b.usage.ModelCalls),
		ToolCalls:           max(0, b.limit.ToolCalls-b.usage.ToolCalls),
		Delegations:         max(0, b.limit.Delegations-b.usage.Delegations),
		TimeoutMilliseconds: b.limit.TimeoutMilliseconds,
	}
}

func (b *Budget) Usage() entity.BudgetUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage
}

func (b *Budget) take(ctx context.Context, used *int, limit int, exhausted error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if *used >= limit {
		return exhausted
	}
	(*used)++
	return nil
}
