package usecase

import (
	"context"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
)

type RouterNode interface {
	Route(context.Context, string, string, *Budget) (entity.RouterDecision, error)
}

type QueryPlannerNode interface {
	Plan(context.Context, string, string, skill.Definition, bool, *Budget) (entity.QueryPlan, bool, error)
}
