package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("direct prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XLH_DIRECT_PROMPT_FILE", promptPath)
	t.Setenv("XLH_PLANNING_PROMPT_FILE", promptPath)
	t.Setenv("XLH_RESEARCH_PROMPT_FILE", promptPath)
	t.Setenv("XLH_SYNTHESIS_PROMPT_FILE", promptPath)
	t.Setenv("XLH_DATABASE_URL", "postgres://database")
	t.Setenv("XLH_AI_API_KEY", "key")
	t.Setenv("DASHSCOPE_API_KEY", "")
	t.Setenv("XLH_AI_CHAT_MODEL", "model")
	t.Setenv("XLH_AI_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://database" || cfg.AIAPIKey != "key" || cfg.AIModel != "model" || cfg.AITimeout != 60*time.Second || cfg.DirectPrompt != "direct prompt" || cfg.AIBaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("config = %#v", cfg)
	}

	t.Run("requires database URL", func(t *testing.T) {
		t.Setenv("XLH_DATABASE_URL", "")
		if _, err := Load(); err == nil || err.Error() != "XLH_DATABASE_URL is required" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("accepts DashScope key fallback", func(t *testing.T) {
		t.Setenv("XLH_DATABASE_URL", "postgres://database")
		t.Setenv("XLH_AI_API_KEY", "")
		t.Setenv("DASHSCOPE_API_KEY", "fallback")
		cfg, err := Load()
		if err != nil || cfg.AIAPIKey != "fallback" {
			t.Fatalf("config=%#v err=%v", cfg, err)
		}
	})

	t.Run("rejects invalid model timeout", func(t *testing.T) {
		t.Setenv("XLH_AI_TIMEOUT", "later")
		if _, err := Load(); err == nil {
			t.Fatal("expected timeout parse error")
		}
	})

	t.Run("rejects disabled model timeout", func(t *testing.T) {
		t.Setenv("XLH_AI_TIMEOUT", "0s")
		if _, err := Load(); err == nil || err.Error() != "XLH_AI_TIMEOUT must be positive" {
			t.Fatalf("err = %v", err)
		}
	})
}
