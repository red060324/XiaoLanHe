package entity

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrInvalidAgentContract = errors.New("invalid agent contract")

const AgentSchemaVersion = 1

type Route string

const (
	RouteDirect   Route = "DIRECT"
	RouteClarify  Route = "CLARIFY"
	RouteResearch Route = "RESEARCH"
	RoutePlanning Route = "PLANNING"
)

func (r Route) Valid() bool {
	return r == RouteDirect || r == RouteClarify || r == RouteResearch || r == RoutePlanning
}

type RouterDecision struct {
	Route        Route  `json:"route"`
	Intent       string `json:"intent"`
	SkillID      string `json:"skillId"`
	SkillVersion string `json:"skillVersion"`
	ResponseMode string `json:"responseMode"`
}

func (d RouterDecision) Validate() error {
	if !d.Route.Valid() || !token(d.Intent, 64) || !token(d.SkillID, 64) || !version(d.SkillVersion) || !token(d.ResponseMode, 64) {
		return ErrInvalidAgentContract
	}
	return nil
}

type QuerySource string

const (
	SourceLightRAG QuerySource = "lightrag"
	SourceCatalog  QuerySource = "catalog"
	SourceForum    QuerySource = "forum"
	SourceWeb      QuerySource = "web"
)

func (s QuerySource) Valid() bool {
	return s == SourceLightRAG || s == SourceCatalog || s == SourceForum || s == SourceWeb
}

type LightRAGMode string

const (
	LightRAGLocal  LightRAGMode = "local"
	LightRAGGlobal LightRAGMode = "global"
	LightRAGHybrid LightRAGMode = "hybrid"
	LightRAGMix    LightRAGMode = "mix"
)

func (m LightRAGMode) Valid() bool {
	return m == LightRAGLocal || m == LightRAGGlobal || m == LightRAGHybrid || m == LightRAGMix
}

type QueryFilters struct {
	Region    string   `json:"region,omitempty"`
	Platforms []string `json:"platforms,omitempty"`
	GameCode  string   `json:"gameCode,omitempty"`
}

type QueryUnit struct {
	ID             string        `json:"id"`
	Text           string        `json:"text"`
	Sources        []QuerySource `json:"sources"`
	LightRAGMode   LightRAGMode  `json:"lightragMode,omitempty"`
	Freshness      string        `json:"freshness"`
	Filters        QueryFilters  `json:"filters"`
	RequiredFacets []string      `json:"requiredFacets"`
}

type QueryPlan struct {
	SchemaVersion int         `json:"schemaVersion"`
	Units         []QueryUnit `json:"units"`
}

func (p QueryPlan) Validate(allowedSources map[QuerySource]bool, allowedModes map[LightRAGMode]bool, webEnabled bool) error {
	if p.SchemaVersion != AgentSchemaVersion || len(p.Units) < 1 || len(p.Units) > 8 {
		return ErrInvalidAgentContract
	}
	ids := make(map[string]bool, len(p.Units))
	texts := make(map[string]bool, len(p.Units))
	for _, unit := range p.Units {
		unit.ID = strings.TrimSpace(unit.ID)
		unit.Text = strings.TrimSpace(unit.Text)
		if !token(unit.ID, 32) || utf8.RuneCountInString(unit.Text) < 1 || utf8.RuneCountInString(unit.Text) > 100 || ids[unit.ID] || texts[strings.ToLower(unit.Text)] {
			return ErrInvalidAgentContract
		}
		ids[unit.ID], texts[strings.ToLower(unit.Text)] = true, true
		if unit.Freshness != "stable" && unit.Freshness != "recent" {
			return ErrInvalidAgentContract
		}
		if len(unit.Sources) < 1 || len(unit.Sources) > 4 || len(unit.RequiredFacets) > 8 || len(unit.Filters.Platforms) > 5 || !optionalToken(unit.Filters.Region, 16) || !optionalToken(unit.Filters.GameCode, 64) {
			return ErrInvalidAgentContract
		}
		sources := make(map[QuerySource]bool, len(unit.Sources))
		hasLightRAG := false
		for _, source := range unit.Sources {
			if !source.Valid() || !allowedSources[source] || sources[source] || source == SourceWeb && !webEnabled {
				return ErrInvalidAgentContract
			}
			sources[source] = true
			hasLightRAG = hasLightRAG || source == SourceLightRAG
		}
		if hasLightRAG != (unit.LightRAGMode != "") || hasLightRAG && (!unit.LightRAGMode.Valid() || !allowedModes[unit.LightRAGMode]) {
			return ErrInvalidAgentContract
		}
		if !validTokens(unit.Filters.Platforms, 5, 32) || !validTokens(unit.RequiredFacets, 8, 32) {
			return ErrInvalidAgentContract
		}
	}
	return nil
}

type BudgetLimit struct {
	ModelCalls          int   `json:"modelCalls"`
	ToolCalls           int   `json:"toolCalls"`
	Delegations         int   `json:"delegations"`
	TimeoutMilliseconds int64 `json:"timeoutMilliseconds"`
}

type BudgetUsage struct {
	ModelCalls  int `json:"modelCalls"`
	ToolCalls   int `json:"toolCalls"`
	Delegations int `json:"delegations"`
}

type Envelope struct {
	SchemaVersion int
	RunID         string
	Sequence      int
	SkillID       string
	SkillVersion  string
}

type ArtifactStatus string

const (
	StatusComplete    ArtifactStatus = "complete"
	StatusPartial     ArtifactStatus = "partial"
	StatusNoResult    ArtifactStatus = "no_result"
	StatusBounded     ArtifactStatus = "bounded"
	StatusUnavailable ArtifactStatus = "unavailable"
)

type Evidence struct {
	ID, Source, Title, Content, URL string
	Score                           float64
}

type ResearchTask struct {
	Envelope
	Objective      string
	QueryUnitIDs   []string
	RequiredFacets []string
	AllowedTools   []string
}

type ResearchArtifact struct {
	Envelope
	Status                       ArtifactStatus
	EvidenceIDs                  []string
	CoveredFacets, MissingFacets []string
	Assumptions                  []string
	Usage                        BudgetUsage
	StopReason                   string
}

type Constraints struct {
	Region        string   `json:"region,omitempty"`
	Platforms     []string `json:"platforms,omitempty"`
	MaxPriceMinor *int64   `json:"maxPriceMinor,omitempty"`
	Currency      string   `json:"currency,omitempty"`
}

type PlanningTask struct {
	Envelope
	Goal                 string
	Constraints          Constraints
	PreferenceProjection map[string][]string
	EvidenceIDs          []string
	AllowedTools         []string
	UserID               int64
}

type PlanItem struct {
	SubjectID                 string
	Recommendation            string
	MatchedConstraints        []string
	UnmetConstraints          []string
	Assumptions, Alternatives []string
	EvidenceIDs               []string
}

type PlanningArtifact struct {
	Envelope
	Status     ArtifactStatus
	Items      []PlanItem
	Usage      BudgetUsage
	StopReason string
}

func (a ResearchArtifact) Validate(task ResearchTask, knownEvidence map[string]Evidence) error {
	if err := a.Envelope.Validate(); err != nil || a.RunID != task.RunID || a.Sequence != task.Sequence || a.SkillID != task.SkillID || a.SkillVersion != task.SkillVersion || !a.Status.Valid() || len(a.EvidenceIDs) > 32 || !validTokens(a.CoveredFacets, 8, 32) || !validTokens(a.MissingFacets, 8, 32) || len(a.Assumptions) > 8 || !validStopReason(a.StopReason) {
		return ErrInvalidAgentContract
	}
	facets := make(map[string]bool, len(task.RequiredFacets))
	for _, value := range task.RequiredFacets {
		facets[value] = false
	}
	for _, values := range [][]string{a.CoveredFacets, a.MissingFacets} {
		for _, value := range values {
			covered, exists := facets[value]
			if !exists || covered {
				return ErrInvalidAgentContract
			}
			facets[value] = true
		}
	}
	for _, classified := range facets {
		if !classified {
			return ErrInvalidAgentContract
		}
	}
	for _, id := range a.EvidenceIDs {
		if _, ok := knownEvidence[id]; !ok {
			return ErrInvalidAgentContract
		}
	}
	return nil
}

func (a PlanningArtifact) Validate(task PlanningTask, knownEvidence map[string]Evidence) error {
	if err := a.Envelope.Validate(); err != nil || a.RunID != task.RunID || a.Sequence != task.Sequence || a.SkillID != task.SkillID || a.SkillVersion != task.SkillVersion || !a.Status.Valid() || len(a.Items) > 10 || !validStopReason(a.StopReason) {
		return ErrInvalidAgentContract
	}
	if a.Status == StatusComplete && len(a.Items) == 0 {
		return ErrInvalidAgentContract
	}
	for _, item := range a.Items {
		if !token(strings.ToLower(strings.TrimSpace(item.SubjectID)), 64) || utf8.RuneCountInString(strings.TrimSpace(item.Recommendation)) < 1 || utf8.RuneCountInString(item.Recommendation) > 500 || len(item.EvidenceIDs) < 1 {
			return ErrInvalidAgentContract
		}
		for _, id := range item.EvidenceIDs {
			if _, ok := knownEvidence[id]; !ok {
				return ErrInvalidAgentContract
			}
		}
	}
	return nil
}

func (s ArtifactStatus) Valid() bool {
	return s == StatusComplete || s == StatusPartial || s == StatusNoResult || s == StatusBounded || s == StatusUnavailable
}

func validStopReason(value string) bool {
	switch value {
	case "complete", "max_iterations", "max_model_calls", "max_tool_calls", "max_delegations", "deadline", "cancelled", "invalid_output", "dependency_unavailable", "no_evidence":
		return true
	default:
		return false
	}
}

func (t ResearchTask) Validate(plan QueryPlan) error {
	if err := t.Envelope.Validate(); err != nil || utf8.RuneCountInString(strings.TrimSpace(t.Objective)) < 1 || utf8.RuneCountInString(t.Objective) > 500 || len(t.QueryUnitIDs) < 1 || len(t.QueryUnitIDs) > 8 || !validTokens(t.QueryUnitIDs, 8, 32) || !validTokens(t.RequiredFacets, 8, 32) || !validTokens(t.AllowedTools, 8, 64) {
		return ErrInvalidAgentContract
	}
	unitIDs := make(map[string]bool, len(plan.Units))
	for _, unit := range plan.Units {
		unitIDs[unit.ID] = true
	}
	for _, id := range t.QueryUnitIDs {
		if !unitIDs[id] {
			return ErrInvalidAgentContract
		}
	}
	return nil
}

func (t PlanningTask) Validate(knownEvidence map[string]Evidence) error {
	if err := t.Envelope.Validate(); err != nil || utf8.RuneCountInString(strings.TrimSpace(t.Goal)) < 1 || utf8.RuneCountInString(t.Goal) > 500 || len(t.EvidenceIDs) > 32 || !validTokens(t.AllowedTools, 8, 64) || len(t.Constraints.Platforms) > 5 {
		return ErrInvalidAgentContract
	}
	for _, id := range t.EvidenceIDs {
		if _, ok := knownEvidence[id]; !ok {
			return ErrInvalidAgentContract
		}
	}
	return nil
}

func (e Envelope) Validate() error {
	if e.SchemaVersion != AgentSchemaVersion || !runIDPattern.MatchString(e.RunID) || e.Sequence < 1 || !token(e.SkillID, 64) || !version(e.SkillVersion) {
		return ErrInvalidAgentContract
	}
	return nil
}

var (
	tokenPattern   = regexp.MustCompile(`^[a-z][a-z0-9_:-]{0,63}$`)
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	runIDPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

func token(value string, max int) bool {
	value = strings.TrimSpace(value)
	return utf8.RuneCountInString(value) <= max && tokenPattern.MatchString(value)
}

func optionalToken(value string, max int) bool {
	return value == "" || token(strings.ToLower(value), max)
}

func version(value string) bool { return versionPattern.MatchString(strings.TrimSpace(value)) }

func validTokens(values []string, maxItems, maxRunes int) bool {
	if len(values) > maxItems {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if !token(key, maxRunes) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
