package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	lightRAGImage                 = "ghcr.io/hkuds/lightrag:v1.5.7@sha256:5bdbd524931b011df246fe20888d110cef691e6804c12cde636a2b746d7de27e"
	writerEvidenceHostDirRequired = "${XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR:?XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR must be an absolute path}"
	sharedGIDRequired             = "${XLH_LIGHTRAG_SHARED_GID:?XLH_LIGHTRAG_SHARED_GID must be a nonzero numeric group ID}"
)

var (
	requiredServices = []string{"lightrag", "lightrag-bootstrap", "milvus", "milvus-etcd", "milvus-init", "milvus-minio"}
	requiredEnv      = map[string]string{
		"WORKSPACE":                   "xiaolanhe_v1",
		"WORKING_DIR":                 "/app/data/rag_storage",
		"WORKERS":                     "2",
		"LIGHTRAG_KV_STORAGE":         "JsonKVStorage",
		"LIGHTRAG_VECTOR_STORAGE":     "MilvusVectorDBStorage",
		"LIGHTRAG_GRAPH_STORAGE":      "NetworkXStorage",
		"LIGHTRAG_DOC_STATUS_STORAGE": "JsonDocStatusStorage",
		"MILVUS_URI":                  "http://milvus:19530",
		"MILVUS_DB_NAME":              "lightrag",
		"MILVUS_INDEX_TYPE":           "AUTOINDEX",
		"MILVUS_METRIC_TYPE":          "COSINE",
		"XLH_LIGHTRAG_SHARED_GID":     sharedGIDRequired,
	}
	fenceMountRW = regexp.MustCompile(`^\$\{XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR:?[^}]+\}:/rebuild-fence$`)
	fenceMountRO = regexp.MustCompile(`^\$\{XLH_LIGHTRAG_REBUILD_FENCE_HOST_DIR:?[^}]+\}:/rebuild-fence:ro$`)
	writerMount  = regexp.MustCompile(`^\$\{XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR:?[^}]+\}:/writer-evidence:ro$`)
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image       string                       `yaml:"image"`
	User        string                       `yaml:"user"`
	Entrypoint  any                          `yaml:"entrypoint"`
	Command     []string                     `yaml:"command"`
	Profiles    []string                     `yaml:"profiles"`
	Environment map[string]any               `yaml:"environment"`
	DependsOn   map[string]composeDependency `yaml:"depends_on"`
	Volumes     []string                     `yaml:"volumes"`
}

type composeDependency struct {
	Condition string `yaml:"condition"`
}

func env(service composeService, key string) string {
	return fmt.Sprint(service.Environment[key])
}

func checkCompose(document composeFile) error {
	actualServices := make([]string, 0, len(document.Services))
	for name := range document.Services {
		actualServices = append(actualServices, name)
	}
	sort.Strings(actualServices)
	if fmt.Sprint(actualServices) != fmt.Sprint(requiredServices) {
		return fmt.Errorf("unexpected service set: %v", actualServices)
	}

	runtime := document.Services["lightrag"]
	bootstrap := document.Services["lightrag-bootstrap"]
	init := document.Services["milvus-init"]
	milvus := document.Services["milvus"]
	for _, name := range []string{"lightrag", "lightrag-bootstrap", "milvus-init"} {
		if document.Services[name].Image != lightRAGImage {
			return fmt.Errorf("%s must use the pinned LightRAG image", name)
		}
	}
	images := map[string]string{
		"milvus":       "milvusdb/milvus:v2.6.11",
		"milvus-etcd":  "quay.io/coreos/etcd:v3.5.25",
		"milvus-minio": "minio/minio:RELEASE.2025-09-07T16-13-09Z",
	}
	for name, expected := range images {
		if document.Services[name].Image != expected {
			return fmt.Errorf("%s image must equal %q", name, expected)
		}
	}
	for _, name := range []string{"lightrag", "lightrag-bootstrap"} {
		service := document.Services[name]
		if service.User != "" {
			return fmt.Errorf("%s must preserve the official root entry point and privilege drop", name)
		}
		if service.Entrypoint != nil {
			return fmt.Errorf("%s must preserve the official root entry point and privilege drop", name)
		}
		for key, expected := range requiredEnv {
			if env(service, key) != expected {
				return fmt.Errorf("%s.%s must equal %q", name, key, expected)
			}
		}
		for _, key := range []string{"EMBEDDING_DOCUMENT_PREFIX", "EMBEDDING_QUERY_PREFIX"} {
			if _, exists := service.Environment[key]; exists {
				return fmt.Errorf("%s.%s must remain unset", name, key)
			}
		}
		if env(service, "MILVUS_TOKEN") != "${XLH_MILVUS_TOKEN:?XLH_MILVUS_TOKEN is required}" {
			return fmt.Errorf("%s must require the steady-state Milvus token", name)
		}
	}
	if env(milvus, "COMMON_SECURITY_AUTHORIZATIONENABLED") != "true" {
		return fmt.Errorf("Milvus authentication must be enabled")
	}
	if env(milvus, "COMMON_SECURITY_ENABLEPUBLICPRIVILEGE") != "false" {
		return fmt.Errorf("Milvus public privileges must be disabled")
	}
	if env(milvus, "COMMON_SECURITY_DEFAULTROOTPASSWORD") != "${XLH_MILVUS_ROOT_PASSWORD:?XLH_MILVUS_ROOT_PASSWORD is required}" {
		return fmt.Errorf("Milvus root password must be required")
	}
	if env(init, "MILVUS_BOOTSTRAP_TOKEN") != "root:${XLH_MILVUS_ROOT_PASSWORD:?XLH_MILVUS_ROOT_PASSWORD is required}" {
		return fmt.Errorf("milvus-init must use only the bootstrap identity")
	}
	if env(init, "MILVUS_RUNTIME_TOKEN") != "${XLH_MILVUS_TOKEN:?XLH_MILVUS_TOKEN is required}" {
		return fmt.Errorf("milvus-init must provision a required runtime identity")
	}
	if _, exists := runtime.Environment["MILVUS_BOOTSTRAP_TOKEN"]; exists {
		return fmt.Errorf("steady-state LightRAG must not receive the bootstrap identity")
	}
	if env(bootstrap, "XLH_LIGHTRAG_WRITER_EVIDENCE_HOST_DIR") != writerEvidenceHostDirRequired {
		return fmt.Errorf("lightrag-bootstrap must require the writer-evidence host directory in its environment")
	}
	if fmt.Sprint(init.Profiles) != "[bootstrap]" || fmt.Sprint(bootstrap.Profiles) != "[bootstrap]" {
		return fmt.Errorf("one-shot init services must use only the bootstrap profile")
	}
	if _, exists := runtime.DependsOn["lightrag-bootstrap"]; exists {
		return fmt.Errorf("steady-state LightRAG must not depend on or rerun bootstrap")
	}
	if runtime.DependsOn["milvus"].Condition != "service_healthy" {
		return fmt.Errorf("steady-state LightRAG must depend on healthy Milvus")
	}
	if fmt.Sprint(runtime.Command) != "[python /opt/xlh/guarded_start.py steady]" {
		return fmt.Errorf("steady-state LightRAG must use the fence-guarded entry point")
	}
	if fmt.Sprint(bootstrap.Command) != "[python /opt/xlh/guarded_start.py bootstrap]" {
		return fmt.Errorf("bootstrap LightRAG must use the fence-guarded entry point")
	}
	for name, service := range map[string]composeService{"lightrag": runtime, "lightrag-bootstrap": bootstrap} {
		found := []string{}
		for _, mount := range service.Volumes {
			if strings.HasSuffix(mount, ":/rebuild-fence") || strings.HasSuffix(mount, ":/rebuild-fence:ro") {
				found = append(found, mount)
			}
		}
		if len(found) != 1 {
			return fmt.Errorf("%s must require exactly one absolute fence host path", name)
		}
		expectedMount := fenceMountRW
		expectedMode := "read-write"
		if name == "lightrag" {
			expectedMount = fenceMountRO
			expectedMode = "read-only"
		}
		if !expectedMount.MatchString(found[0]) {
			return fmt.Errorf("%s must require exactly one absolute fence host path mounted %s", name, expectedMode)
		}
	}
	foundWriter := []string{}
	for _, mount := range bootstrap.Volumes {
		if strings.HasSuffix(mount, ":/writer-evidence:ro") {
			foundWriter = append(foundWriter, mount)
		}
	}
	if len(foundWriter) != 1 || !writerMount.MatchString(foundWriter[0]) {
		return fmt.Errorf("bootstrap must require exactly one absolute writer-evidence host path")
	}
	return nil
}

func loadCompose(path string) (composeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return composeFile{}, err
	}
	var document composeFile
	if err := yaml.Unmarshal(data, &document); err != nil {
		return composeFile{}, err
	}
	return document, nil
}

func main() {
	path := "deploy/docker-compose.lightrag.yml"
	if len(os.Args) == 2 {
		path = os.Args[1]
	} else if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: check_lightrag_compose [compose-file]")
		os.Exit(2)
	}
	document, err := loadCompose(path)
	if err == nil {
		err = checkCompose(document)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "LightRAG compose contract failed: %v\n", err)
		os.Exit(1)
	}
}
