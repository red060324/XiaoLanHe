package main

import (
	"os"
	"strings"
	"testing"
)

func TestLightRAGWorkflowExportsVerifiedContractBeforeSteadyStart(t *testing.T) {
	sourceBytes, err := os.ReadFile("../.github/workflows/go.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	stepStart := strings.Index(source, "- name: Official LightRAG image and runtime contract")
	if stepStart < 0 {
		t.Fatal("official LightRAG runtime step is missing")
	}
	stepEnd := strings.Index(source[stepStart:], "\n      - name: Repository gates")
	if stepEnd < 0 {
		t.Fatal("official LightRAG runtime step boundary is missing")
	}
	step := source[stepStart : stepStart+stepEnd]
	if !strings.Contains(step, "run: |\n          set -o pipefail") {
		t.Fatal("bootstrap logging pipeline must preserve the controller exit status")
	}
	if !strings.Contains(step, `deploy/docker-compose.lightrag.yml "$XLH_CI_COMPOSE_PROJECT" | tee /dev/stderr`) {
		t.Fatal("bootstrap output must remain visible when the command fails")
	}

	validated := strings.Index(step, `[[ "$reported_contract" == "$contract_sha256" ]]`)
	exported := strings.Index(step, `export XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256="$reported_contract"`)
	started := strings.Index(step, `up --detach --wait --no-deps lightrag`)
	if validated < 0 || exported < 0 || started < 0 {
		t.Fatalf("runtime step must validate and export the rebuild contract before steady startup: validated=%d exported=%d started=%d", validated, exported, started)
	}
	if !(validated < exported && exported < started) {
		t.Fatalf("rebuild contract propagation order is unsafe: validated=%d exported=%d started=%d", validated, exported, started)
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
