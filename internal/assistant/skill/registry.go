package skill

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

var (
	ErrInvalidSkill  = errors.New("invalid assistant skill")
	ErrSkillNotFound = errors.New("assistant skill not found")
)

const maxDefinitionBytes = 16 << 10

//go:embed definitions/*.json
var definitions embed.FS

type Definition struct {
	ID             string                `json:"id"`
	Version        string                `json:"version"`
	PromptVersion  string                `json:"promptVersion"`
	Intents        []string              `json:"intents"`
	Routes         []entity.Route        `json:"routes"`
	Delegates      []string              `json:"delegates"`
	Tools          []string              `json:"tools"`
	LightRAGModes  []entity.LightRAGMode `json:"lightragModes"`
	Budget         entity.BudgetLimit    `json:"budget"`
	OutputContract string                `json:"outputContract"`
	Citations      bool                  `json:"citations"`
	EvalCases      []string              `json:"evalCases"`
}

type Registry struct{ byKey map[string]Definition }

func Load(max entity.BudgetLimit) (*Registry, error) { return loadFS(definitions, max) }

func loadFS(source fs.FS, max entity.BudgetLimit) (*Registry, error) {
	entries, err := fs.ReadDir(source, "definitions")
	if err != nil {
		return nil, fmt.Errorf("%w: read definitions", ErrInvalidSkill)
	}
	registry := &Registry{byKey: make(map[string]Definition, len(entries))}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, err := fs.ReadFile(source, "definitions/"+entry.Name())
		if err != nil || len(body) == 0 || len(body) > maxDefinitionBytes {
			return nil, fmt.Errorf("%w: %s size", ErrInvalidSkill, entry.Name())
		}
		var definition Definition
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&definition); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalidSkill, entry.Name(), err)
		}
		if trailingErr := decoder.Decode(&struct{}{}); !errors.Is(trailingErr, io.EOF) || validate(definition, max) != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidSkill, entry.Name())
		}
		key := definition.ID + "@" + definition.Version
		if _, exists := registry.byKey[key]; exists {
			return nil, fmt.Errorf("%w: duplicate %s", ErrInvalidSkill, key)
		}
		registry.byKey[key] = definition
	}
	if len(registry.byKey) != 4 {
		return nil, fmt.Errorf("%w: expected four definitions", ErrInvalidSkill)
	}
	return registry, nil
}

func (r *Registry) Resolve(id, version string) (Definition, error) {
	definition, ok := r.byKey[strings.TrimSpace(id)+"@"+strings.TrimSpace(version)]
	if !ok {
		return Definition{}, ErrSkillNotFound
	}
	return clone(definition), nil
}

func (r *Registry) All() []Definition {
	result := make([]Definition, 0, len(r.byKey))
	for _, definition := range r.byKey {
		result = append(result, clone(definition))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (d Definition) Supports(decision entity.RouterDecision) bool {
	if decision.SkillID != d.ID || decision.SkillVersion != d.Version || !contains(d.Intents, decision.Intent) {
		return false
	}
	for _, route := range d.Routes {
		if route == decision.Route {
			return true
		}
	}
	return false
}

func (d Definition) AllowsDelegate(value string) bool { return contains(d.Delegates, value) }
func (d Definition) AllowsTool(value string) bool     { return contains(d.Tools, value) }
func (d Definition) AllowedSources() map[entity.QuerySource]bool {
	result := map[entity.QuerySource]bool{}
	for _, tool := range d.Tools {
		switch tool {
		case "search_lightrag":
			result[entity.SourceLightRAG] = true
		case "search_catalog", "read_catalog":
			result[entity.SourceCatalog] = true
		case "search_forum":
			result[entity.SourceForum] = true
		case "search_web":
			result[entity.SourceWeb] = true
		}
	}
	return result
}
func (d Definition) AllowedModes() map[entity.LightRAGMode]bool {
	result := map[entity.LightRAGMode]bool{}
	for _, mode := range d.LightRAGModes {
		result[mode] = true
	}
	return result
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var semver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func validate(d Definition, max entity.BudgetLimit) error {
	if !identifier.MatchString(d.ID) || !semver.MatchString(d.Version) || !identifier.MatchString(d.PromptVersion) || len(d.Intents) == 0 || len(d.Intents) > 8 || len(d.Routes) == 0 || len(d.Routes) > 4 || len(d.Delegates) > 2 || len(d.Tools) > 8 || len(d.EvalCases) == 0 || len(d.EvalCases) > 16 || !identifier.MatchString(d.OutputContract) {
		return ErrInvalidSkill
	}
	if d.Budget.ModelCalls < 1 || d.Budget.ToolCalls < 0 || d.Budget.Delegations < 0 || d.Budget.TimeoutMilliseconds < 1 || d.Budget.ModelCalls > max.ModelCalls || d.Budget.ToolCalls > max.ToolCalls || d.Budget.Delegations > max.Delegations || d.Budget.TimeoutMilliseconds > max.TimeoutMilliseconds {
		return ErrInvalidSkill
	}
	if !uniqueIdentifiers(d.Intents) || !uniqueIdentifiers(d.Delegates) || !uniqueIdentifiers(d.Tools) || !uniqueIdentifiers(d.EvalCases) {
		return ErrInvalidSkill
	}
	routes := map[entity.Route]bool{}
	for _, route := range d.Routes {
		if !route.Valid() || routes[route] {
			return ErrInvalidSkill
		}
		routes[route] = true
	}
	delegates := map[string]bool{}
	for _, delegate := range d.Delegates {
		if delegate != "research" && delegate != "planning" {
			return ErrInvalidSkill
		}
		delegates[delegate] = true
	}
	allowedTools := map[string]string{
		"search_lightrag": "research", "search_catalog": "research", "search_forum": "research", "search_web": "research",
		"read_catalog": "planning", "read_entitlements": "planning", "score_constraints": "planning",
	}
	for _, tool := range d.Tools {
		delegate, ok := allowedTools[tool]
		if !ok || !delegates[delegate] {
			return ErrInvalidSkill
		}
	}
	modes := map[entity.LightRAGMode]bool{}
	for _, mode := range d.LightRAGModes {
		if !mode.Valid() || modes[mode] {
			return ErrInvalidSkill
		}
		modes[mode] = true
	}
	if len(d.LightRAGModes) > 0 != contains(d.Tools, "search_lightrag") {
		return ErrInvalidSkill
	}
	if routes[entity.RoutePlanning] && !delegates["planning"] {
		return ErrInvalidSkill
	}
	return nil
}

func uniqueIdentifiers(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !identifier.MatchString(value) || seen[value] {
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
func clone(d Definition) Definition {
	d.Intents = append([]string(nil), d.Intents...)
	d.Routes = append([]entity.Route(nil), d.Routes...)
	d.Delegates = append([]string(nil), d.Delegates...)
	d.Tools = append([]string(nil), d.Tools...)
	d.LightRAGModes = append([]entity.LightRAGMode(nil), d.LightRAGModes...)
	d.EvalCases = append([]string(nil), d.EvalCases...)
	return d
}
