package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/jackc/pgx/v5/pgxpool"

	accountentry "github.com/red060324/XiaoLanHe/internal/account/entry"
	"github.com/red060324/XiaoLanHe/internal/account/repository/password"
	accountpg "github.com/red060324/XiaoLanHe/internal/account/repository/postgres"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	einoadapter "github.com/red060324/XiaoLanHe/internal/adapter/eino"
	"github.com/red060324/XiaoLanHe/internal/adapter/postgres"
	"github.com/red060324/XiaoLanHe/internal/adapter/websearch"
	catalogentry "github.com/red060324/XiaoLanHe/internal/catalog/entry"
	catalogpg "github.com/red060324/XiaoLanHe/internal/catalog/repository/postgres"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	communityentry "github.com/red060324/XiaoLanHe/internal/community/entry"
	communitypg "github.com/red060324/XiaoLanHe/internal/community/repository/postgres"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	"github.com/red060324/XiaoLanHe/internal/config"
	"github.com/red060324/XiaoLanHe/internal/entry"
	orderentry "github.com/red060324/XiaoLanHe/internal/order/entry"
	orderpg "github.com/red060324/XiaoLanHe/internal/order/repository/postgres"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	promotionentry "github.com/red060324/XiaoLanHe/internal/promotion/entry"
	promotionpg "github.com/red060324/XiaoLanHe/internal/promotion/repository/postgres"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
	"github.com/red060324/XiaoLanHe/internal/usecase"
	"github.com/red060324/XiaoLanHe/migrations"
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
	if err := postgres.Migrate(ctx, pool, migrations.Files); err != nil {
		slog.Error("initialize database", "error", err)
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
	search := usecase.NewWebSearch(websearch.NewSearXNG(cfg.SearchEnabled, cfg.SearchEndpoint, cfg.SearchTimeout))
	accountService := account.NewService(accountpg.NewStore(pool), password.Bcrypt{}, 7*24*time.Hour)
	catalogService := catalog.NewService(catalogpg.NewStore(pool))
	communityService := community.NewService(communitypg.NewStore(pool), catalogService)
	nodes := einoadapter.NewModelNodes(chatModel, cfg.AIModel, cfg.PlanningPrompt, cfg.DirectPrompt, cfg.SynthesisPrompt)
	research, err := einoadapter.NewResearchAgent(ctx, chatModel, cfg.ResearchPrompt, einoadapter.ResearchCapabilities{Knowledge: knowledge, Catalog: catalogService, Forum: communityService, Web: search, WebEnabled: cfg.SearchEnabled}, einoadapter.ResearchLimits{TotalTimeout: cfg.ResearchTimeout, ToolTimeout: cfg.ResearchToolTimeout, MaxIterations: cfg.ResearchMaxIterations, MaxToolCalls: cfg.ResearchMaxToolCalls})
	if err != nil {
		slog.Error("create research agent", "error", err)
		os.Exit(1)
	}
	assistant := usecase.NewAssistantFlow(nodes, research, nodes)
	server := entry.NewHTTPWithServices(cfg.Address, usecase.NewChat(store, assistant), knowledge, search, accountService, httpauth.RequireOrigin(cfg.PublicOrigin), httpauth.RequireRole(accountService, auth.RoleAdmin))
	accountentry.NewHTTP(accountService, cfg.CookieSecure, cfg.PublicOrigin).Register(server.Router())
	catalogentry.NewHTTP(catalogService, accountService, cfg.PublicOrigin).Register(server.Router())
	communityentry.NewHTTP(communityService, accountService, cfg.PublicOrigin).Register(server.Router())
	promotionService := promotion.NewService(promotionpg.NewStore(pool))
	promotionentry.NewHTTP(promotionService, accountService, cfg.PublicOrigin).Register(server.Router())
	orderentry.NewHTTP(order.NewService(orderpg.NewStore(pool), catalogService, promotionService), accountService, cfg.PublicOrigin).Register(server.Router())
	server.RegisterReadiness(pool.Ping)
	slog.Info("xiaolanhe started", "address", cfg.Address, "model", cfg.AIModel)
	server.Spin()
}
