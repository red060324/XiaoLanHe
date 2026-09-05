package eval

import (
	"errors"
	"sort"
)

type Thresholds struct {
	RouteSkillAccuracy, FacetCoverage, RecallAt8, CitationCoverage, ProfileConsistency float64
	MaximumRegression                                                                  float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{RouteSkillAccuracy: 0.90, FacetCoverage: 0.85, RecallAt8: 0.80, CitationCoverage: 1, ProfileConsistency: 1, MaximumRegression: 0.02}
}

type Metrics struct {
	RouteSkillAccuracy float64 `json:"routeSkillAccuracy"`
	FacetCoverage      float64 `json:"facetCoverage"`
	RecallAt8          float64 `json:"recallAt8"`
	CitationCoverage   float64 `json:"citationCoverage"`
	ProfileConsistency float64 `json:"profileConsistency"`
	UnsupportedClaims  int     `json:"unsupportedClaims"`
	UnauthorizedCalls  int     `json:"unauthorizedCalls"`
	ForeignSources     int     `json:"foreignSources"`
	HiddenFallbacks    int     `json:"hiddenFallbacks"`
	ModelCalls         int     `json:"modelCalls"`
	ToolCalls          int     `json:"toolCalls"`
	Delegations        int     `json:"delegations"`
	LatencyP50Millis   int64   `json:"fixtureLatencyP50Milliseconds"`
	LatencyP95Millis   int64   `json:"fixtureLatencyP95Milliseconds"`
}

type CaseResult struct {
	ID       string  `json:"id"`
	Baseline Metrics `json:"baseline"`
	Advanced Metrics `json:"advanced"`
}

type Report struct {
	SchemaVersion  int          `json:"schemaVersion"`
	DatasetVersion string       `json:"datasetVersion"`
	Metadata       Metadata     `json:"metadata"`
	Thresholds     Thresholds   `json:"thresholds"`
	Baseline       Metrics      `json:"baseline"`
	Advanced       Metrics      `json:"advanced"`
	Cases          []CaseResult `json:"cases"`
	Passed         bool         `json:"passed"`
	Failures       []string     `json:"failures"`
}

func Run(dataset Dataset, thresholds Thresholds) (Report, error) {
	if err := dataset.Validate(); err != nil {
		return Report{}, err
	}
	if thresholds.RouteSkillAccuracy <= 0 || thresholds.FacetCoverage <= 0 || thresholds.RecallAt8 <= 0 || thresholds.CitationCoverage <= 0 || thresholds.ProfileConsistency <= 0 || thresholds.MaximumRegression < 0 {
		return Report{}, errors.New("invalid eval thresholds")
	}
	report := Report{SchemaVersion: DatasetSchemaVersion, DatasetVersion: dataset.Metadata.DatasetVersion, Metadata: dataset.Metadata, Thresholds: thresholds}
	baselineObservations := make([]Observation, 0, len(dataset.Cases))
	advancedObservations := make([]Observation, 0, len(dataset.Cases))
	for _, item := range dataset.Cases {
		baseline := metricsFor([]Case{item}, func(Case) Observation { return item.Baseline })
		advanced := metricsFor([]Case{item}, func(Case) Observation { return item.Advanced })
		report.Cases = append(report.Cases, CaseResult{ID: item.ID, Baseline: baseline, Advanced: advanced})
		baselineObservations = append(baselineObservations, item.Baseline)
		advancedObservations = append(advancedObservations, item.Advanced)
	}
	report.Baseline = aggregate(dataset.Cases, baselineObservations)
	report.Advanced = aggregate(dataset.Cases, advancedObservations)
	report.Failures = thresholdFailures(report.Baseline, report.Advanced, thresholds)
	report.Passed = len(report.Failures) == 0
	return report, nil
}

func metricsFor(cases []Case, observation func(Case) Observation) Metrics {
	values := make([]Observation, 0, len(cases))
	for _, item := range cases {
		values = append(values, observation(item))
	}
	return aggregate(cases, values)
}

func aggregate(cases []Case, observations []Observation) Metrics {
	var metrics Metrics
	routeMatches, requiredFacets, coveredFacets := 0, 0, 0
	relevantEvidence, recalledEvidence := 0, 0
	factualClaims, citedClaims := 0, 0
	requiredProfile, satisfiedProfile := 0, 0
	latencies := make([]int64, 0, len(cases))
	for index, item := range cases {
		observed := observations[index]
		if observed.Route == item.Expected.Route && observed.SkillID == item.Expected.SkillID {
			routeMatches++
		}
		requiredFacets += len(item.Expected.RequiredFacets)
		coveredFacets += intersectionCount(item.Expected.RequiredFacets, observed.CoveredFacets)
		relevantEvidence += len(item.Expected.RelevantEvidenceIDs)
		recalledEvidence += intersectionCount(item.Expected.RelevantEvidenceIDs, observed.RetrievedEvidenceIDs)
		if item.Expected.CitationsRequired {
			factualClaims += observed.FactualClaims
			citedClaims += observed.CitedClaims
		}
		requiredProfile += len(item.Expected.RequiredProfileFacts)
		satisfiedProfile += intersectionCount(item.Expected.RequiredProfileFacts, observed.ProfileFactsSatisfied)
		metrics.UnsupportedClaims += observed.UnsupportedClaims
		metrics.UnauthorizedCalls += observed.UnauthorizedCalls
		metrics.ForeignSources += observed.ForeignSources
		metrics.HiddenFallbacks += observed.HiddenFallbacks
		metrics.ModelCalls += observed.Usage.ModelCalls
		metrics.ToolCalls += observed.Usage.ToolCalls
		metrics.Delegations += observed.Usage.Delegations
		latencies = append(latencies, observed.LatencyMilliseconds)
	}
	metrics.RouteSkillAccuracy = ratio(routeMatches, len(cases))
	metrics.FacetCoverage = ratio(coveredFacets, requiredFacets)
	metrics.RecallAt8 = ratio(recalledEvidence, relevantEvidence)
	metrics.CitationCoverage = ratio(citedClaims, factualClaims)
	metrics.ProfileConsistency = ratio(satisfiedProfile, requiredProfile)
	metrics.LatencyP50Millis = percentile(latencies, 0.50)
	metrics.LatencyP95Millis = percentile(latencies, 0.95)
	return metrics
}

func thresholdFailures(baseline, advanced Metrics, thresholds Thresholds) []string {
	failures := []string{}
	checks := []struct {
		name                   string
		baseline, actual, want float64
	}{
		{"route_skill_accuracy", baseline.RouteSkillAccuracy, advanced.RouteSkillAccuracy, thresholds.RouteSkillAccuracy},
		{"facet_coverage", baseline.FacetCoverage, advanced.FacetCoverage, thresholds.FacetCoverage},
		{"recall_at_8", baseline.RecallAt8, advanced.RecallAt8, thresholds.RecallAt8},
		{"citation_coverage", baseline.CitationCoverage, advanced.CitationCoverage, thresholds.CitationCoverage},
		{"profile_consistency", baseline.ProfileConsistency, advanced.ProfileConsistency, thresholds.ProfileConsistency},
	}
	for _, check := range checks {
		if check.actual < check.want {
			failures = append(failures, check.name+" below threshold")
		}
		if check.actual+thresholds.MaximumRegression < check.baseline {
			failures = append(failures, check.name+" regressed from baseline")
		}
	}
	if advanced.UnsupportedClaims != 0 {
		failures = append(failures, "unsupported claims detected")
	}
	if advanced.UnauthorizedCalls != 0 {
		failures = append(failures, "unauthorized calls detected")
	}
	if advanced.ForeignSources != 0 {
		failures = append(failures, "foreign sources detected")
	}
	if advanced.HiddenFallbacks != 0 {
		failures = append(failures, "hidden fallback detected")
	}
	return failures
}

func intersectionCount(expected, actual []string) int {
	set := map[string]bool{}
	for _, value := range actual {
		set[value] = true
	}
	count := 0
	for _, value := range expected {
		if set[value] {
			count++
		}
	}
	return count
}
func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 1
	}
	return float64(numerator) / float64(denominator)
}
func percentile(values []int64, quantile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	index := int(float64(len(copyValues)-1)*quantile + 0.999999)
	return copyValues[index]
}
