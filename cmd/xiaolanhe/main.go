package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	redisclient "github.com/redis/go-redis/v9"

	accountentry "github.com/red060324/XiaoLanHe/internal/account/entry"
	accountmysql "github.com/red060324/XiaoLanHe/internal/account/repository/mysql"
	"github.com/red060324/XiaoLanHe/internal/account/repository/password"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	einoadapter "github.com/red060324/XiaoLanHe/internal/adapter/eino"
	mysqladapter "github.com/red060324/XiaoLanHe/internal/adapter/mysql"
	"github.com/red060324/XiaoLanHe/internal/adapter/websearch"
	assistantagent "github.com/red060324/XiaoLanHe/internal/assistant/agent/eino"
	assistantentity "github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistantentry "github.com/red060324/XiaoLanHe/internal/assistant/entry"
	assistantlightrag "github.com/red060324/XiaoLanHe/internal/assistant/repository/lightrag"
	assistantmysql "github.com/red060324/XiaoLanHe/internal/assistant/repository/mysql"
	assistantskill "github.com/red060324/XiaoLanHe/internal/assistant/skill"
	assistantuc "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	catalogentry "github.com/red060324/XiaoLanHe/internal/catalog/entry"
	catalogmysql "github.com/red060324/XiaoLanHe/internal/catalog/repository/mysql"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	communityentry "github.com/red060324/XiaoLanHe/internal/community/entry"
	communitymysql "github.com/red060324/XiaoLanHe/internal/community/repository/mysql"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	"github.com/red060324/XiaoLanHe/internal/config"
	"github.com/red060324/XiaoLanHe/internal/entry"
	flashentry "github.com/red060324/XiaoLanHe/internal/flashsale/entry"
	flashmysql "github.com/red060324/XiaoLanHe/internal/flashsale/repository/mysql"
	flashorder "github.com/red060324/XiaoLanHe/internal/flashsale/repository/order"
	flashredis "github.com/red060324/XiaoLanHe/internal/flashsale/repository/redis"
	flashmq "github.com/red060324/XiaoLanHe/internal/flashsale/repository/rocketmq"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	knowledgeentry "github.com/red060324/XiaoLanHe/internal/knowledge/entry"
	knowledgelightrag "github.com/red060324/XiaoLanHe/internal/knowledge/repository/lightrag"
	knowledgeuc "github.com/red060324/XiaoLanHe/internal/knowledge/usecase"
	orderentry "github.com/red060324/XiaoLanHe/internal/order/entry"
	ordermysql "github.com/red060324/XiaoLanHe/internal/order/repository/mysql"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
	promotionentry "github.com/red060324/XiaoLanHe/internal/promotion/entry"
	promotionmysql "github.com/red060324/XiaoLanHe/internal/promotion/repository/mysql"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
	"github.com/red060324/XiaoLanHe/internal/usecase"
	mysqlmigrations "github.com/red060324/XiaoLanHe/migrations/mysql"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}

	var db *sql.DB
	err = runStartupPhase(databaseStartupTimeout, func(ctx context.Context) error {
		var openErr error
		db, openErr = mysqladapter.Open(ctx, cfg.DatabaseURL, mysqladapter.Options{
			MaxOpenConnections:    cfg.Database.MaxOpenConnections,
			MaxIdleConnections:    cfg.Database.MaxIdleConnections,
			ConnectionMaxLifetime: cfg.Database.ConnectionMaxLifetime,
			ConnectionMaxIdleTime: cfg.Database.ConnectionMaxIdleTime,
			AllowInsecure:         cfg.Database.AllowInsecure,
			TLS: mysqladapter.TLSOptions{
				CAFile:          cfg.Database.TLSCAFile,
				ServerName:      cfg.Database.TLSServerName,
				CertificateFile: cfg.Database.TLSCertificateFile,
				KeyFile:         cfg.Database.TLSKeyFile,
			},
		})
		return openErr
	})
	if err != nil {
		slog.Error("connect database", "outcome", "dependency_unavailable")
		os.Exit(1)
	}
	defer db.Close()
	if err := runStartupPhase(migrationStartupTimeout(cfg.Database.MigrationLockTimeout), func(ctx context.Context) error {
		return mysqladapter.Migrate(ctx, db, mysqlmigrations.Files, mysqladapter.MigrationOptions{LockTimeout: cfg.Database.MigrationLockTimeout})
	}); err != nil {
		slog.Error("initialize database", "outcome", "migration_failed")
		os.Exit(1)
	}

	temperature := float32(0.4)
	var providerChatModel *openai.ChatModel
	err = runStartupPhase(componentConstructionTimeout, func(ctx context.Context) error {
		var modelErr error
		providerChatModel, modelErr = openai.NewChatModel(ctx, &openai.ChatModelConfig{
			APIKey:      cfg.AIAPIKey,
			BaseURL:     cfg.AIBaseURL,
			Model:       cfg.AIModel,
			Timeout:     cfg.AITimeout,
			Temperature: &temperature,
			ExtraFields: map[string]any{"enable_thinking": false},
		})
		return modelErr
	})
	if err != nil {
		slog.Error("create chat model", "error", err)
		os.Exit(1)
	}
	chatModel := einoadapter.ObserveChatModel(providerChatModel, platformmetrics.Default())

	store := mysqladapter.NewConversationStore(db)
	assistantProfileStore := assistantmysql.NewProfileStore(db)
	client, clientErr := knowledgelightrag.NewClient(knowledgelightrag.Config{
		BaseURL: cfg.AdvancedAI.LightRAG.BaseURL, APIKey: cfg.AdvancedAI.LightRAG.APIKey,
		Workspace: cfg.AdvancedAI.LightRAG.Workspace, WorkingDirectory: cfg.AdvancedAI.LightRAG.WorkingDirectory,
		CoreVersion: cfg.AdvancedAI.LightRAG.CoreVersion, APIVersion: cfg.AdvancedAI.LightRAG.APIVersion,
		FencePath: cfg.AdvancedAI.LightRAG.FencePath, FenceGeneration: cfg.AdvancedAI.LightRAG.FenceGeneration,
		FenceContractSHA256: cfg.AdvancedAI.LightRAG.FenceContractSHA256, AllowInsecure: cfg.AdvancedAI.LightRAG.AllowInsecure,
		Timeout: cfg.AdvancedAI.LightRAG.Timeout,
	})
	if clientErr != nil {
		slog.Error("configure LightRAG", "outcome", "invalid_configuration")
		os.Exit(1)
	}
	knowledgeService := knowledgeuc.NewService(client)
	if readyErr := runStartupPhase(cfg.AdvancedAI.LightRAG.Timeout, knowledgeService.Ready); readyErr != nil {
		slog.Error("verify LightRAG", "outcome", "dependency_unavailable")
		os.Exit(1)
	}
	researchKnowledge := assistantlightrag.NewSearchAdapter(knowledgeService)
	search := usecase.NewWebSearch(websearch.NewSearXNG(cfg.SearchEnabled, cfg.SearchEndpoint, cfg.SearchTimeout))
	accountService := account.NewService(accountmysql.NewStore(db), password.Bcrypt{}, 7*24*time.Hour)
	catalogService := catalog.NewService(catalogmysql.NewStore(db))
	communityService := community.NewService(communitymysql.NewStore(db), catalogService)
	nodes := einoadapter.NewModelNodes(chatModel, cfg.AIModel, cfg.PlanningPrompt, cfg.DirectPrompt, cfg.SynthesisPrompt)
	var research *einoadapter.ResearchAgent
	err = runStartupPhase(componentConstructionTimeout, func(ctx context.Context) error {
		var agentErr error
		research, agentErr = einoadapter.NewResearchAgent(ctx, chatModel, cfg.ResearchPrompt, einoadapter.ResearchCapabilities{Knowledge: researchKnowledge, Catalog: catalogService, Forum: communityService, Web: search, WebEnabled: cfg.SearchEnabled}, einoadapter.ResearchLimits{TotalTimeout: cfg.ResearchTimeout, ToolTimeout: cfg.ResearchToolTimeout, MaxIterations: cfg.ResearchMaxIterations, MaxToolCalls: cfg.ResearchMaxToolCalls})
		return agentErr
	})
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
			assistantmysql.NewMemoryStore(db),
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
	server := entry.NewHTTPWithServices(cfg.Address, chatService, search, accountService)
	server.RegisterMetrics(cfg.MetricsToken, platformmetrics.Default())
	accountentry.NewHTTP(accountService, cfg.CookieSecure, cfg.PublicOrigin).Register(server.Router())
	assistantentry.NewProfileHTTP(assistantuc.NewProfileService(assistantProfileStore), accountService, cfg.PublicOrigin).Register(server.Router())
	knowledgeentry.NewHTTP(knowledgeService, accountService, cfg.PublicOrigin).Register(server.Router())
	catalogentry.NewHTTP(catalogService, accountService, cfg.PublicOrigin).Register(server.Router())
	communityentry.NewHTTP(communityService, accountService, cfg.PublicOrigin).Register(server.Router())
	promotionService := promotion.NewService(promotionmysql.NewStore(db))
	promotionentry.NewHTTP(promotionService, accountService, cfg.PublicOrigin).Register(server.Router())
	orderService := order.NewService(ordermysql.NewStore(db), catalogService, promotionService)
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
		if err := runStartupPhase(dependencyStartupTimeout, redisStore.Ping); err != nil {
			slog.Error("ping flash sale Redis", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		if err := runStartupPhase(dependencyStartupTimeout, redisStore.LoadScripts); err != nil {
			slog.Error("load flash sale Redis scripts", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		flashStore := flashmysql.NewStore(db)
		mqConfig := flashmq.Config{
			NameServers: cfg.FlashSale.RocketMQNameServers, AccessKey: cfg.FlashSale.RocketMQAccessKey, SecretKey: cfg.FlashSale.RocketMQSecretKey,
			Topic: cfg.FlashSale.RocketMQTopic, ProducerGroup: cfg.FlashSale.RocketMQProducer, ConsumerGroup: cfg.FlashSale.RocketMQConsumer,
			SendTimeout: cfg.FlashSale.RocketMQSendTimeout, ConsumeTimeout: cfg.FlashSale.RocketMQConsumeTimeout,
			ConsumerConcurrency: cfg.FlashSale.ConsumerConcurrency, RetryLimit: int32(cfg.FlashSale.RetryLimit),
		}
		rocketReadiness, err := flashmq.NewReadinessProbe(mqConfig)
		if err != nil {
			slog.Error("configure RocketMQ readiness", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		producer, err := flashmq.NewProducer(mqConfig, redisStore, redisStore)
		if err != nil {
			slog.Error("configure flash sale producer", "outcome", "invalid_configuration")
			os.Exit(1)
		}
		if err := startRocketMQClient(dependencyStartupTimeout, rocketReadiness.Ready, producer.Start, rocketMQStartDeadline("producer")); err != nil {
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
		if err := startRocketMQClient(dependencyStartupTimeout, rocketReadiness.Ready, consumer.Start, rocketMQStartDeadline("consumer")); err != nil {
			slog.Error("start flash sale consumer", "outcome", "dependency_unavailable")
			os.Exit(1)
		}
		defer consumer.Shutdown()
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
		checks := []func(context.Context) error{func(checkCtx context.Context) error { return mysqladapter.Ready(checkCtx, db) }, redisStore.Ping, rocketReadiness.Ready, knowledgeService.Ready}
		server.RegisterReadinessChecks(checks...)
	} else {
		server.RegisterReadinessChecks(func(checkCtx context.Context) error { return mysqladapter.Ready(checkCtx, db) }, knowledgeService.Ready)
	}
	slog.Info("xiaolanhe started", "address", cfg.Address, "model", cfg.AIModel)
	server.Spin()
}
