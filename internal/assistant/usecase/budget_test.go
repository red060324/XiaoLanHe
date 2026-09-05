package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

func TestBudgetConcurrentLimits(t *testing.T) {
	budget, err := NewBudget(entity.BudgetLimit{ModelCalls: 5, ToolCalls: 7, Delegations: 2, TimeoutMilliseconds: 100})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	modelOK, toolOK := 0, 0
	for range 30 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if budget.TakeModel(context.Background()) == nil {
				mu.Lock()
				modelOK++
				mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			if budget.TakeTool(context.Background()) == nil {
				mu.Lock()
				toolOK++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if modelOK != 5 || toolOK != 7 || budget.Usage() != (entity.BudgetUsage{ModelCalls: 5, ToolCalls: 7}) {
		t.Fatalf("model=%d tool=%d usage=%+v", modelOK, toolOK, budget.Usage())
	}
	if !errors.Is(budget.TakeModel(context.Background()), ErrModelBudget) || !errors.Is(budget.TakeTool(context.Background()), ErrToolBudget) {
		t.Fatal("expected exhausted budgets")
	}
}

func TestBudgetDelegationAndDeadline(t *testing.T) {
	budget, _ := NewBudget(entity.BudgetLimit{ModelCalls: 1, ToolCalls: 1, Delegations: 1, TimeoutMilliseconds: 1})
	if err := budget.BeginDelegation(context.Background(), "research"); err != nil {
		t.Fatal(err)
	}
	if err := budget.BeginDelegation(context.Background(), "research"); !errors.Is(err, ErrDelegationCycle) {
		t.Fatalf("cycle err=%v", err)
	}
	if err := budget.BeginDelegation(context.Background(), "planning"); !errors.Is(err, ErrDelegationBudget) {
		t.Fatalf("budget err=%v", err)
	}
	ctx, cancel := budget.Context(context.Background())
	defer cancel()
	<-ctx.Done()
	if !errors.Is(budget.TakeModel(ctx), context.DeadlineExceeded) {
		t.Fatal("expected deadline")
	}
}

func TestBudgetConstrainAppliesSkillLimit(t *testing.T) {
	budget, _ := NewBudget(entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 45_000})
	if err := budget.TakeModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := budget.Constrain(entity.BudgetLimit{ModelCalls: 2, ToolCalls: 1, Delegations: 1, TimeoutMilliseconds: 10_000}); err != nil {
		t.Fatal(err)
	}
	if err := budget.TakeModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(budget.TakeModel(context.Background()), ErrModelBudget) {
		t.Fatal("skill model limit was not applied")
	}
	if remaining := budget.Remaining(); remaining.ToolCalls != 1 || remaining.Delegations != 1 || remaining.TimeoutMilliseconds != 10_000 {
		t.Fatalf("remaining=%+v", remaining)
	}
}
