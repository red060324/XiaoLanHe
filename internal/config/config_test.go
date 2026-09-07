package config

import (
	"os"
	"path/filepath"
	"strings"
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
	t.Setenv("XLH_DATABASE_URL", "xlh:test@tcp(localhost:3306)/xiaolanhe")
	t.Setenv("XLH_AI_API_KEY", "key")
	t.Setenv("DASHSCOPE_API_KEY", "")
	t.Setenv("XLH_AI_CHAT_MODEL", "model")
	t.Setenv("XLH_AI_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode")
	t.Setenv("XLH_ADDRESS", "")
	t.Setenv("PORT", "10000")
	t.Setenv("XLH_FLASH_SALE_ENABLED", "false")
	t.Setenv("XLH_DATABASE_ALLOW_INSECURE", "true")
	t.Setenv("XLH_LIGHTRAG_API_KEY", "test-lightrag-key-at-least-32-chars")
	setLightRAGFenceEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != ":10000" || cfg.DatabaseURL != "xlh:test@tcp(localhost:3306)/xiaolanhe" || cfg.AIAPIKey != "key" || cfg.AIModel != "model" || cfg.AITimeout != 60*time.Second || cfg.DirectPrompt != "direct prompt" || cfg.AIBaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" || cfg.ResearchTimeout != 25*time.Second || cfg.ResearchToolTimeout != 10*time.Second || cfg.ResearchMaxIterations != 6 || cfg.ResearchMaxToolCalls != 8 {
		t.Fatalf("config = %#v", cfg)
	}

	t.Run("requires database URL", func(t *testing.T) {
		t.Setenv("XLH_DATABASE_URL", "")
		if _, err := Load(); err == nil || err.Error() != "XLH_DATABASE_URL is required" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("accepts DashScope key fallback", func(t *testing.T) {
		t.Setenv("XLH_DATABASE_URL", "xlh:test@tcp(localhost:3306)/xiaolanhe")
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
	t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "true")
	t.Setenv("XLH_LIGHTRAG_API_KEY", "test-lightrag-key-at-least-32-chars")
	setLightRAGFenceEnv(t)
	cfg, err := loadAdvancedAI()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.LightRAG.BaseURL != "http://lightrag:9621" || !cfg.LightRAG.AllowInsecure || cfg.LightRAG.Workspace != "xiaolanhe_v1" || cfg.LightRAG.CoreVersion != "1.5.7" || cfg.LightRAG.APIVersion != "0344" || cfg.LightRAG.FencePath != "/rebuild-fence" || cfg.LightRAG.FenceGeneration != "test-generation" || cfg.LightRAG.FenceContractSHA256 != "sha256:"+strings.Repeat("a", 64) || cfg.OverallTimeout != 45*time.Second || cfg.MaxModelCalls != 12 || cfg.MaxToolCalls != 12 || cfg.MaxDelegations != 3 || cfg.CopilotMaxIterations != 4 || cfg.PlanningTimeout != 15*time.Second || cfg.PlanningMaxIterations != 4 || cfg.PlanningMaxToolCalls != 4 || cfg.SummaryTimeout != 10*time.Second || cfg.SummaryThreshold != 12_000 || cfg.SummaryCap != 2_000 || cfg.RecentWindow != 8 || cfg.SummaryPromptVersion != "summary-v1" {
		t.Fatalf("config=%+v", cfg)
	}
	t.Run("defaults to HTTPS", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "")
		value, err := loadAdvancedAI()
		if err != nil || value.LightRAG.BaseURL != "https://127.0.0.1:9621" || value.LightRAG.AllowInsecure {
			t.Fatalf("value=%+v err=%v", value, err)
		}
	})
	t.Run("rejects HTTP without explicit opt-in", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "http://lightrag:9621")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "false")
		if _, err := loadAdvancedAI(); err == nil || err.Error() != "XLH_LIGHTRAG_BASE_URL must use https unless XLH_LIGHTRAG_ALLOW_INSECURE=true" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("accepts HTTP with explicit opt-in", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "http://controlled-lightrag:9621")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "true")
		value, err := loadAdvancedAI()
		if err != nil || value.LightRAG.BaseURL != "http://controlled-lightrag:9621" || !value.LightRAG.AllowInsecure {
			t.Fatalf("value=%+v err=%v", value, err)
		}
	})
	t.Run("accepts HTTPS without insecure opt-in", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "https://lightrag.example.com/")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "false")
		value, err := loadAdvancedAI()
		if err != nil || value.LightRAG.BaseURL != "https://lightrag.example.com" || value.LightRAG.AllowInsecure {
			t.Fatalf("value=%+v err=%v", value, err)
		}
	})
	t.Run("rejects invalid insecure opt-in", func(t *testing.T) {
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "sometimes")
		if _, err := loadAdvancedAI(); err == nil || err.Error() != "XLH_LIGHTRAG_ALLOW_INSECURE must be true or false" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("disabled still configures LightRAG", func(t *testing.T) {
		t.Setenv("XLH_ADVANCED_AI_ENABLED", "false")
		t.Setenv("XLH_LIGHTRAG_API_KEY", "test-lightrag-key-at-least-32-chars")
		setLightRAGFenceEnv(t)
		if value, err := loadAdvancedAI(); err != nil || value.Enabled || value.LightRAG.BaseURL != "http://lightrag:9621" || value.LightRAG.Timeout != 15*time.Second {
			t.Fatalf("value=%+v err=%v", value, err)
		}
	})
	t.Run("disabled still rejects HTTP without explicit opt-in", func(t *testing.T) {
		t.Setenv("XLH_ADVANCED_AI_ENABLED", "false")
		t.Setenv("XLH_LIGHTRAG_BASE_URL", "http://lightrag:9621")
		t.Setenv("XLH_LIGHTRAG_ALLOW_INSECURE", "false")
		if _, err := loadAdvancedAI(); err == nil || err.Error() != "XLH_LIGHTRAG_BASE_URL must use https unless XLH_LIGHTRAG_ALLOW_INSECURE=true" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("disabled rejects missing LightRAG key", func(t *testing.T) {
		t.Setenv("XLH_ADVANCED_AI_ENABLED", "false")
		t.Setenv("XLH_LIGHTRAG_API_KEY", "")
		if _, err := loadAdvancedAI(); err == nil {
			t.Fatal("expected missing LightRAG key to fail")
		}
	})
	for name, values := range map[string]map[string]string{
		"missing key":           {"XLH_LIGHTRAG_API_KEY": ""},
		"short key":             {"XLH_LIGHTRAG_API_KEY": "secret"},
		"unsafe URL":            {"XLH_LIGHTRAG_API_KEY": "test-lightrag-key-at-least-32-chars", "XLH_LIGHTRAG_BASE_URL": "http://user:pass@lightrag:9621"},
		"bad workspace":         {"XLH_LIGHTRAG_API_KEY": "test-lightrag-key-at-least-32-chars", "XLH_LIGHTRAG_BASE_URL": "http://lightrag:9621", "XLH_LIGHTRAG_WORKSPACE": "bad-workspace"},
		"missing fence path":    {"XLH_LIGHTRAG_REBUILD_FENCE_DIR": ""},
		"relative fence path":   {"XLH_LIGHTRAG_REBUILD_FENCE_DIR": "relative"},
		"missing generation":    {"XLH_LIGHTRAG_DEPLOYMENT_GENERATION": ""},
		"bad generation":        {"XLH_LIGHTRAG_DEPLOYMENT_GENERATION": "bad generation"},
		"missing contract hash": {"XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256": ""},
		"bad contract hash":     {"XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256": "sha256:BAD"},
		"excessive calls":       {"XLH_LIGHTRAG_API_KEY": "test-lightrag-key-at-least-32-chars", "XLH_LIGHTRAG_BASE_URL": "http://lightrag:9621", "XLH_LIGHTRAG_WORKSPACE": "xiaolanhe_v1", "XLH_ASSISTANT_MAX_MODEL_CALLS": "25"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XLH_ADVANCED_AI_ENABLED", "true")
			setLightRAGFenceEnv(t)
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
	setLightRAGFenceEnv(t)
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
		t.Setenv("XLH_REDIS_ALLOW_INSECURE", "true")
		t.Setenv("XLH_ROCKETMQ_NAMESERVERS", "rmq:9876")
		t.Setenv("XLH_ROCKETMQ_ALLOW_INSECURE", "true")
		if _, err := Load(); err == nil || err.Error() != "XLH_METRICS_TOKEN is required when advanced AI or flash sale is enabled" {
			t.Fatalf("err=%v", err)
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
	t.Setenv("XLH_DATABASE_URL", "xlh:test@tcp(localhost:3306)/xiaolanhe")
	t.Setenv("XLH_AI_API_KEY", "key")
	t.Setenv("XLH_FLASH_SALE_ENABLED", "false")
	t.Setenv("XLH_DATABASE_ALLOW_INSECURE", "true")
	t.Setenv("XLH_LIGHTRAG_API_KEY", "test-lightrag-key-at-least-32-chars")
	setLightRAGFenceEnv(t)
	return Load()
}

func setLightRAGFenceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XLH_LIGHTRAG_REBUILD_FENCE_DIR", "/rebuild-fence")
	t.Setenv("XLH_LIGHTRAG_DEPLOYMENT_GENERATION", "test-generation")
	t.Setenv("XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256", "sha256:"+strings.Repeat("a", 64))
}

func TestLoadDatabase(t *testing.T) {
	t.Setenv("XLH_DATABASE_ALLOW_INSECURE", "true")
	cfg, err := loadDatabase()
	if err != nil || cfg.MaxOpenConnections != 25 || cfg.MaxIdleConnections != 10 || cfg.ConnectionMaxLifetime != 30*time.Minute || cfg.ConnectionMaxIdleTime != 5*time.Minute || cfg.MigrationLockTimeout != 30*time.Second || !cfg.AllowInsecure {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	for name, values := range map[string]map[string]string{
		"idle exceeds open":          {"XLH_DATABASE_MAX_OPEN_CONNECTIONS": "2", "XLH_DATABASE_MAX_IDLE_CONNECTIONS": "3"},
		"idle time exceeds lifetime": {"XLH_DATABASE_CONNECTION_MAX_LIFETIME": "1m", "XLH_DATABASE_CONNECTION_MAX_IDLE_TIME": "2m"},
		"bad insecure flag":          {"XLH_DATABASE_ALLOW_INSECURE": "sometimes"},
	} {
		t.Run(name, func(t *testing.T) {
			for key, value := range values {
				t.Setenv(key, value)
			}
			if _, err := loadDatabase(); err == nil {
				t.Fatal("expected database configuration error")
			}
		})
	}
	t.Run("production TLS settings", func(t *testing.T) {
		t.Setenv("XLH_DATABASE_ALLOW_INSECURE", "false")
		t.Setenv("XLH_DATABASE_TLS_CA_FILE", "/run/secrets/mysql-ca.pem")
		t.Setenv("XLH_DATABASE_TLS_SERVER_NAME", "mysql.internal.example")
		config, err := loadDatabase()
		if err != nil || config.TLSCAFile != "/run/secrets/mysql-ca.pem" || config.TLSServerName != "mysql.internal.example" {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	})
	t.Run("production requires TLS settings", func(t *testing.T) {
		t.Setenv("XLH_DATABASE_ALLOW_INSECURE", "false")
		if _, err := loadDatabase(); err == nil {
			t.Fatal("expected production TLS configuration error")
		}
	})
	t.Run("client key pair is atomic", func(t *testing.T) {
		t.Setenv("XLH_DATABASE_TLS_CERT_FILE", "/run/secrets/mysql-client.pem")
		if _, err := loadDatabase(); err == nil {
			t.Fatal("expected incomplete client key pair error")
		}
	})
	for name, values := range map[string]map[string]string{
		"relative CA":   {"XLH_DATABASE_TLS_CA_FILE": "relative/ca.pem"},
		"unclean CA":    {"XLH_DATABASE_TLS_CA_FILE": "/run/../secrets/ca.pem"},
		"relative cert": {"XLH_DATABASE_TLS_CERT_FILE": "client.pem", "XLH_DATABASE_TLS_KEY_FILE": "/run/secrets/client.key"},
		"relative key":  {"XLH_DATABASE_TLS_CERT_FILE": "/run/secrets/client.pem", "XLH_DATABASE_TLS_KEY_FILE": "client.key"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XLH_DATABASE_ALLOW_INSECURE", "true")
			for key, value := range values {
				t.Setenv(key, value)
			}
			if _, err := LoadDatabaseConfig(); err == nil {
				t.Fatal("expected invalid TLS file path")
			}
		})
	}
}

func TestLoadFlashSale(t *testing.T) {
	t.Setenv("XLH_FLASH_SALE_ENABLED", "true")
	t.Setenv("XLH_REDIS_URL", "rediss://:password@redis:6379/0")
	t.Setenv("XLH_REDIS_ALLOW_INSECURE", "false")
	t.Setenv("XLH_REDIS_KEY_PREFIX", "xlh-prod")
	t.Setenv("XLH_ROCKETMQ_NAMESERVERS", "rmq-a:9876, rmq-b:9876")
	t.Setenv("XLH_ROCKETMQ_ACCESS_KEY", "access-key")
	t.Setenv("XLH_ROCKETMQ_SECRET_KEY", "secret-key")
	t.Setenv("XLH_ROCKETMQ_ALLOW_INSECURE", "false")
	cfg, err := loadFlashSale()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.RedisAllowInsecure || cfg.RocketMQAllowInsecure || len(cfg.RocketMQNameServers) != 2 || cfg.ConsumerConcurrency != 16 || cfg.RocketMQSendTimeout != 3*time.Second ||
		cfg.RecoveryInterval != 5*time.Second || cfg.RecoveryStale != 30*time.Second || cfg.RecoveryLease != 30*time.Second || cfg.RecoveryBatch != 100 || cfg.ReleaseBatch != 100 {
		t.Fatalf("config=%+v", cfg)
	}

	t.Run("disabled needs no dependencies", func(t *testing.T) {
		t.Setenv("XLH_FLASH_SALE_ENABLED", "false")
		t.Setenv("XLH_REDIS_URL", "")
		t.Setenv("XLH_REDIS_ALLOW_INSECURE", "not-a-boolean")
		t.Setenv("XLH_ROCKETMQ_NAMESERVERS", "")
		t.Setenv("XLH_ROCKETMQ_ACCESS_KEY", "partial-access-key")
		t.Setenv("XLH_ROCKETMQ_SECRET_KEY", "")
		t.Setenv("XLH_ROCKETMQ_ALLOW_INSECURE", "not-a-boolean")
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

	t.Run("rejects plaintext Redis without explicit opt-in", func(t *testing.T) {
		t.Setenv("XLH_REDIS_URL", "redis://:password@redis:6379/0")
		t.Setenv("XLH_REDIS_ALLOW_INSECURE", "false")
		if _, err := loadFlashSale(); err == nil || err.Error() != "XLH_REDIS_URL must use rediss unless XLH_REDIS_ALLOW_INSECURE=true" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("accepts plaintext Redis with explicit local opt-in", func(t *testing.T) {
		t.Setenv("XLH_REDIS_URL", "redis://:password@redis:6379/0")
		t.Setenv("XLH_REDIS_ALLOW_INSECURE", "true")
		value, err := loadFlashSale()
		if err != nil || !value.RedisAllowInsecure {
			t.Fatalf("config=%+v err=%v", value, err)
		}
	})

	t.Run("rejects invalid Redis insecure opt-in", func(t *testing.T) {
		t.Setenv("XLH_REDIS_ALLOW_INSECURE", "sometimes")
		if _, err := loadFlashSale(); err == nil || err.Error() != "XLH_REDIS_ALLOW_INSECURE must be true or false" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("rejects missing RocketMQ ACL without explicit opt-in", func(t *testing.T) {
		t.Setenv("XLH_ROCKETMQ_ACCESS_KEY", "")
		t.Setenv("XLH_ROCKETMQ_SECRET_KEY", "")
		t.Setenv("XLH_ROCKETMQ_ALLOW_INSECURE", "false")
		if _, err := loadFlashSale(); err == nil || err.Error() != "RocketMQ access and secret keys are required unless XLH_ROCKETMQ_ALLOW_INSECURE=true" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("accepts missing RocketMQ ACL with explicit local opt-in", func(t *testing.T) {
		t.Setenv("XLH_ROCKETMQ_ACCESS_KEY", "")
		t.Setenv("XLH_ROCKETMQ_SECRET_KEY", "")
		t.Setenv("XLH_ROCKETMQ_ALLOW_INSECURE", "true")
		value, err := loadFlashSale()
		if err != nil || !value.RocketMQAllowInsecure {
			t.Fatalf("config=%+v err=%v", value, err)
		}
	})

	t.Run("rejects invalid RocketMQ insecure opt-in", func(t *testing.T) {
		t.Setenv("XLH_ROCKETMQ_ALLOW_INSECURE", "sometimes")
		if _, err := loadFlashSale(); err == nil || err.Error() != "XLH_ROCKETMQ_ALLOW_INSECURE must be true or false" {
			t.Fatalf("err=%v", err)
		}
	})

	for name, credentials := range map[string]map[string]string{
		"access key only": {"XLH_ROCKETMQ_ACCESS_KEY": "access-key", "XLH_ROCKETMQ_SECRET_KEY": ""},
		"secret key only": {"XLH_ROCKETMQ_ACCESS_KEY": "", "XLH_ROCKETMQ_SECRET_KEY": "secret-key"},
	} {
		t.Run("rejects partial RocketMQ credentials with insecure opt-in/"+name, func(t *testing.T) {
			t.Setenv("XLH_ROCKETMQ_ALLOW_INSECURE", "true")
			for key, value := range credentials {
				t.Setenv(key, value)
			}
			if _, err := loadFlashSale(); err == nil || err.Error() != "RocketMQ access and secret keys must be configured together" {
				t.Fatalf("err=%v", err)
			}
		})
	}

	for name, values := range map[string]map[string]string{
		"malformed Redis URL":        {"XLH_REDIS_URL": "http://redis:6379"},
		"unauthenticated Redis URL":  {"XLH_REDIS_URL": "rediss://redis:6379/0"},
		"malformed RocketMQ address": {"XLH_ROCKETMQ_NAMESERVERS": "rmq"},
		"invalid RocketMQ port":      {"XLH_ROCKETMQ_NAMESERVERS": "rmq:70000"},
		"invalid RocketMQ topic":     {"XLH_ROCKETMQ_TOPIC": "bad topic"},
		"unsafe RocketMQ secret":     {"XLH_ROCKETMQ_SECRET_KEY": "secret\nvalue"},
		"blank RocketMQ secret":      {"XLH_ROCKETMQ_SECRET_KEY": "   "},
		"excessive retry limit":      {"XLH_ROCKETMQ_RETRY_LIMIT": "65"},
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
			t.Setenv(key, value)
			if _, err := loadFlashSale(); err == nil {
				t.Fatal("expected recovery configuration error")
			}
		})
	}
}
