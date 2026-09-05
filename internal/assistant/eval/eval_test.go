package eval

import (
	"strings"
	"testing"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
)

func TestDefaultDatasetMatchesSkillsAndPassesThresholds(t *testing.T) {
	dataset, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := skill.Load(entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 45_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := dataset.ValidateRegistry(registry); err != nil {
		t.Fatal(err)
	}
	report, err := Run(dataset, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || len(report.Cases) != 8 || len(report.Failures) != 0 {
		t.Fatalf("report=%+v", report)
	}
	if report.Advanced.RouteSkillAccuracy != 1 || report.Advanced.FacetCoverage != 1 || report.Advanced.RecallAt8 != 1 || report.Advanced.CitationCoverage != 1 || report.Advanced.ProfileConsistency != 1 {
		t.Fatalf("advanced metrics=%+v", report.Advanced)
	}
	if report.Baseline.FacetCoverage >= report.Advanced.FacetCoverage || report.Baseline.RecallAt8 >= report.Advanced.RecallAt8 {
		t.Fatalf("baseline=%+v advanced=%+v", report.Baseline, report.Advanced)
	}
	if report.Advanced.ModelCalls <= report.Baseline.ModelCalls || report.Advanced.Delegations <= report.Baseline.Delegations {
		t.Fatalf("expected the hierarchical candidate cost to be measured: baseline=%+v advanced=%+v", report.Baseline, report.Advanced)
	}
}

func TestEvalGateRejectsSafetyViolationAndRegression(t *testing.T) {
	dataset, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Cases[0].Advanced.Route = entity.RouteResearch
	dataset.Cases[0].Advanced.UnauthorizedCalls = 1
	report, err := Run(dataset, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || !containsText(report.Failures, "route_skill_accuracy below threshold") || !containsText(report.Failures, "unauthorized calls detected") {
		t.Fatalf("failures=%v", report.Failures)
	}
}

func TestEvalDatasetStrictValidation(t *testing.T) {
	for _, fixture := range []string{
		`{"recordType":"unknown"}`,
		`{"recordType":"metadata","unknown":true}`,
		strings.Repeat("x", maxFixtureLineBytes+1),
	} {
		if _, err := Load(strings.NewReader(fixture)); err == nil {
			t.Fatalf("expected invalid fixture for %.32q", fixture)
		}
	}
}

func containsText(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
