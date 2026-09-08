package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	groupValidated := strings.Index(step, `[[ "$shared_gid" =~ ^[1-9][0-9]{0,9}$ ]]`)
	groupBounded := strings.Index(step, `(( shared_gid <= 2147483647 ))`)
	groupPersisted := strings.Index(step, `echo "XLH_LIGHTRAG_SHARED_GID=$shared_gid"`)
	if validated < 0 || persisted < 0 || writerValidated < 0 || writerPersisted < 0 || groupValidated < 0 || groupBounded < 0 || groupPersisted < 0 {
		t.Fatalf("bootstrap step must validate and persist digests and shared GID: contract=(%d,%d) writer=(%d,%d) group=(%d,%d,%d)", validated, persisted, writerValidated, writerPersisted, groupValidated, groupBounded, groupPersisted)
	}
	if validated >= persisted || writerValidated >= writerPersisted || groupValidated >= groupBounded || groupBounded >= groupPersisted {
		t.Fatalf("bootstrap propagation order is unsafe: contract=(%d,%d) writer=(%d,%d) group=(%d,%d,%d)", validated, persisted, writerValidated, writerPersisted, groupValidated, groupBounded, groupPersisted)
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
            "$XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256" "$XLH_LIGHTRAG_SHARED_GID"`
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

func TestLightRAGLifecycleUsesPersistedSharedGIDForEveryLiveReadiness(t *testing.T) {
	sourceBytes, err := os.ReadFile("check-lightrag-lifecycle.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	const livePrefix = `bash deploy/check-lightrag-live.sh "$base_url" "$XLH_LIGHTRAG_API_KEY" "$fence_dir" "$XLH_LIGHTRAG_DEPLOYMENT_GENERATION" "$contract_sha256" `
	const liveCommand = livePrefix + `"$XLH_LIGHTRAG_SHARED_GID"`
	if got := strings.Count(source, liveCommand); got != 3 {
		t.Fatalf("lifecycle live readiness shared-GID propagation count = %d, want 3", got)
	}
	if strings.Contains(source, livePrefix+`"$shared_gid"`) {
		t.Fatal("lifecycle must not reference an unassigned local shared_gid")
	}
}

func TestLightRAGBootstrapEmitsSafePhaseAnnotations(t *testing.T) {
	sourceBytes, err := os.ReadFile("lightrag-bootstrap-empty.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	if !strings.Contains(source, "trap report_bootstrap_exit EXIT") {
		t.Fatal("LightRAG bootstrap must annotate every nonzero exit")
	}
	if !strings.Contains(source, "::error title=LightRAG bootstrap failed::phase=%s exit_code=%s") {
		t.Fatal("LightRAG bootstrap failure annotation is missing")
	}

	phases := []string{
		"bootstrap_phase=preflight",
		"bootstrap_phase=managed_service_absence",
		"bootstrap_phase=writer_evidence",
		"bootstrap_phase=dependency_start",
		"bootstrap_phase=milvus_rbac",
		"bootstrap_phase=fence_controller",
		"bootstrap_phase=fence_verify",
		"bootstrap_phase=complete",
	}
	previous := -1
	for _, phase := range phases {
		position := strings.Index(source, phase)
		if position < 0 {
			t.Fatalf("LightRAG bootstrap phase is missing: %s", phase)
		}
		if position <= previous {
			t.Fatalf("LightRAG bootstrap phase is out of order: %s", phase)
		}
		previous = position
	}

	if !strings.Contains(source, `for service in milvus-etcd milvus-minio milvus`) ||
		!strings.Contains(source, `service=%s state=%s health=%s exit_code=%s`) {
		t.Fatal("dependency startup failures must expose bounded container state")
	}
	if !strings.Contains(source, `prepare_shared_fence.py "$operator_uid" "$shared_gid"`) ||
		!strings.Contains(source, `--network none --read-only --user 0:0`) ||
		!strings.Contains(source, `--cap-drop ALL --cap-add CHOWN --cap-add FOWNER --cap-add DAC_OVERRIDE --cap-add FSETID`) ||
		!strings.Contains(source, `--security-opt no-new-privileges`) ||
		!strings.Contains(source, `prepare_shared_fence.py verify "$operator_uid" "$shared_gid" "$attempt_id"`) ||
		!strings.Contains(source, `--user "$operator_uid:$shared_gid"`) ||
		!strings.Contains(source, `--group-add 1000`) ||
		!strings.Contains(source, `--volume "$fence_dir:/rebuild-fence:ro"`) ||
		!strings.Contains(source, `--volume "$evidence_dir:/writer-evidence:ro"`) {
		t.Fatal("shared fence ownership must use the pinned, isolated root helper")
	}
}

func TestLightRAGBootstrapAnnotatesExpansionAndExplicitExitFailures(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		wantCode int
	}{
		{name: "missing required variable", env: nil, wantCode: 2},
		{
			name: "invalid generation",
			env: append(validBootstrapEnvironment(t),
				"XLH_LIGHTRAG_DEPLOYMENT_GENERATION=invalid!generation",
			),
			wantCode: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("bash", "deploy/lightrag-bootstrap-empty.sh")
			command.Dir = ".."
			command.Env = append([]string{"GITHUB_ACTIONS=true"}, test.env...)
			output, err := command.CombinedOutput()
			var exitError *exec.ExitError
			if !strings.Contains(string(output), "::error title=LightRAG bootstrap failed::phase=preflight exit_code=") {
				t.Fatalf("missing preflight annotation in %q", output)
			}
			if !isExitCode(err, test.wantCode, &exitError) {
				t.Fatalf("exit error = %v, want code %d; output=%q", err, test.wantCode, output)
			}
			if strings.Count(string(output), "title=LightRAG bootstrap failed") != 1 {
				t.Fatalf("failure annotation must be emitted once: %q", output)
			}
		})
	}
}

func TestLightRAGBootstrapAnnotatesCommandSubstitutionFailureOnce(t *testing.T) {
	fakeBin := t.TempDir()
	fakeDocker := `#!/bin/bash
set -eu
if [[ "${1:-}" == "compose" && "${2:-}" == "version" ]]; then exit 0; fi
exit 2
`
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte(fakeDocker), 0o700); err != nil {
		t.Fatal(err)
	}
	fakePython := "#!/usr/bin/env bash\nexit 7\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "python3"), []byte(fakePython), 0o700); err != nil {
		t.Fatal(err)
	}

	environment := validBootstrapEnvironment(t)
	environment = append(environment, "PATH="+fakeBin+":"+os.Getenv("PATH"))
	command := exec.Command("bash", "deploy/lightrag-bootstrap-empty.sh")
	command.Dir = ".."
	command.Env = environment
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !isExitCode(err, 7, &exitError) {
		t.Fatalf("exit error = %v, want code 7; output=%q", err, output)
	}
	annotation := "::error title=LightRAG bootstrap failed::phase=preflight exit_code=7"
	if strings.Count(string(output), annotation) != 1 {
		t.Fatalf("command substitution failure must annotate exactly once: %q", output)
	}
}

func TestLightRAGBootstrapSuccessDoesNotAnnotate(t *testing.T) {
	fakeBin := t.TempDir()
	fakeDocker := `#!/usr/bin/env bash
set -eu
if [[ "${1:-}" == "run" ]]; then exit 0; fi
if [[ "${1:-}" == "compose" ]]; then
  for argument in "$@"; do
    if [[ "$argument" == "version" || "$argument" == "up" || "$argument" == "run" ]]; then exit 0; fi
    if [[ "$argument" == "ps" ]]; then exit 0; fi
  done
fi
exit 2
`
	fakeBash := `#!/bin/bash
set -eu
case "${1:-}" in
  deploy/lightrag-contract-hash.sh) printf 'sha256:%064d\n' 0 ;;
  deploy/check-lightrag-fence.sh) exit 0 ;;
  *) exec /bin/bash "$@" ;;
esac
`
	for name, content := range map[string]string{"docker": fakeDocker, "bash": fakeBash} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command("/bin/bash", "deploy/lightrag-bootstrap-empty.sh")
	command.Dir = ".."
	command.Env = append(validBootstrapEnvironment(t), "PATH="+fakeBin+":"+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("successful bootstrap failed: %v; output=%q", err, output)
	}
	if strings.Contains(string(output), "::error") {
		t.Fatalf("successful bootstrap emitted an error annotation: %q", output)
	}
	for _, name := range []string{"XLH_LIGHTRAG_DEPLOYMENT_GENERATION", "XLH_LIGHTRAG_ATTEMPT_ID", "XLH_LIGHTRAG_WRITER_EVIDENCE_SHA256", "XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256", "XLH_LIGHTRAG_SHARED_GID"} {
		if !strings.Contains(string(output), name+"=") {
			t.Fatalf("successful bootstrap omitted %s: %q", name, output)
		}
	}
}

func TestLightRAGBootstrapPreservesDependencyFailureAndReportsBoundedState(t *testing.T) {
	fakeBin := t.TempDir()
	fakeDocker := `#!/usr/bin/env bash
set -eu
if [[ "${1:-}" == "run" ]]; then exit 0; fi
if [[ "${1:-}" == "compose" && "${2:-}" == "version" ]]; then
  exit 0
fi
if [[ "${1:-}" == "compose" ]]; then
  for argument in "$@"; do
    if [[ "$argument" == "up" ]]; then
      exit 9
    fi
    if [[ "$argument" == "ps" ]]; then
      service=${!#}
      if [[ "$service" != "lightrag" ]]; then
        printf '%s-id\n' "$service"
      fi
      exit 0
    fi
  done
fi
if [[ "${1:-}" == "inspect" ]]; then
  printf 'running|unhealthy|42\n'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte(fakeDocker), 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("bash", "deploy/lightrag-bootstrap-empty.sh", "deploy/docker-compose.lightrag.yml", "testproject")
	command.Dir = ".."
	command.Env = append(validBootstrapEnvironment(t), "PATH="+fakeBin+":"+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !isExitCode(err, 9, &exitError) {
		t.Fatalf("exit error = %v, want code 9; output=%q", err, output)
	}
	text := string(output)
	if strings.Count(text, "::error title=LightRAG bootstrap failed::phase=dependency_start exit_code=9") != 1 {
		t.Fatalf("dependency failure annotation mismatch: %q", output)
	}
	for _, service := range []string{"milvus-etcd", "milvus-minio", "milvus"} {
		want := "::error title=LightRAG dependency state::service=" + service + " state=running health=unhealthy exit_code=42"
		if !strings.Contains(text, want) {
			t.Fatalf("missing bounded dependency state %q in %q", want, output)
		}
	}
	if strings.Contains(text, "test-secret") {
		t.Fatalf("bootstrap diagnostics leaked a credential: %q", output)
	}
}

func TestLightRAGBootstrapDependencyDiagnosticsDegradeWithoutMaskingFailure(t *testing.T) {
	fakeBin := t.TempDir()
	fakeDocker := `#!/usr/bin/env bash
set -eu
if [[ "${1:-}" == "run" ]]; then exit 0; fi
if [[ "${1:-}" == "compose" && "${2:-}" == "version" ]]; then exit 0; fi
if [[ "${1:-}" == "compose" ]]; then
  for argument in "$@"; do
    if [[ "$argument" == "up" ]]; then exit 9; fi
    if [[ "$argument" == "ps" ]]; then
      service=${!#}
      if [[ "$service" != "lightrag" ]]; then printf 'container-id\n'; fi
      exit 0
    fi
  done
fi
if [[ "${1:-}" == "inspect" ]]; then exit 17; fi
exit 2
`
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte(fakeDocker), 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("bash", "deploy/lightrag-bootstrap-empty.sh", "deploy/docker-compose.lightrag.yml", "testproject")
	command.Dir = ".."
	command.Env = append(validBootstrapEnvironment(t), "PATH="+fakeBin+":"+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !isExitCode(err, 9, &exitError) {
		t.Fatalf("exit error = %v, want code 9; output=%q", err, output)
	}
	text := string(output)
	for _, service := range []string{"milvus-etcd", "milvus-minio", "milvus"} {
		want := "service=" + service + " state=unknown health=unknown exit_code=unknown"
		if !strings.Contains(text, want) {
			t.Fatalf("missing degraded dependency state %q in %q", want, output)
		}
	}
}

func validBootstrapEnvironment(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonicalRoot
	groupOutput, err := exec.Command("id", "-g").Output()
	if err != nil {
		t.Fatal(err)
	}
	sharedGroup := strings.TrimSpace(string(groupOutput))
	if sharedGroup == "0" {
		// Root operators must choose a dedicated non-root group. The fake Docker
		// tests do not touch the host filesystem, so a stable numeric value is enough.
		sharedGroup = "1"
	}
	return []string{
		"GITHUB_ACTIONS=true",
		"XLH_LIGHTRAG_DEPLOYMENT_GENERATION=test-generation",
		"XLH_LIGHTRAG_ATTEMPT_ID=test-attempt",
		"XLH_LIGHTRAG_UNCONTROLLED_WRITERS_ATTESTATION=no_uncontrolled_writers",
		"XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR=" + filepath.Join(root, "writer"),
		"XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR=" + filepath.Join(root, "fence"),
		"XLH_LIGHTRAG_SHARED_GID=" + sharedGroup,
		"XLH_LIGHTRAG_API_KEY=test-secret-api-key",
		"XLH_LIGHTRAG_LLM_API_KEY=test-secret-llm-key",
		"XLH_LIGHTRAG_EMBEDDING_API_KEY=test-secret-embedding-key",
		"XLH_MILVUS_MINIO_USER=test-user",
		"XLH_MILVUS_MINIO_PASSWORD=test-secret-minio-password",
		"XLH_MILVUS_ROOT_PASSWORD=test-secret-root-password",
		"XLH_MILVUS_TOKEN=test-user:test-secret-runtime-password",
		"PATH=" + os.Getenv("PATH"),
	}
}

func isExitCode(err error, want int, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	exitError, ok := err.(*exec.ExitError)
	if ok {
		*target = exitError
	}
	return ok && exitError.ExitCode() == want
}
