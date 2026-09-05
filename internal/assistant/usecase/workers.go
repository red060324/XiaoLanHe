package usecase

import (
	"context"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
)

type ResearchWorkerResult struct {
	Artifact entity.ResearchArtifact
	Evidence []entity.Evidence
}

type PlanningWorkerResult struct {
	Artifact entity.PlanningArtifact
	Evidence []entity.Evidence
}

type ResearchWorker interface {
	RunResearch(context.Context, entity.ResearchTask, entity.QueryPlan, *Budget) (ResearchWorkerResult, error)
}

type PlanningWorker interface {
	RunPlanning(context.Context, entity.PlanningTask, *Budget, *EvidenceStore) (PlanningWorkerResult, error)
}

type CopilotInput struct {
	RunID      string
	Message    string
	Context    string
	UserID     int64
	Profile    entity.Profile
	Decision   entity.RouterDecision
	Plan       entity.QueryPlan
	Skill      skill.Definition
	Budget     *Budget
	WebEnabled bool
}

type CopilotResult struct {
	Evidence []entity.Evidence
	Plan     *entity.PlanningArtifact
	Notes    []string
	Usage    entity.BudgetUsage
}

type Copilot interface {
	Run(context.Context, CopilotInput) (CopilotResult, error)
}
