package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"

	accountentry "github.com/red060324/XiaoLanHe/internal/account/entry"
	"github.com/red060324/XiaoLanHe/internal/account/repository/password"
	accountpg "github.com/red060324/XiaoLanHe/internal/account/repository/postgres"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	einoadapter "github.com/red060324/XiaoLanHe/internal/adapter/eino"
	"github.com/red060324/XiaoLanHe/internal/adapter/postgres"
	"github.com/red060324/XiaoLanHe/internal/adapter/websearch"
	assistantagent "github.com/red060324/XiaoLanHe/internal/assistant/agent/eino"
	assistantentity "github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistantentry "github.com/red060324/XiaoLanHe/internal/assistant/entry"
	assistantlightrag "github.com/red060324/XiaoLanHe/internal/assistant/repository/lightrag"
	assistantpg "github.com/red060324/XiaoLanHe/internal/assistant/repository/postgres"
	assistantskill "github.com/red060324/XiaoLanHe/internal/assistant/skill"
	assistantuc "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	catalogentry "github.com/red060324/XiaoLanHe/internal/catalog/entry"
	catalogpg "github.com/red060324/XiaoLanHe/internal/catalog/repository/postgres"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	communityentry "github.com/red060324/XiaoLanHe/internal/community/entry"
	communitypg "github.com/red060324/XiaoLanHe/internal/community/repository/postgres"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	"github.com/red060324/XiaoLanHe/internal/config"
	"github.com/red060324/XiaoLanHe/internal/entry"
	flashentry "github.com/red060324/XiaoLanHe/internal/flashsale/entry"
	flashorder "github.com/red060324/XiaoLanHe/internal/flashsale/repository/order"
	flashpg "github.com/red060324/XiaoLanHe/internal/flashsale/repository/postgres"
	flashredis "github.com/red060324/XiaoLanHe/internal/flashsale/repository/redis"
	flashmq "github.com/red060324/XiaoLanHe/internal/flashsale/repository/rocketmq"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	knowledgeentry "github.com/red060324/XiaoLanHe/internal/knowledge/entry"
	knowledgelightrag "github.com/red060324/XiaoLanHe/internal/knowledge/repository/lightrag"
	knowledgeuc "github.com/red060324/XiaoLanHe/internal/knowledge/usecase"
	orderentry "github.com/red060324/XiaoLanHe/internal/order/entry"
	orderpg "github.com/red060324/XiaoLanHe/internal/order/repository/postgres"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
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
	providerChatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
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
	chatModel := einoadapter.ObserveChatModel(providerChatModel, platformmetrics.Default())

	store := postgres.NewConversationStore(pool)
	assistantProfileStore := assistantpg.NewProfileStore(pool)
	legacyKnowledge := usecase.NewKnowledge(postgres.NewKnowledgeStore(pool), einoadapter.NewOpenAIEmbedder(cfg.AIBaseURL, cfg.AIAPIKey, cfg.EmbeddingModel, cfg.AITimeout))
	var researchKnowledge einoadapter.KnowledgeSearch = legacyKnowledge
	var advancedKnowledge *knowledgeuc.Service
	if cfg.AdvancedAI.Enabled {
		client, clientErr := knowledgelightrag.NewClient(knowledgelightrag.Config{
			BaseURL: cfg.AdvancedAI.LightRAG.BaseURL, APIKey: cfg.AdvancedAI.LightRAG.APIKey,
			Workspace: cfg.AdvancedAI.LightRAG.Workspace, WorkingDirectory: cfg.AdvancedAI.LightRAG.WorkingDirectory,
			CoreVersion: cfg.AdvancedAI.LightRAG.CoreVersion, APIVersion: cfg.AdvancedAI.LightRAG.APIVersion, Timeout: cfg.AdvancedAI.LightRAG.Timeout,
		})
		if clientErr != nil {
			slog.Error("configure LightRAG", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		advancedKnowledge = knowledgeuc.NewService(client)
		if readyErr := advancedKnowledge.Ready(ctx); readyErr != nil {
			slog.Error("verify LightRAG", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		researchKnowledge = assistantlightrag.NewSearchAdapter(advancedKnowledge)
	}
	search := usecase.NewWebSearch(websearch.NewSearXNG(cfg.SearchEnabled, cfg.SearchEndpoint, cfg.SearchTimeout))
	accountService := account.NewService(accountpg.NewStore(pool), password.Bcrypt{}, 7*24*time.Hour)
	catalogService := catalog.NewService(catalogpg.NewStore(pool))
	communityService := community.NewService(communitypg.NewStore(pool), catalogService)
	nodes := einoadapter.NewModelNodes(chatModel, cfg.AIModel, cfg.PlanningPrompt, cfg.DirectPrompt, cfg.SynthesisPrompt)
	research, err := einoadapter.NewResearchAgent(ctx, chatModel, cfg.ResearchPrompt, einoadapter.ResearchCapabilities{Knowledge: researchKnowledge, Catalog: catalogService, Forum: communityService, Web: search, WebEnabled: cfg.SearchEnabled}, einoadapter.ResearchLimits{TotalTimeout: cfg.ResearchTimeout, ToolTimeout: cfg.ResearchToolTimeout, MaxIterations: cfg.ResearchMaxIterations, MaxToolCalls: cfg.ResearchMaxToolCalls})
	if err != nil {
		slog.Error("create research agent", "error", err)
		os.Exit(1)
	}
	var assistant usecase.Assistant = usecase.NewAssistantFlow(nodes, research, nodes)
	if cfg.AdvancedAI.Enabled {
		registry, registryErr := assistantskill.Load(assistantentity.BudgetLimit{
			ModelCalls: cfg.AdvancedAI.MaxModelCalls, ToolCalls: cfg.AdvancedAI.MaxToolCalls,
			Delegations: cfg.AdvancedAI.MaxDelegations, TimeoutMilliseconds: cfg.AdvancedAI.OverallTimeout.Milliseconds(),
		})
		if registryErr != nil {
			slog.Error("load assistant skills", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		planningAgent, planningErr := assistantagent.NewPlanningAgent(chatModel, catalogService, cfg.AdvancedAI.PlanningMaxIterations, cfg.AdvancedAI.PlanningMaxToolCalls, cfg.AdvancedAI.PlanningTimeout)
		if planningErr != nil {
			slog.Error("create planning agent", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		copilot, copilotErr := assistantagent.NewGameCopilot(chatModel, research, planningAgent, cfg.AdvancedAI.CopilotMaxIterations)
		if copilotErr != nil {
			slog.Error("create game copilot", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		advancedNodes := assistantagent.NewAdvancedNodes(chatModel)
		advancedAssistant, advancedErr := assistantuc.NewAdvancedAssistant(advancedNodes, advancedNodes, copilot, nodes, registry, assistantProfileStore, assistantuc.AdvancedConfig{
			Limit:      assistantentity.BudgetLimit{ModelCalls: cfg.AdvancedAI.MaxModelCalls, ToolCalls: cfg.AdvancedAI.MaxToolCalls, Delegations: cfg.AdvancedAI.MaxDelegations, TimeoutMilliseconds: cfg.AdvancedAI.OverallTimeout.Milliseconds()},
			WebEnabled: cfg.SearchEnabled,
		})
		if advancedErr != nil {
			slog.Error("create advanced assistant", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		assistant = advancedAssistant
	}
	chatService := usecase.NewChat(store, assistant)
	if cfg.AdvancedAI.Enabled {
		memory, memoryErr := assistantuc.NewMemoryService(
			assistantpg.NewMemoryStore(pool),
			assistantagent.NewSummaryNode(chatModel),
			assistantuc.MemoryConfig{
				Threshold: cfg.AdvancedAI.SummaryThreshold, SummaryCap: cfg.AdvancedAI.SummaryCap, RecentWindow: cfg.AdvancedAI.RecentWindow,
				PromptVersion: cfg.AdvancedAI.SummaryPromptVersion, Timeout: cfg.AdvancedAI.SummaryTimeout,
			},
		)
		if memoryErr != nil {
			slog.Error("configure conversation memory", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		chatService.WithMemory(memory)
	}
	httpKnowledge := legacyKnowledge
	if advancedKnowledge != nil {
		httpKnowledge = nil
	}
	server := entry.NewHTTPWithServices(cfg.Address, chatService, httpKnowledge, search, accountService, httpauth.RequireOrigin(cfg.PublicOrigin), httpauth.RequireRole(accountService, auth.RoleAdmin))
	server.RegisterMetrics(cfg.MetricsToken, platformmetrics.Default())
	accountentry.NewHTTP(accountService, cfg.CookieSecure, cfg.PublicOrigin).Register(server.Router())
	assistantentry.NewProfileHTTP(assistantuc.NewProfileService(assistantProfileStore), accountService, cfg.PublicOrigin).Register(server.Router())
	if advancedKnowledge != nil {
		knowledgeentry.NewHTTP(advancedKnowledge, accountService, cfg.PublicOrigin).Register(server.Router())
	}
	catalogentry.NewHTTP(catalogService, accountService, cfg.PublicOrigin).Register(server.Router())
	communityentry.NewHTTP(communityService, accountService, cfg.PublicOrigin).Register(server.Router())
	promotionService := promotion.NewService(promotionpg.NewStore(pool))
	promotionentry.NewHTTP(promotionService, accountService, cfg.PublicOrigin).Register(server.Router())
	orderService := order.NewService(orderpg.NewStore(pool), catalogService, promotionService)
	orderentry.NewHTTP(orderService, accountService, cfg.PublicOrigin).Register(server.Router())
	if cfg.FlashSale.Enabled {
		redisOptions, err := redisclient.ParseURL(cfg.FlashSale.RedisURL)
		if err != nil {
			slog.Error("parse flash sale Redis configuration", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		redisConnection := redisclient.NewClient(redisOptions)
		defer redisConnection.Close()
		redisStore, err := flashredis.NewStore(redisConnection, cfg.FlashSale.RedisKeyPrefix, cfg.FlashSale.RedisRecoveryGrace)
		if err != nil {
			slog.Error("initialize flash sale Redis", "error", err)
			os.Exit(1)
		}
		if err := redisStore.Ping(ctx); err != nil {
			slog.Error("ping flash sale Redis", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		if err := redisStore.LoadScripts(ctx); err != nil {
			slog.Error("load flash sale Redis scripts", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		flashStore := flashpg.NewStore(pool)
		mqConfig := flashmq.Config{
			NameServers: cfg.FlashSale.RocketMQNameServers, AccessKey: cfg.FlashSale.RocketMQAccessKey, SecretKey: cfg.FlashSale.RocketMQSecretKey,
			Topic: cfg.FlashSale.RocketMQTopic, ProducerGroup: cfg.FlashSale.RocketMQProducer, ConsumerGroup: cfg.FlashSale.RocketMQConsumer,
			SendTimeout: cfg.FlashSale.RocketMQSendTimeout, ConsumeTimeout: cfg.FlashSale.RocketMQConsumeTimeout,
			ConsumerConcurrency: cfg.FlashSale.ConsumerConcurrency, RetryLimit: int32(cfg.FlashSale.RetryLimit),
		}
		producer, err := flashmq.NewProducer(mqConfig, redisStore, redisStore)
		if err != nil {
			slog.Error("configure flash sale producer", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		if err := producer.Start(); err != nil {
			slog.Error("start flash sale producer", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		defer producer.Shutdown()
		flashService := flashsale.NewService(flashStore, catalogService, producer, flashorder.NewService(orderService)).WithActivityCache(redisStore)
		consumer, err := flashmq.NewConsumer(mqConfig, flashService, redisStore)
		if err != nil {
			slog.Error("configure flash sale consumer", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		if err := consumer.Start(); err != nil {
			slog.Error("start flash sale consumer", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		defer consumer.Shutdown()
		rocketReadiness, err := flashmq.NewReadinessProbe(mqConfig)
		if err != nil {
			slog.Error("configure RocketMQ readiness", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		flashentry.NewHTTP(flashService, accountService, cfg.PublicOrigin).Register(server.Router())
		background := flashentry.StartBackground(context.Background(), cfg.FlashSale.RecoveryInterval,
			flashentry.RecoveryRunner(flashsale.NewRecoveryDispatcher(flashStore, redisStore, producer, cfg.FlashSale.RecoveryBatch, cfg.FlashSale.RecoveryStale, cfg.FlashSale.RecoveryLease)),
			cfg.FlashSale.ExpiryInterval,
			flashentry.ExpiryRunner(flashsale.NewExpiryReaper(flashStore, cfg.FlashSale.ExpiryBatch)),
			cfg.FlashSale.ReleaseInterval, flashentry.ReleaseRunner(flashsale.NewReleaseWorker(flashStore, redisStore, cfg.FlashSale.ReleaseBatch, cfg.FlashSale.ReleaseLease)))
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			_ = background.Shutdown(shutdownCtx)
		}()
		checks := []func(context.Context) error{pool.Ping, redisStore.Ping, rocketReadiness.Ready}
		if advancedKnowledge != nil {
			checks = append(checks, advancedKnowledge.Ready)
		}
		server.RegisterReadinessChecks(checks...)
	} else if advancedKnowledge != nil {
		server.RegisterReadinessChecks(pool.Ping, advancedKnowledge.Ready)
	} else {
		server.RegisterReadiness(pool.Ping)
	}
	slog.Info("xiaolanhe started", "address", cfg.Address, "model", cfg.AIModel)
	server.Spin()
}
