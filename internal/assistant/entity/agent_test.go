package entity

import (
	"errors"
	"strings"
	"testing"
)

func TestRouterDecisionValidate(t *testing.T) {
	valid := RouterDecision{Route: RoutePlanning, Intent: "game_recommendation", SkillID: "recommend_games", SkillVersion: "1.0.0", ResponseMode: "ranked_recommendation"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []RouterDecision{
		{},
		{Route: "WRITE", Intent: valid.Intent, SkillID: valid.SkillID, SkillVersion: valid.SkillVersion, ResponseMode: valid.ResponseMode},
		{Route: valid.Route, Intent: "bad intent", SkillID: valid.SkillID, SkillVersion: valid.SkillVersion, ResponseMode: valid.ResponseMode},
		{Route: valid.Route, Intent: valid.Intent, SkillID: valid.SkillID, SkillVersion: "latest", ResponseMode: valid.ResponseMode},
	}
	for _, value := range invalid {
		if !errors.Is(value.Validate(), ErrInvalidAgentContract) {
			t.Fatalf("accepted %+v", value)
		}
	}
}

func TestQueryPlanValidate(t *testing.T) {
	sources := map[QuerySource]bool{SourceLightRAG: true, SourceCatalog: true, SourceWeb: true}
	modes := map[LightRAGMode]bool{LightRAGMix: true, LightRAGHybrid: true}
	valid := QueryPlan{SchemaVersion: 1, Units: []QueryUnit{{
		ID: "q1", Text: "co-op RPG", Sources: []QuerySource{SourceLightRAG, SourceCatalog},
		LightRAGMode: LightRAGMix, Freshness: "stable", Filters: QueryFilters{Region: "CN", Platforms: []string{"pc"}}, RequiredFacets: []string{"genre"},
	}}}
	if err := valid.Validate(sources, modes, true); err != nil {
		t.Fatal(err)
	}
	cases := map[string]QueryPlan{
		"wrong version":     {SchemaVersion: 2, Units: valid.Units},
		"too many units":    {SchemaVersion: 1, Units: append(append([]QueryUnit{}, valid.Units...), make([]QueryUnit, 8)...)},
		"duplicate queries": {SchemaVersion: 1, Units: append(append([]QueryUnit{}, valid.Units...), valid.Units[0])},
		"oversize text":     {SchemaVersion: 1, Units: []QueryUnit{{ID: "q1", Text: strings.Repeat("界", 101), Sources: []QuerySource{SourceCatalog}, Freshness: "stable"}}},
		"forbidden source":  {SchemaVersion: 1, Units: []QueryUnit{{ID: "q1", Text: "q", Sources: []QuerySource{SourceForum}, Freshness: "stable"}}},
		"web disabled":      {SchemaVersion: 1, Units: []QueryUnit{{ID: "q1", Text: "q", Sources: []QuerySource{SourceWeb}, Freshness: "recent"}}},
		"missing mode":      {SchemaVersion: 1, Units: []QueryUnit{{ID: "q1", Text: "q", Sources: []QuerySource{SourceLightRAG}, Freshness: "stable"}}},
		"unsafe mode":       {SchemaVersion: 1, Units: []QueryUnit{{ID: "q1", Text: "q", Sources: []QuerySource{SourceLightRAG}, LightRAGMode: "bypass", Freshness: "stable"}}},
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			webEnabled := name != "web disabled"
			if !errors.Is(plan.Validate(sources, modes, webEnabled), ErrInvalidAgentContract) {
				t.Fatalf("accepted %+v", plan)
			}
		})
	}
}

func TestEnvelopeValidate(t *testing.T) {
	valid := Envelope{SchemaVersion: 1, RunID: "11111111-1111-4111-8111-111111111111", Sequence: 1, SkillID: "generic_qa", SkillVersion: "1.0.0"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.RunID = "model-controlled"
	if !errors.Is(valid.Validate(), ErrInvalidAgentContract) {
		t.Fatal("accepted invalid run id")
	}
}

func TestWorkerTaskValidationRejectsForeignReferences(t *testing.T) {
	envelope := Envelope{SchemaVersion: 1, RunID: "11111111-1111-4111-8111-111111111111", Sequence: 1, SkillID: "recommend_games", SkillVersion: "1.0.0"}
	plan := QueryPlan{SchemaVersion: 1, Units: []QueryUnit{{ID: "q1", Text: "query", Sources: []QuerySource{SourceCatalog}, Freshness: "stable"}}}
	research := ResearchTask{Envelope: envelope, Objective: "collect games", QueryUnitIDs: []string{"q2"}, AllowedTools: []string{"search_catalog"}}
	if !errors.Is(research.Validate(plan), ErrInvalidAgentContract) {
		t.Fatal("accepted unknown query unit")
	}
	planning := PlanningTask{Envelope: envelope, Goal: "rank games", EvidenceIDs: []string{"ev_foreign"}, AllowedTools: []string{"read_catalog"}}
	if !errors.Is(planning.Validate(map[string]Evidence{}), ErrInvalidAgentContract) {
		t.Fatal("accepted foreign evidence id")
	}
}
