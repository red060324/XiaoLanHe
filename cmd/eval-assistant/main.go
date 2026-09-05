package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistanteval "github.com/red060324/XiaoLanHe/internal/assistant/eval"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
)

func main() {
	dataset, err := assistanteval.LoadDefault()
	if err != nil {
		fail(err)
	}
	registry, err := skill.Load(entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 45_000})
	if err != nil {
		fail(err)
	}
	if err := dataset.ValidateRegistry(registry); err != nil {
		fail(err)
	}
	report, err := assistanteval.Run(dataset, assistanteval.DefaultThresholds())
	if err != nil {
		fail(err)
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(body))
	if !report.Passed {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "assistant eval:", err)
	os.Exit(2)
}
