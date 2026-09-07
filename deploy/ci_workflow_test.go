package main

import (
	"os"
	"strings"
	"testing"
)

func TestLightRAGWorkflowPersistsVerifiedContractBeforeLaterStages(t *testing.T) {
	sourceBytes, err := os.ReadFile("../.github/workflows/go.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	stepStart := strings.Index(source, "- name: Bootstrap empty LightRAG knowledge store")
	if stepStart < 0 {
		t.Fatal("LightRAG bootstrap step is missing")
	}
	stepEnd := strings.Index(source[stepStart:], "\n      - name: Validate bootstrapped Milvus runtime contract")
	if stepEnd < 0 {
		t.Fatal("LightRAG bootstrap step boundary is missing")
	}
	step := source[stepStart : stepStart+stepEnd]
	if !strings.Contains(step, "run: |\n          set -o pipefail") {
		t.Fatal("bootstrap logging pipeline must preserve the controller exit status")
	}
	if !strings.Contains(step, `deploy/docker-compose.lightrag.yml "$XLH_CI_COMPOSE_PROJECT" | tee /dev/stderr`) {
		t.Fatal("bootstrap output must remain visible when the command fails")
	}

	validated := strings.Index(step, `[[ "$reported_contract" == "$contract_sha256" ]]`)
	persisted := strings.Index(step, `echo "XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256=$reported_contract"`)
	writerValidated := strings.Index(step, `[[ "$writer_sha256" =~ ^sha256:[0-9a-f]{64}$ ]]`)
	writerPersisted := strings.Index(step, `echo "XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256=$writer_sha256"`)
	if validated < 0 || persisted < 0 || writerValidated < 0 || writerPersisted < 0 {
		t.Fatalf("bootstrap step must validate and persist both digests: contract=(%d,%d) writer=(%d,%d)", validated, persisted, writerValidated, writerPersisted)
	}
	if validated >= persisted || writerValidated >= writerPersisted {
		t.Fatalf("bootstrap digest propagation order is unsafe: contract=(%d,%d) writer=(%d,%d)", validated, persisted, writerValidated, writerPersisted)
	}

	stageNames := []string{
		"- name: Bootstrap empty LightRAG knowledge store",
		"- name: Validate bootstrapped Milvus runtime contract",
		"- name: Start guarded LightRAG steady service",
		"- name: Validate live LightRAG API contract",
		"- name: Repository gates",
	}
	previous := -1
	for _, name := range stageNames {
		position := strings.Index(source, name)
		if position < 0 {
			t.Fatalf("workflow stage is missing: %s", name)
		}
		if position <= previous {
			t.Fatalf("workflow stage is out of order: %s", name)
		}
		previous = position
	}

	var liveStage string
	for index, name := range stageNames[1:4] {
		stageStart := strings.Index(source, name)
		nextStep := strings.Index(source[stageStart+len(name):], "\n      - name:")
		if nextStep < 0 {
			t.Fatalf("workflow stage boundary is missing: %s", name)
		}
		stage := source[stageStart : stageStart+len(name)+nextStep]
		if strings.Contains(stage, "continue-on-error:") || strings.Contains(stage, "\n        if:") {
			t.Fatalf("LightRAG runtime stage must remain success-gated: %s", name)
		}
		if index == 2 {
			liveStage = stage
		}
	}
	liveCommand := `bash deploy/check-lightrag-live.sh http://127.0.0.1:9621 "$XLH_LIGHTRAG_API_KEY" \
            "$XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" \
            "$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256"`
	if !strings.Contains(liveStage, liveCommand) {
		t.Fatal("live LightRAG stage must consume the propagated runtime contract")
	}
}

func TestLightRAGFailureLogsIncludeOneShotServices(t *testing.T) {
	sourceBytes, err := os.ReadFile("../.github/workflows/go.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	stepStart := strings.Index(source, "- name: LightRAG and Milvus logs")
	if stepStart < 0 {
		t.Fatal("LightRAG diagnostic step is missing")
	}
	stepEnd := strings.Index(source[stepStart:], "\n      - name: Middleware logs")
	if stepEnd < 0 {
		t.Fatal("LightRAG diagnostic step boundary is missing")
	}
	step := source[stepStart : stepStart+stepEnd]
	logsStart := strings.Index(step, "logs --no-color")
	if logsStart < 0 {
		t.Fatal("LightRAG diagnostic logs command is missing")
	}
	logsLine := step[logsStart:]
	if lineEnd := strings.IndexByte(logsLine, '\n'); lineEnd >= 0 {
		logsLine = logsLine[:lineEnd]
	}
	got := strings.Fields(strings.TrimPrefix(logsLine, "logs --no-color"))
	want := []string{"lightrag", "lightrag-bootstrap", "milvus-init", "milvus", "milvus-etcd", "milvus-minio"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("LightRAG diagnostic services mismatch: got %q, want %q", got, want)
	}
}
