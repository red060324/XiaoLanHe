package main

import (
	"strings"
	"testing"

	knowledgelightrag "github.com/red060324/XiaoLanHe/internal/knowledge/repository/lightrag"
)

func TestParseFlagsRejectsUnsafeModesAndLegacyAfterID(t *testing.T) {
	for _, arguments := range [][]string{
		{"--execute", "--reconcile"},
		{"--after-id", "10"},
		{"--limit", "0"},
		{"unexpected"},
	} {
		if _, err := parseFlags(arguments); err == nil {
			t.Fatalf("arguments %v unexpectedly accepted", arguments)
		}
	}
}

func TestLoadLightRAGConfigIncludesFenceWithoutGlobalConfig(t *testing.T) {
	t.Setenv("XLH_LIGHTRAG_BASE_URL", "https://lightrag.example")
	t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "false")
	t.Setenv("XLH_LIGHTRAG_API_KEY", strings.Repeat("k", 32))
	t.Setenv("XLH_LIGHTRAG_REBUILD_FENCE_DIR", "/var/lib/xlh/fence")
	t.Setenv("XLH_LIGHTRAG_DEPLOYMENT_GENERATION", "generation-9")
	t.Setenv("XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256", "sha256:"+strings.Repeat("a", 64))
	config, err := loadLightRAGConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.BaseURL != "https://lightrag.example" || config.AllowInsecure || config.FencePath != "/var/lib/xlh/fence" || config.FenceGeneration != "generation-9" || config.FenceContractSHA256 != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("config=%+v", config)
	}
}

func TestLoadLightRAGConfigTransportSecurity(t *testing.T) {
	t.Setenv("XLH_LIGHTRAG_API_KEY", strings.Repeat("k", 32))
	t.Setenv("XLH_LIGHTRAG_REBUILD_FENCE_DIR", "/var/lib/xlh/fence")
	t.Setenv("XLH_LIGHTRAG_DEPLOYMENT_GENERATION", "generation-9")
	t.Setenv("XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256", "sha256:"+strings.Repeat("a", 64))

	t.Run("defaults to HTTPS", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "")
		config, err := loadLightRAGConfig()
		if err != nil || config.BaseURL != "https://127.0.0.1:9621" || config.AllowInsecure {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	})
	t.Run("rejects HTTP without opt-in", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "http://lightrag:9621")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "false")
		if _, err := loadLightRAGConfig(); err == nil || err.Error() != "XLH_LIGHTRAG_BASE_URL must use https unless XLH_LIGHTRAG_ALLOW_INSECURE=true" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("accepts HTTP with explicit opt-in", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "http://lightrag:9621/")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "true")
		config, err := loadLightRAGConfig()
		if err != nil || config.BaseURL != "http://lightrag:9621" || !config.AllowInsecure {
			t.Fatalf("config=%+v err=%v", config, err)
		}
		if _, err := knowledgelightrag.NewClient(config); err != nil {
			t.Fatalf("shared client rejected validated config: %v", err)
		}
	})
	t.Run("rejects invalid opt-in", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "sometimes")
		if _, err := loadLightRAGConfig(); err == nil || err.Error() != "XLH_LIGHTRAG_ALLOW_INSECURE must be true or false" {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestLoadLightRAGConfigRequiresFenceContract(t *testing.T) {
	t.Setenv("XLH_LIGHTRAG_BASE_URL", "https://lightrag.example")
	t.Setenv("XLH_LIGHTRAG_API_KEY", strings.Repeat("k", 32))
	t.Setenv("XLH_LIGHTRAG_REBUILD_FENCE_DIR", "")
	t.Setenv("XLH_LIGHTRAG_DEPLOYMENT_GENERATION", "")
	t.Setenv("XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256", "")
	if _, err := loadLightRAGConfig(); err == nil {
		t.Fatal("missing fence contract unexpectedly accepted")
	}
}
