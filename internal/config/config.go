package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address               string
	DatabaseURL           string
	AIBaseURL             string
	AIAPIKey              string
	AIModel               string
	AITimeout             time.Duration
	DirectPrompt          string
	PlanningPrompt        string
	ResearchPrompt        string
	SynthesisPrompt       string
	EmbeddingModel        string
	ResearchTimeout       time.Duration
	ResearchToolTimeout   time.Duration
	ResearchMaxIterations int
	ResearchMaxToolCalls  int
	SearchEnabled         bool
	SearchEndpoint        string
	SearchTimeout         time.Duration
	CookieSecure          bool
	PublicOrigin          string
}

func Load() (Config, error) {
	timeout, err := time.ParseDuration(env("XLH_AI_TIMEOUT", "60s"))
	if err != nil {
		return Config{}, fmt.Errorf("parse XLH_AI_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return Config{}, errors.New("XLH_AI_TIMEOUT must be positive")
	}
	searchTimeout, err := time.ParseDuration(env("XLH_SEARCH_TIMEOUT", "10s"))
	if err != nil || searchTimeout <= 0 {
		return Config{}, errors.New("XLH_SEARCH_TIMEOUT must be a positive duration")
	}
	researchTimeout, err := positiveDuration("XLH_RESEARCH_TIMEOUT", "30s")
	if err != nil {
		return Config{}, err
	}
	researchToolTimeout, err := positiveDuration("XLH_RESEARCH_TOOL_TIMEOUT", "10s")
	if err != nil {
		return Config{}, err
	}
	researchMaxIterations, err := positiveInt("XLH_RESEARCH_MAX_ITERATIONS", 6)
	if err != nil {
		return Config{}, err
	}
	researchMaxToolCalls, err := positiveInt("XLH_RESEARCH_MAX_TOOL_CALLS", 8)
	if err != nil {
		return Config{}, err
	}
	promptPath := env("XLH_DIRECT_PROMPT_FILE", "prompts/main-agent-direct.md")
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return Config{}, err
	}
	planningPrompt, err := os.ReadFile(env("XLH_PLANNING_PROMPT_FILE", "prompts/main-agent-planning.md"))
	if err != nil {
		return Config{}, err
	}
	researchPrompt, err := os.ReadFile(env("XLH_RESEARCH_PROMPT_FILE", "prompts/search-agent-decomposition.md"))
	if err != nil {
		return Config{}, err
	}
	synthesisPrompt, err := os.ReadFile(env("XLH_SYNTHESIS_PROMPT_FILE", "prompts/synthesis.md"))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Address:               listenAddress(),
		DatabaseURL:           os.Getenv("XLH_DATABASE_URL"),
		AIBaseURL:             normalizeAIBaseURL(env("XLH_AI_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1")),
		AIAPIKey:              first(os.Getenv("XLH_AI_API_KEY"), os.Getenv("DASHSCOPE_API_KEY")),
		AIModel:               env("XLH_AI_CHAT_MODEL", "qwen3.5-flash"),
		AITimeout:             timeout,
		DirectPrompt:          string(prompt),
		PlanningPrompt:        string(planningPrompt),
		ResearchPrompt:        string(researchPrompt),
		SynthesisPrompt:       string(synthesisPrompt),
		EmbeddingModel:        env("XLH_AI_EMBEDDING_MODEL", "text-embedding-v4"),
		ResearchTimeout:       researchTimeout,
		ResearchToolTimeout:   researchToolTimeout,
		ResearchMaxIterations: researchMaxIterations,
		ResearchMaxToolCalls:  researchMaxToolCalls,
		SearchEnabled:         strings.EqualFold(env("XLH_SEARCH_ENABLED", "true"), "true"),
		SearchEndpoint:        env("SEARXNG_BASE_URL", "http://127.0.0.1:8080"),
		SearchTimeout:         searchTimeout,
		CookieSecure:          !strings.EqualFold(env("XLH_COOKIE_SECURE", "true"), "false"),
		PublicOrigin:          strings.TrimRight(os.Getenv("XLH_PUBLIC_ORIGIN"), "/"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("XLH_DATABASE_URL is required")
	}
	if cfg.AIAPIKey == "" {
		return Config{}, errors.New("XLH_AI_API_KEY or DASHSCOPE_API_KEY is required")
	}
	return cfg, nil
}

func positiveDuration(key, fallback string) (time.Duration, error) {
	value, err := time.ParseDuration(env(key, fallback))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return value, nil
}

func positiveInt(key string, fallback int) (int, error) {
	value, err := strconv.Atoi(env(key, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func listenAddress() string {
	if address := os.Getenv("XLH_ADDRESS"); address != "" {
		return address
	}
	return ":" + env("PORT", "8088")
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
