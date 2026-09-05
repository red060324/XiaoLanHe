package skill

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

var testMaximum = entity.BudgetLimit{ModelCalls: 12, ToolCalls: 12, Delegations: 3, TimeoutMilliseconds: 45_000}

func TestLoadEmbeddedRegistry(t *testing.T) {
	registry, err := Load(testMaximum)
	if err != nil {
		t.Fatal(err)
	}
	all := registry.All()
	if len(all) != 4 || all[0].ID != "build_team" || all[3].ID != "research_guide" {
		t.Fatalf("skills=%+v", all)
	}
	recommend, err := registry.Resolve("recommend_games", "1.0.0")
	decision := entity.RouterDecision{Route: entity.RoutePlanning, Intent: "game_recommendation", SkillID: recommend.ID, SkillVersion: recommend.Version}
	if err != nil || !recommend.AllowsDelegate("research") || !recommend.AllowsDelegate("planning") || recommend.AllowsTool("create_order") || !recommend.Supports(decision) {
		t.Fatalf("recommend=%+v err=%v", recommend, err)
	}
	recommend.Tools[0] = "mutated"
	again, _ := registry.Resolve("recommend_games", "1.0.0")
	if again.Tools[0] == "mutated" {
		t.Fatal("registry leaked mutable definition")
	}
	if _, err := registry.Resolve("unknown", "1.0.0"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestRegistryRejectsUnsafeDefinitions(t *testing.T) {
	valid := `{"id":"generic_qa","version":"1.0.0","promptVersion":"generic_qa_v1","intents":["generic_qa"],"routes":["RESEARCH"],"delegates":["research"],"tools":["search_lightrag"],"lightragModes":["mix"],"budget":{"modelCalls":2,"toolCalls":2,"delegations":1,"timeoutMilliseconds":1000},"outputContract":"evidence_answer_v1","citations":true,"evalCases":["case_one"]}`
	cases := map[string]string{
		"unknown field":  valid[:len(valid)-1] + `,"executable":"rm"}`,
		"write tool":     strings.Replace(valid, `"search_lightrag"`, `"create_order"`, 1),
		"budget escape":  strings.Replace(valid, `"modelCalls":2`, `"modelCalls":13`, 1),
		"duplicate tool": strings.Replace(valid, `"search_lightrag"`, `"search_lightrag","search_lightrag"`, 1),
		"unsafe mode":    strings.Replace(valid, `"mix"`, `"bypass"`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			files := fstest.MapFS{}
			for _, file := range []string{"a", "b", "c", "d"} {
				files["definitions/"+file+".json"] = &fstest.MapFile{Data: []byte(body)}
			}
			if _, err := loadFS(files, testMaximum); !errors.Is(err, ErrInvalidSkill) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
