package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	Address      string
	DatabaseURL  string
	AIBaseURL    string
	AIAPIKey     string
	AIModel      string
	AITimeout    time.Duration
	DirectPrompt string
}

func Load() (Config, error) {
	timeout, err := time.ParseDuration(env("XLH_AI_TIMEOUT", "60s"))
	if err != nil {
		return Config{}, fmt.Errorf("parse XLH_AI_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return Config{}, errors.New("XLH_AI_TIMEOUT must be positive")
	}
	promptPath := env("XLH_DIRECT_PROMPT_FILE", "xiaolanhe-agent/src/main/resources/prompts/main-agent-direct.md")
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Address:      env("XLH_ADDRESS", ":8088"),
		DatabaseURL:  os.Getenv("XLH_DATABASE_URL"),
		AIBaseURL:    normalizeAIBaseURL(env("XLH_AI_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1")),
		AIAPIKey:     first(os.Getenv("XLH_AI_API_KEY"), os.Getenv("DASHSCOPE_API_KEY")),
		AIModel:      env("XLH_AI_CHAT_MODEL", "qwen3.5-flash"),
		AITimeout:    timeout,
		DirectPrompt: string(prompt),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("XLH_DATABASE_URL is required")
	}
	if cfg.AIAPIKey == "" {
		return Config{}, errors.New("XLH_AI_API_KEY or DASHSCOPE_API_KEY is required")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeAIBaseURL(value string) string {
	value = strings.TrimRight(value, "/")
	if strings.HasSuffix(value, "/compatible-mode") {
		return value + "/v1"
	}
	return value
}
