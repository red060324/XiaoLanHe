package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func checkedInCompose(t *testing.T) composeFile {
	t.Helper()
	document, err := loadCompose("docker-compose.lightrag.yml")
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func cloneCompose(t *testing.T, source composeFile) composeFile {
	t.Helper()
	data, err := yaml.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result composeFile
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCheckedInComposePasses(t *testing.T) {
	if err := checkCompose(checkedInCompose(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeIdentityMustBeOnLightRAGService(t *testing.T) {
	document := cloneCompose(t, checkedInCompose(t))
	service := document.Services["lightrag"]
	service.Environment["MILVUS_TOKEN"] = ""
	document.Services["lightrag"] = service
	if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "lightrag must require") {
		t.Fatalf("got %v", err)
	}
}

func TestBootstrapMustReceiveRequiredWriterEvidenceHostDir(t *testing.T) {
	tests := []struct {
		name  string
		value *string
	}{
		{name: "missing"},
		{name: "fallback", value: stringPointer("${XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR:-/tmp/writer-evidence}")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := cloneCompose(t, checkedInCompose(t))
			service := document.Services["lightrag-bootstrap"]
			if test.value == nil {
				delete(service.Environment, "XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR")
			} else {
				service.Environment["XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR"] = *test.value
			}
			document.Services["lightrag-bootstrap"] = service
			if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "must require the writer-evidence host directory in its environment") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestLightRAGServicesMustPreserveOfficialPrivilegeDrop(t *testing.T) {
	for _, name := range []string{"lightrag", "lightrag-bootstrap"} {
		for _, mutation := range []struct {
			name string
			edit func(*composeService)
		}{
			{name: "user", edit: func(service *composeService) { service.User = "1000:1000" }},
			{name: "entrypoint", edit: func(service *composeService) { service.Entrypoint = []string{"python"} }},
		} {
			t.Run(name+"/"+mutation.name, func(t *testing.T) {
				document := cloneCompose(t, checkedInCompose(t))
				service := document.Services[name]
				mutation.edit(&service)
				document.Services[name] = service
				if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "must preserve the official root entry point") {
					t.Fatalf("got %v", err)
				}
			})
		}
	}
}

func TestEmbeddingPrefixesMustRemainUnset(t *testing.T) {
	for _, serviceName := range []string{"lightrag", "lightrag-bootstrap"} {
		for _, key := range []string{"EMBEDDING_DOCUMENT_PREFIX", "EMBEDDING_QUERY_PREFIX"} {
			for _, value := range []string{"", "NO_PREFIX"} {
				t.Run(serviceName+"/"+key+"/"+value, func(t *testing.T) {
					document := cloneCompose(t, checkedInCompose(t))
					service := document.Services[serviceName]
					service.Environment[key] = value
					document.Services[serviceName] = service
					if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), key+" must remain unset") {
						t.Fatalf("got %v", err)
					}
				})
			}
		}
	}
}

func TestContractHashClearsInheritedEmbeddingPrefixes(t *testing.T) {
	const expectedDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000\n"
	shimDirectory := t.TempDir()
	shimPath := filepath.Join(shimDirectory, "python3")
	shim := `#!/bin/sh
set -eu
if [ "${EMBEDDING_DOCUMENT_PREFIX+x}" = x ] || [ "${EMBEDDING_QUERY_PREFIX+x}" = x ]; then
  echo "embedding prefix leaked into contract builder" >&2
  exit 41
fi
printf '%s\n' 'sha256:0000000000000000000000000000000000000000000000000000000000000000'
`
	if err := os.WriteFile(shimPath, []byte(shim), 0o700); err != nil {
		t.Fatalf("write python3 shim: %v", err)
	}

	command := exec.Command("bash", "deploy/lightrag-contract-hash.sh")
	command.Dir = ".."
	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") ||
			strings.HasPrefix(entry, "EMBEDDING_DOCUMENT_PREFIX=") ||
			strings.HasPrefix(entry, "EMBEDDING_QUERY_PREFIX=") {
			continue
		}
		environment = append(environment, entry)
	}
	command.Env = append(
		environment,
		"PATH="+shimDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EMBEDDING_DOCUMENT_PREFIX=inherited-document-prefix",
		"EMBEDDING_QUERY_PREFIX=inherited-query-prefix",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("contract hash rejected inherited environment: %v\n%s", err, output)
	}
	if string(output) != expectedDigest {
		t.Fatalf("contract helper did not execute the environment-checking shim: %q", output)
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestSteadyStateCannotDependOnBootstrap(t *testing.T) {
	document := cloneCompose(t, checkedInCompose(t))
	service := document.Services["lightrag"]
	service.DependsOn = map[string]composeDependency{"lightrag-bootstrap": {Condition: "service_completed_successfully"}}
	document.Services["lightrag"] = service
	if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "must not depend") {
		t.Fatalf("got %v", err)
	}
}

func TestLightRAGCommandsMustRemainFenceGuarded(t *testing.T) {
	tests := []struct {
		service string
		want    string
	}{
		{service: "lightrag", want: "steady-state LightRAG"},
		{service: "lightrag-bootstrap", want: "bootstrap LightRAG"},
	}
	for _, test := range tests {
		t.Run(test.service, func(t *testing.T) {
			document := cloneCompose(t, checkedInCompose(t))
			service := document.Services[test.service]
			service.Command = []string{"python", "-m", "lightrag.api.lightrag_server"}
			document.Services[test.service] = service
			if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), test.want+" must use the fence-guarded entry point") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestFenceMountMustUseRequiredHostVariable(t *testing.T) {
	document := cloneCompose(t, checkedInCompose(t))
	service := document.Services["lightrag"]
	for index, mount := range service.Volumes {
		if strings.HasSuffix(mount, ":/rebuild-fence:ro") {
			service.Volumes[index] = "./relative-fence:/rebuild-fence:ro"
		}
	}
	document.Services["lightrag"] = service
	if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "absolute fence host path") {
		t.Fatalf("got %v", err)
	}
}

func TestSteadyFenceMountMustBeReadOnly(t *testing.T) {
	document := cloneCompose(t, checkedInCompose(t))
	service := document.Services["lightrag"]
	for index, mount := range service.Volumes {
		if strings.HasSuffix(mount, ":/rebuild-fence:ro") {
			service.Volumes[index] = strings.TrimSuffix(mount, ":ro")
		}
	}
	document.Services["lightrag"] = service
	if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "mounted read-only") {
		t.Fatalf("got %v", err)
	}
}

func TestBootstrapFenceMountMustBeReadWrite(t *testing.T) {
	document := cloneCompose(t, checkedInCompose(t))
	service := document.Services["lightrag-bootstrap"]
	for index, mount := range service.Volumes {
		if strings.HasSuffix(mount, ":/rebuild-fence") {
			service.Volumes[index] = mount + ":ro"
		}
	}
	document.Services["lightrag-bootstrap"] = service
	if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "mounted read-write") {
		t.Fatalf("got %v", err)
	}
}
