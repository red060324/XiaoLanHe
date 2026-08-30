package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/jackc/pgx/v5/pgxpool"

	einoadapter "github.com/red060324/XiaoLanHe/internal/adapter/eino"
	"github.com/red060324/XiaoLanHe/internal/adapter/postgres"
	"github.com/red060324/XiaoLanHe/internal/adapter/websearch"
	"github.com/red060324/XiaoLanHe/internal/config"
	"github.com/red060324/XiaoLanHe/internal/entry"
	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		slog.Error("ping database", "error", err)
		os.Exit(1)
	}

	temperature := float32(0.4)
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:      cfg.AIAPIKey,
		BaseURL:     cfg.AIBaseURL,
		Model:       cfg.AIModel,
		Timeout:     cfg.AITimeout,
		Temperature: &temperature,
		ExtraFields: map[string]any{"enable_thinking": false},
	})
	if err != nil {
		slog.Error("create chat model", "error", err)
		os.Exit(1)
	}

	store := postgres.NewConversationStore(pool)
	knowledge := usecase.NewKnowledge(postgres.NewKnowledgeStore(pool), einoadapter.NewOpenAIEmbedder(cfg.AIBaseURL, cfg.AIAPIKey, cfg.EmbeddingModel, cfg.AITimeout))
	search := usecase.NewWebSearch(websearch.NewSearXNG(cfg.SearchEnabled, cfg.SearchProvider, cfg.SearchEndpoint, cfg.SearchTimeout))
	agentModel := einoadapter.NewAgentModel(chatModel, cfg.AIModel, cfg.PlanningPrompt, cfg.ResearchPrompt, cfg.DirectPrompt, cfg.SynthesisPrompt)
	agent := usecase.NewAgent(agentModel, usecase.NewResearch(agentModel, knowledge, search), agentModel)
	server := entry.NewHTTPWithServices(cfg.Address, usecase.NewChat(store, agent), knowledge, search, cfg.AgentMode, cfg.MinioBucket)
	slog.Info("xiaolanhe started", "address", cfg.Address, "model", cfg.AIModel)
	server.Spin()
}
