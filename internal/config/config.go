package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
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
	MetricsToken          string
	FlashSale             FlashSaleConfig
	AdvancedAI            AdvancedAIConfig
}

type AdvancedAIConfig struct {
	Enabled                                     bool
	LightRAG                                    LightRAGConfig
	OverallTimeout                              time.Duration
	MaxModelCalls, MaxToolCalls, MaxDelegations int
	CopilotMaxIterations                        int
	PlanningTimeout                             time.Duration
	PlanningMaxIterations, PlanningMaxToolCalls int
	SummaryTimeout                              time.Duration
	SummaryThreshold, SummaryCap, RecentWindow  int
	SummaryPromptVersion                        string
}

type LightRAGConfig struct {
	BaseURL, APIKey, Workspace, WorkingDirectory string
	CoreVersion, APIVersion                      string
	Timeout                                      time.Duration
}

type FlashSaleConfig struct {
	Enabled                bool
	RedisURL               string
	RedisKeyPrefix         string
	RedisRecoveryGrace     time.Duration
	RocketMQNameServers    []string
	RocketMQAccessKey      string
	RocketMQSecretKey      string
	RocketMQTopic          string
	RocketMQProducer       string
	RocketMQConsumer       string
	RocketMQSendTimeout    time.Duration
	RocketMQConsumeTimeout time.Duration
	ConsumerConcurrency    int
	RetryLimit             int
	RecoveryInterval       time.Duration
	RecoveryStale          time.Duration
	RecoveryLease          time.Duration
	RecoveryBatch          int
	ExpiryInterval         time.Duration
	ExpiryBatch            int
	ReleaseInterval        time.Duration
	ReleaseBatch           int
	ReleaseLease           time.Duration
}

var rocketMQNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,126}$`)

func Load() (Config, error) {
	flashSale, err := loadFlashSale()
	if err != nil {
		return Config{}, err
	}
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
	researchTimeout, err := positiveDuration("XLH_RESEARCH_TIMEOUT", "25s")
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
		MetricsToken:          strings.TrimSpace(os.Getenv("XLH_METRICS_TOKEN")),
		FlashSale:             flashSale,
	}
	cfg.AdvancedAI, err = loadAdvancedAI()
	if err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("XLH_DATABASE_URL is required")
	}
	if cfg.AIAPIKey == "" {
		return Config{}, errors.New("XLH_AI_API_KEY or DASHSCOPE_API_KEY is required")
	}
	if cfg.MetricsToken != "" && !validSecret(cfg.MetricsToken) {
		return Config{}, errors.New("XLH_METRICS_TOKEN must contain 32 to 512 characters without line breaks")
	}
	if (cfg.AdvancedAI.Enabled || cfg.FlashSale.Enabled) && cfg.MetricsToken == "" {
		return Config{}, errors.New("XLH_METRICS_TOKEN is required when advanced AI or flash sale is enabled")
	}
	return cfg, nil
}

func loadAdvancedAI() (AdvancedAIConfig, error) {
	enabled, err := strconv.ParseBool(env("XLH_ADVANCED_AI_ENABLED", "false"))
	if err != nil {
		return AdvancedAIConfig{}, errors.New("XLH_ADVANCED_AI_ENABLED must be true or false")
	}
	cfg := AdvancedAIConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	cfg.LightRAG = LightRAGConfig{
		BaseURL:          strings.TrimRight(strings.TrimSpace(env("XLH_LIGHTRAG_BASE_URL", "http://127.0.0.1:9621")), "/"),
		APIKey:           strings.TrimSpace(os.Getenv("XLH_LIGHTRAG_API_KEY")),
		Workspace:        strings.TrimSpace(env("XLH_LIGHTRAG_WORKSPACE", "xiaolanhe_v1")),
		WorkingDirectory: strings.TrimSpace(env("XLH_LIGHTRAG_WORKING_DIR", "/app/data/rag_storage")),
		CoreVersion:      "1.5.7", APIVersion: "0344",
	}
	parsed, parseErr := url.Parse(cfg.LightRAG.BaseURL)
	if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return AdvancedAIConfig{}, errors.New("XLH_LIGHTRAG_BASE_URL must be an absolute http(s) URL without credentials, query or fragment")
	}
	if !validSecret(cfg.LightRAG.APIKey) {
		return AdvancedAIConfig{}, errors.New("XLH_LIGHTRAG_API_KEY must contain 32 to 512 characters without line breaks when advanced AI is enabled")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`).MatchString(cfg.LightRAG.Workspace) {
		return AdvancedAIConfig{}, errors.New("XLH_LIGHTRAG_WORKSPACE is invalid")
	}
	if !strings.HasPrefix(cfg.LightRAG.WorkingDirectory, "/") || strings.Contains(cfg.LightRAG.WorkingDirectory, "..") {
		return AdvancedAIConfig{}, errors.New("XLH_LIGHTRAG_WORKING_DIR must be an absolute normalized path")
	}
	if cfg.LightRAG.Timeout, err = positiveDuration("XLH_LIGHTRAG_TIMEOUT", "15s"); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.OverallTimeout, err = positiveDuration("XLH_ASSISTANT_TOTAL_TIMEOUT", "45s"); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.MaxModelCalls, err = positiveInt("XLH_ASSISTANT_MAX_MODEL_CALLS", 12); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.MaxToolCalls, err = positiveInt("XLH_ASSISTANT_MAX_TOOL_CALLS", 12); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.MaxDelegations, err = positiveInt("XLH_ASSISTANT_MAX_DELEGATIONS", 3); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.CopilotMaxIterations, err = positiveInt("XLH_COPILOT_MAX_ITERATIONS", 4); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.PlanningTimeout, err = positiveDuration("XLH_PLANNING_TIMEOUT", "15s"); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.PlanningMaxIterations, err = positiveInt("XLH_PLANNING_MAX_ITERATIONS", 4); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.PlanningMaxToolCalls, err = positiveInt("XLH_PLANNING_MAX_TOOL_CALLS", 4); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.SummaryTimeout, err = positiveDuration("XLH_SUMMARY_TIMEOUT", "10s"); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.SummaryThreshold, err = positiveInt("XLH_SUMMARY_THRESHOLD", 12_000); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.SummaryCap, err = positiveInt("XLH_SUMMARY_CAP", 2_000); err != nil {
		return AdvancedAIConfig{}, err
	}
	if cfg.RecentWindow, err = positiveInt("XLH_RECENT_MESSAGE_WINDOW", 8); err != nil {
		return AdvancedAIConfig{}, err
	}
	cfg.SummaryPromptVersion = strings.TrimSpace(env("XLH_SUMMARY_PROMPT_VERSION", "summary-v1"))
	if cfg.LightRAG.Timeout > cfg.OverallTimeout || cfg.MaxModelCalls > 24 || cfg.MaxToolCalls > 24 || cfg.MaxDelegations > 4 {
		return AdvancedAIConfig{}, errors.New("advanced AI budget exceeds the supported maximum")
	}
	if cfg.CopilotMaxIterations > 4 || cfg.PlanningTimeout > cfg.OverallTimeout || cfg.PlanningMaxIterations > 4 || cfg.PlanningMaxToolCalls > 4 || cfg.SummaryTimeout > cfg.OverallTimeout || cfg.SummaryThreshold > 100_000 || cfg.SummaryCap > 10_000 || cfg.RecentWindow > 32 || !regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$").MatchString(cfg.SummaryPromptVersion) {
		return AdvancedAIConfig{}, errors.New("advanced AI component limit exceeds the supported maximum")
	}
	return cfg, nil
}

func loadFlashSale() (FlashSaleConfig, error) {
	enabled, err := strconv.ParseBool(env("XLH_FLASH_SALE_ENABLED", "false"))
	if err != nil {
		return FlashSaleConfig{}, errors.New("XLH_FLASH_SALE_ENABLED must be true or false")
	}
	cfg := FlashSaleConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	cfg.RedisURL = strings.TrimSpace(os.Getenv("XLH_REDIS_URL"))
	cfg.RedisKeyPrefix = env("XLH_REDIS_KEY_PREFIX", "xlh")
	cfg.RocketMQNameServers = splitNonEmpty(os.Getenv("XLH_ROCKETMQ_NAMESERVERS"))
	cfg.RocketMQAccessKey = os.Getenv("XLH_ROCKETMQ_ACCESS_KEY")
	cfg.RocketMQSecretKey = os.Getenv("XLH_ROCKETMQ_SECRET_KEY")
	cfg.RocketMQTopic = env("XLH_ROCKETMQ_TOPIC", "XLH_FLASH_SALE_V1")
	cfg.RocketMQProducer = env("XLH_ROCKETMQ_PRODUCER_GROUP", "xlh-flash-sale-producer-v1")
	cfg.RocketMQConsumer = env("XLH_ROCKETMQ_CONSUMER_GROUP", "xlh-flash-sale-consumer-v1")
	if cfg.RedisURL == "" || len(cfg.RocketMQNameServers) == 0 {
		return FlashSaleConfig{}, errors.New("XLH_REDIS_URL and XLH_ROCKETMQ_NAMESERVERS are required when flash sale is enabled")
	}
	redisURL, parseErr := url.Parse(cfg.RedisURL)
	redisPassword, hasRedisPassword := "", false
	if parseErr == nil && redisURL != nil && redisURL.User != nil {
		redisPassword, hasRedisPassword = redisURL.User.Password()
	}
	if parseErr != nil || (redisURL.Scheme != "redis" && redisURL.Scheme != "rediss") || redisURL.Host == "" ||
		redisURL.Fragment != "" || strings.ContainsAny(cfg.RedisURL, "\r\n") || !hasRedisPassword || redisPassword == "" {
		return FlashSaleConfig{}, errors.New("XLH_REDIS_URL must be an authenticated redis or rediss URL without a fragment")
	}
	for _, address := range cfg.RocketMQNameServers {
		host, port, splitErr := net.SplitHostPort(address)
		portNumber, portErr := strconv.Atoi(port)
		if splitErr != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n") || portErr != nil || portNumber < 1 || portNumber > 65535 {
			return FlashSaleConfig{}, errors.New("XLH_ROCKETMQ_NAMESERVERS must contain host:port addresses")
		}
	}
	if (cfg.RocketMQAccessKey == "") != (cfg.RocketMQSecretKey == "") {
		return FlashSaleConfig{}, errors.New("RocketMQ access and secret keys must be configured together")
	}
	if cfg.RocketMQAccessKey != "" && (!rocketMQNamePattern.MatchString(cfg.RocketMQAccessKey) || len(cfg.RocketMQSecretKey) > 512 || strings.ContainsAny(cfg.RocketMQSecretKey, "\r\n")) {
		return FlashSaleConfig{}, errors.New("RocketMQ credentials are invalid")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`).MatchString(cfg.RedisKeyPrefix) {
		return FlashSaleConfig{}, errors.New("XLH_REDIS_KEY_PREFIX is invalid")
	}
	if !rocketMQNamePattern.MatchString(cfg.RocketMQTopic) || !rocketMQNamePattern.MatchString(cfg.RocketMQProducer) ||
		!rocketMQNamePattern.MatchString(cfg.RocketMQConsumer) {
		return FlashSaleConfig{}, errors.New("RocketMQ topic and group names are invalid")
	}
	if cfg.RedisRecoveryGrace, err = positiveDuration("XLH_FLASH_SALE_RECOVERY_GRACE", "24h"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.RocketMQSendTimeout, err = positiveDuration("XLH_ROCKETMQ_SEND_TIMEOUT", "3s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.RocketMQConsumeTimeout, err = positiveDuration("XLH_ROCKETMQ_CONSUME_TIMEOUT", "30s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.RecoveryInterval, err = positiveDuration("XLH_FLASH_SALE_RECOVERY_INTERVAL", "5s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.RecoveryStale, err = positiveDuration("XLH_FLASH_SALE_RECOVERY_STALE", "30s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.RecoveryLease, err = positiveDuration("XLH_FLASH_SALE_RECOVERY_LEASE", "30s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.ExpiryInterval, err = positiveDuration("XLH_FLASH_SALE_EXPIRY_INTERVAL", "5s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.ReleaseInterval, err = positiveDuration("XLH_FLASH_SALE_RELEASE_INTERVAL", "2s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.ReleaseLease, err = positiveDuration("XLH_FLASH_SALE_RELEASE_LEASE", "30s"); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.ConsumerConcurrency, err = positiveInt("XLH_ROCKETMQ_CONSUMER_CONCURRENCY", 16); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.RetryLimit, err = positiveInt("XLH_ROCKETMQ_RETRY_LIMIT", 16); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.RecoveryBatch, err = positiveInt("XLH_FLASH_SALE_RECOVERY_BATCH", 100); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.ExpiryBatch, err = positiveInt("XLH_FLASH_SALE_EXPIRY_BATCH", 100); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.ReleaseBatch, err = positiveInt("XLH_FLASH_SALE_RELEASE_BATCH", 100); err != nil {
		return FlashSaleConfig{}, err
	}
	if cfg.ConsumerConcurrency > 128 || cfg.RetryLimit > 64 || cfg.RecoveryBatch > 1000 || cfg.ExpiryBatch > 1000 || cfg.ReleaseBatch > 1000 {
		return FlashSaleConfig{}, errors.New("flash sale worker limit exceeds maximum")
	}
	return cfg, nil
}

func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func validSecret(value string) bool {
	return len(value) >= 32 && len(value) <= 512 && !strings.ContainsAny(value, "\r\n")
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
