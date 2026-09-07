package main

import (
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

func TestSteadyStateCannotDependOnBootstrap(t *testing.T) {
	document := cloneCompose(t, checkedInCompose(t))
	service := document.Services["lightrag"]
	service.DependsOn = map[string]composeDependency{"lightrag-bootstrap": {Condition: "service_completed_successfully"}}
	document.Services["lightrag"] = service
	if err := checkCompose(document); err == nil || !strings.Contains(err.Error(), "must not depend") {
		t.Fatalf("got %v", err)
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
