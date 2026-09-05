package eval

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/assistant/skill"
)

const (
	DatasetSchemaVersion = 1
	maxFixtureLineBytes  = 256 << 10
)

//go:embed testdata/*.jsonl
var fixtureFS embed.FS

type Metadata struct {
	RecordType           string            `json:"recordType"`
	SchemaVersion        int               `json:"schemaVersion"`
	DatasetVersion       string            `json:"datasetVersion"`
	ApplicationVersion   string            `json:"applicationVersion"`
	RouterPromptVersion  string            `json:"routerPromptVersion"`
	PlannerPromptVersion string            `json:"plannerPromptVersion"`
	LightRAGCoreVersion  string            `json:"lightragCoreVersion"`
	LightRAGAPIVersion   string            `json:"lightragApiVersion"`
	LightRAGImage        string            `json:"lightragImage"`
	LightRAGStores       []string          `json:"lightragStores"`
	ModelVersion         string            `json:"modelVersion"`
	EmbeddingVersion     string            `json:"embeddingVersion"`
	SkillVersions        map[string]string `json:"skillVersions"`
	SkillPromptVersions  map[string]string `json:"skillPromptVersions"`
}

type Expected struct {
	Route                entity.Route `json:"route"`
	SkillID              string       `json:"skillId"`
	RequiredFacets       []string     `json:"requiredFacets"`
	RelevantEvidenceIDs  []string     `json:"relevantEvidenceIds"`
	RequiredProfileFacts []string     `json:"requiredProfileFacts"`
	CitationsRequired    bool         `json:"citationsRequired"`
}

type Usage struct {
	ModelCalls, ToolCalls, Delegations int
}

type Observation struct {
	Route                 entity.Route `json:"route"`
	SkillID               string       `json:"skillId"`
	CoveredFacets         []string     `json:"coveredFacets"`
	RetrievedEvidenceIDs  []string     `json:"retrievedEvidenceIds"`
	FactualClaims         int          `json:"factualClaims"`
	CitedClaims           int          `json:"citedClaims"`
	ProfileFactsSatisfied []string     `json:"profileFactsSatisfied"`
	UnsupportedClaims     int          `json:"unsupportedClaims"`
	UnauthorizedCalls     int          `json:"unauthorizedCalls"`
	ForeignSources        int          `json:"foreignSources"`
	HiddenFallbacks       int          `json:"hiddenFallbacks"`
	Usage                 Usage        `json:"usage"`
	LatencyMilliseconds   int64        `json:"latencyMilliseconds"`
}

type Case struct {
	RecordType string      `json:"recordType"`
	ID         string      `json:"id"`
	Expected   Expected    `json:"expected"`
	Baseline   Observation `json:"baseline"`
	Advanced   Observation `json:"advanced"`
}

type Dataset struct {
	Metadata Metadata
	Cases    []Case
}

func LoadDefault() (Dataset, error) {
	body, err := fixtureFS.ReadFile("testdata/advanced_ai_v1.jsonl")
	if err != nil {
		return Dataset{}, err
	}
	return Load(bytes.NewReader(body))
}

func Load(reader io.Reader) (Dataset, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxFixtureLineBytes)
	dataset := Dataset{}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var header struct {
			RecordType string `json:"recordType"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return Dataset{}, fmt.Errorf("eval fixture line %d: invalid JSON", lineNumber)
		}
		switch header.RecordType {
		case "metadata":
			if dataset.Metadata.RecordType != "" || len(dataset.Cases) > 0 {
				return Dataset{}, fmt.Errorf("eval fixture line %d: metadata must be first and unique", lineNumber)
			}
			if err := strictDecode(line, &dataset.Metadata); err != nil {
				return Dataset{}, fmt.Errorf("eval fixture line %d: %w", lineNumber, err)
			}
		case "case":
			var item Case
			if err := strictDecode(line, &item); err != nil {
				return Dataset{}, fmt.Errorf("eval fixture line %d: %w", lineNumber, err)
			}
			dataset.Cases = append(dataset.Cases, item)
		default:
			return Dataset{}, fmt.Errorf("eval fixture line %d: unknown record type", lineNumber)
		}
	}
	if err := scanner.Err(); err != nil {
		return Dataset{}, fmt.Errorf("read eval fixture: %w", err)
	}
	if err := dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func (d Dataset) Validate() error {
	m := d.Metadata
	if m.RecordType != "metadata" || m.SchemaVersion != DatasetSchemaVersion || !safeVersion(m.DatasetVersion) || !safeVersion(m.ApplicationVersion) || !safeVersion(m.RouterPromptVersion) || !safeVersion(m.PlannerPromptVersion) || m.LightRAGCoreVersion == "" || m.LightRAGAPIVersion == "" || m.LightRAGImage == "" || m.ModelVersion == "" || m.EmbeddingVersion == "" || len(m.LightRAGStores) != 4 || len(m.SkillVersions) != 4 || len(m.SkillPromptVersions) != 4 || len(d.Cases) == 0 {
		return errors.New("invalid eval metadata")
	}
	ids := map[string]bool{}
	for _, item := range d.Cases {
		if item.RecordType != "case" || !safeVersion(item.ID) || ids[item.ID] || !item.Expected.Route.Valid() || item.Expected.SkillID == "" {
			return fmt.Errorf("invalid eval case %q", item.ID)
		}
		ids[item.ID] = true
		if !unique(item.Expected.RequiredFacets) || !unique(item.Expected.RelevantEvidenceIDs) || !unique(item.Expected.RequiredProfileFacts) || validateObservation(item.Baseline) != nil || validateObservation(item.Advanced) != nil {
			return fmt.Errorf("invalid eval case %q", item.ID)
		}
	}
	return nil
}

func (d Dataset) ValidateRegistry(registry *skill.Registry) error {
	if registry == nil {
		return errors.New("eval skill registry is required")
	}
	declared := []string{}
	for _, definition := range registry.All() {
		if d.Metadata.SkillVersions[definition.ID] != definition.Version || d.Metadata.SkillPromptVersions[definition.ID] != definition.PromptVersion {
			return fmt.Errorf("eval metadata does not pin skill %s", definition.ID)
		}
		declared = append(declared, definition.EvalCases...)
	}
	actual := make([]string, 0, len(d.Cases))
	for _, item := range d.Cases {
		definition, err := registry.Resolve(item.Expected.SkillID, "1.0.0")
		if err != nil || !contains(definition.EvalCases, item.ID) {
			return fmt.Errorf("eval case %s is not owned by skill %s", item.ID, item.Expected.SkillID)
		}
		actual = append(actual, item.ID)
	}
	sort.Strings(declared)
	sort.Strings(actual)
	if strings.Join(declared, "\x00") != strings.Join(actual, "\x00") {
		return errors.New("eval cases do not exactly match skill declarations")
	}
	return nil
}

func validateObservation(value Observation) error {
	if !value.Route.Valid() || value.SkillID == "" || !unique(value.CoveredFacets) || !unique(value.RetrievedEvidenceIDs) || len(value.RetrievedEvidenceIDs) > 8 || !unique(value.ProfileFactsSatisfied) || value.FactualClaims < 0 || value.CitedClaims < 0 || value.CitedClaims > value.FactualClaims || value.UnsupportedClaims < 0 || value.UnauthorizedCalls < 0 || value.ForeignSources < 0 || value.HiddenFallbacks < 0 || value.Usage.ModelCalls < 0 || value.Usage.ModelCalls > 12 || value.Usage.ToolCalls < 0 || value.Usage.ToolCalls > 12 || value.Usage.Delegations < 0 || value.Usage.Delegations > 3 || value.LatencyMilliseconds < 0 {
		return errors.New("invalid eval observation")
	}
	return nil
}

func strictDecode(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing eval JSON")
	}
	return nil
}

func safeVersion(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n")
}
func unique(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if !safeVersion(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
