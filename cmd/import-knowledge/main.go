package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/adapter/postgres"
	"github.com/red060324/XiaoLanHe/internal/knowledge/importer"
	knowledgelightrag "github.com/red060324/XiaoLanHe/internal/knowledge/repository/lightrag"
)

func main() {
	var execute bool
	var afterID int64
	var limit int
	var pollInterval, documentTimeout time.Duration
	flag.BoolVar(&execute, "execute", false, "submit documents; without this flag the command is a dry run")
	flag.Int64Var(&afterID, "after-id", 0, "resume after this legacy knowledge_document ID")
	flag.IntVar(&limit, "limit", 20, "maximum documents for this bounded run (1-100)")
	flag.DurationVar(&pollInterval, "poll-interval", 2*time.Second, "LightRAG track polling interval")
	flag.DurationVar(&documentTimeout, "document-timeout", 2*time.Minute, "per-document create and indexing deadline")
	flag.Parse()

	databaseURL := strings.TrimSpace(os.Getenv("XLH_DATABASE_URL"))
	if databaseURL == "" {
		fail("XLH_DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fail("connect legacy database: " + err.Error())
	}
	defer pool.Close()

	var destination importer.Destination
	if execute {
		client, clientErr := knowledgelightrag.NewClient(knowledgelightrag.Config{
			BaseURL: strings.TrimSpace(os.Getenv("XLH_LIGHTRAG_BASE_URL")), APIKey: strings.TrimSpace(os.Getenv("XLH_LIGHTRAG_API_KEY")),
			Workspace: env("XLH_LIGHTRAG_WORKSPACE", "xiaolanhe_v1"), WorkingDirectory: env("XLH_LIGHTRAG_WORKING_DIR", "/app/data/rag_storage"),
			CoreVersion: "1.5.7", APIVersion: "0344", Timeout: 15 * time.Second,
		})
		if clientErr != nil {
			fail("configure LightRAG: " + clientErr.Error())
		}
		if _, readyErr := client.Health(ctx); readyErr != nil {
			fail("LightRAG readiness check failed")
		}
		destination = client
	}
	runner, err := importer.New(postgres.NewLegacyKnowledgeSource(pool), destination)
	if err != nil {
		fail(err.Error())
	}
	report, runErr := runner.Run(ctx, importer.Options{Execute: execute, AfterID: afterID, Limit: limit, PollInterval: pollInterval, PerDocumentTimeout: documentTimeout})
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fail("encode report: " + err.Error())
	}
	if runErr != nil {
		fail(runErr.Error())
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
