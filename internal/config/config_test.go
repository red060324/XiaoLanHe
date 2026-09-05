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
	t.Setenv("XLH_ADDRESS", "")
	t.Setenv("PORT", "10000")
	t.Setenv("XLH_FLASH_SALE_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != ":10000" || cfg.DatabaseURL != "postgres://database" || cfg.AIAPIKey != "key" || cfg.AIModel != "model" || cfg.AITimeout != 60*time.Second || cfg.DirectPrompt != "direct prompt" || cfg.AIBaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" || cfg.ResearchTimeout != 25*time.Second || cfg.ResearchToolTimeout != 10*time.Second || cfg.ResearchMaxIterations != 6 || cfg.ResearchMaxToolCalls != 8 {
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

	t.Run("rejects invalid research budgets", func(t *testing.T) {
		for key, value := range map[string]string{
			"XLH_RESEARCH_TIMEOUT":        "0s",
			"XLH_RESEARCH_TOOL_TIMEOUT":   "later",
			"XLH_RESEARCH_MAX_ITERATIONS": "0",
			"XLH_RESEARCH_MAX_TOOL_CALLS": "many",
		} {
			t.Run(key, func(t *testing.T) {
				t.Setenv(key, value)
				if _, err := Load(); err == nil {
					t.Fatal("expected budget validation error")
				}
			})
		}
	})
}

func TestLoadAdvancedAI(t *testing.T) {
	t.Setenv("XLH_ADVANCED_AI_ENABLED", "true")
	t.Setenv("XLH_LIGHTRAG_BASE_URL", "http://lightrag:9621/")
	t.Setenv("XLH_LIGHTRAG_API_KEY", "test-lightrag-key-at-least-32-chars")
	cfg, err := loadAdvancedAI()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.LightRAG.BaseURL != "http://lightrag:9621" || cfg.LightRAG.Workspace != "xiaolanhe_v1" || cfg.LightRAG.CoreVersion != "1.5.7" || cfg.LightRAG.APIVersion != "0344" || cfg.OverallTimeout != 45*time.Second || cfg.MaxModelCalls != 12 || cfg.MaxToolCalls != 12 || cfg.MaxDelegations != 3 || cfg.CopilotMaxIterations != 4 || cfg.PlanningTimeout != 15*time.Second || cfg.PlanningMaxIterations != 4 || cfg.PlanningMaxToolCalls != 4 || cfg.SummaryTimeout != 10*time.Second || cfg.SummaryThreshold != 12_000 || cfg.SummaryCap != 2_000 || cfg.RecentWindow != 8 || cfg.SummaryPromptVersion != "summary-v1" {
		t.Fatalf("config=%+v", cfg)
	}
	t.Run("disabled requires no LightRAG", func(t *testing.T) {
		t.Setenv("XLH_ADVANCED_AI_ENABLED", "false")
		t.Setenv("XLH_LIGHTRAG_API_KEY", "")
		if value, err := loadAdvancedAI(); err != nil || value.Enabled {
			t.Fatalf("value=%+v err=%v", value, err)
		}
	})
	for name, values := range map[string]map[string]string{
		"missing key":     {"XLH_LIGHTRAG_API_KEY": ""},
		"short key":       {"XLH_LIGHTRAG_API_KEY": "secret"},
		"unsafe URL":      {"XLH_LIGHTRAG_API_KEY": "test-lightrag-key-at-least-32-chars", "XLH_LIGHTRAG_BASE_URL": "http://user:pass@lightrag:9621"},
		"bad workspace":   {"XLH_LIGHTRAG_API_KEY": "test-lightrag-key-at-least-32-chars", "XLH_LIGHTRAG_BASE_URL": "http://lightrag:9621", "XLH_LIGHTRAG_WORKSPACE": "bad-workspace"},
		"excessive calls": {"XLH_LIGHTRAG_API_KEY": "test-lightrag-key-at-least-32-chars", "XLH_LIGHTRAG_BASE_URL": "http://lightrag:9621", "XLH_LIGHTRAG_WORKSPACE": "xiaolanhe_v1", "XLH_ASSISTANT_MAX_MODEL_CALLS": "25"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XLH_ADVANCED_AI_ENABLED", "true")
			for key, value := range values {
				t.Setenv(key, value)
			}
			if _, err := loadAdvancedAI(); err == nil {
				t.Fatal("expected invalid advanced AI configuration")
			}
		})
	}
}

func TestLoadMetricsToken(t *testing.T) {
	t.Setenv("XLH_ADVANCED_AI_ENABLED", "true")
	t.Setenv("XLH_LIGHTRAG_API_KEY", "test-lightrag-key-at-least-32-chars")
	t.Setenv("XLH_METRICS_TOKEN", "metrics-secret-at-least-thirty-two-chars")
	if cfg, err := loadWithTestPrompts(t); err != nil || cfg.MetricsToken == "" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	t.Setenv("XLH_METRICS_TOKEN", "short")
	if _, err := loadWithTestPrompts(t); err == nil {
		t.Fatal("expected short metrics token to fail")
	}
	t.Run("required for flash sale", func(t *testing.T) {
		t.Setenv("XLH_ADVANCED_AI_ENABLED", "false")
		t.Setenv("XLH_METRICS_TOKEN", "")
		t.Setenv("XLH_FLASH_SALE_ENABLED", "true")
		t.Setenv("XLH_REDIS_URL", "redis://:password@redis:6379/0")
		t.Setenv("XLH_ROCKETMQ_NAMESERVERS", "rmq:9876")
		if _, err := Load(); err == nil {
			t.Fatal("expected flash sale without metrics token to fail")
		}
	})
}

func loadWithTestPrompts(t *testing.T) (Config, error) {
	t.Helper()
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"XLH_DIRECT_PROMPT_FILE", "XLH_PLANNING_PROMPT_FILE", "XLH_RESEARCH_PROMPT_FILE", "XLH_SYNTHESIS_PROMPT_FILE"} {
		t.Setenv(key, promptPath)
	}
	t.Setenv("XLH_DATABASE_URL", "postgres://database")
	t.Setenv("XLH_AI_API_KEY", "key")
	t.Setenv("XLH_FLASH_SALE_ENABLED", "false")
	return Load()
}

func TestLoadFlashSale(t *testing.T) {
	t.Setenv("XLH_FLASH_SALE_ENABLED", "true")
	t.Setenv("XLH_REDIS_URL", "redis://:password@redis:6379/0")
	t.Setenv("XLH_REDIS_KEY_PREFIX", "xlh-prod")
	t.Setenv("XLH_ROCKETMQ_NAMESERVERS", "rmq-a:9876, rmq-b:9876")
	cfg, err := loadFlashSale()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || len(cfg.RocketMQNameServers) != 2 || cfg.ConsumerConcurrency != 16 || cfg.RocketMQSendTimeout != 3*time.Second ||
		cfg.RecoveryInterval != 5*time.Second || cfg.RecoveryStale != 30*time.Second || cfg.RecoveryLease != 30*time.Second || cfg.RecoveryBatch != 100 || cfg.ReleaseBatch != 100 {
		t.Fatalf("config=%+v", cfg)
	}

	t.Run("disabled needs no dependencies", func(t *testing.T) {
		t.Setenv("XLH_FLASH_SALE_ENABLED", "false")
		t.Setenv("XLH_REDIS_URL", "")
		t.Setenv("XLH_ROCKETMQ_NAMESERVERS", "")
		if cfg, err := loadFlashSale(); err != nil || cfg.Enabled {
			t.Fatalf("config=%+v err=%v", cfg, err)
		}
	})

	t.Run("enabled validates dependencies", func(t *testing.T) {
		t.Setenv("XLH_FLASH_SALE_ENABLED", "true")
		t.Setenv("XLH_REDIS_URL", "")
		if _, err := loadFlashSale(); err == nil {
			t.Fatal("expected missing Redis URL error")
		}
	})

	for name, values := range map[string]map[string]string{
		"malformed Redis URL":        {"XLH_REDIS_URL": "http://redis:6379", "XLH_ROCKETMQ_NAMESERVERS": "rmq:9876"},
		"unauthenticated Redis URL":  {"XLH_REDIS_URL": "redis://redis:6379/0", "XLH_ROCKETMQ_NAMESERVERS": "rmq:9876"},
		"malformed RocketMQ address": {"XLH_REDIS_URL": "redis://:password@redis:6379/0", "XLH_ROCKETMQ_NAMESERVERS": "rmq"},
		"invalid RocketMQ port":      {"XLH_REDIS_URL": "redis://:password@redis:6379/0", "XLH_ROCKETMQ_NAMESERVERS": "rmq:70000"},
		"invalid RocketMQ topic":     {"XLH_REDIS_URL": "redis://:password@redis:6379/0", "XLH_ROCKETMQ_NAMESERVERS": "rmq:9876", "XLH_ROCKETMQ_TOPIC": "bad topic"},
		"unsafe RocketMQ secret":     {"XLH_REDIS_URL": "redis://:password@redis:6379/0", "XLH_ROCKETMQ_NAMESERVERS": "rmq:9876", "XLH_ROCKETMQ_ACCESS_KEY": "access", "XLH_ROCKETMQ_SECRET_KEY": "secret\nvalue"},
		"excessive retry limit":      {"XLH_REDIS_URL": "redis://:password@redis:6379/0", "XLH_ROCKETMQ_NAMESERVERS": "rmq:9876", "XLH_ROCKETMQ_RETRY_LIMIT": "65"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			t.Setenv("XLH_FLASH_SALE_ENABLED", "true")
			for key, value := range values {
				t.Setenv(key, value)
			}
			if _, err := loadFlashSale(); err == nil {
				t.Fatal("expected invalid dependency configuration")
			}
		})
	}

	for key, value := range map[string]string{
		"XLH_FLASH_SALE_RECOVERY_INTERVAL": "0s",
		"XLH_FLASH_SALE_RECOVERY_STALE":    "later",
		"XLH_FLASH_SALE_RECOVERY_LEASE":    "-1s",
		"XLH_FLASH_SALE_RECOVERY_BATCH":    "1001",
	} {
		t.Run("rejects_"+key, func(t *testing.T) {
			t.Setenv("XLH_FLASH_SALE_ENABLED", "true")
			t.Setenv("XLH_REDIS_URL", "redis://:password@redis:6379/0")
			t.Setenv("XLH_ROCKETMQ_NAMESERVERS", "rmq:9876")
			t.Setenv(key, value)
			if _, err := loadFlashSale(); err == nil {
				t.Fatal("expected recovery configuration error")
			}
		})
	}
}
