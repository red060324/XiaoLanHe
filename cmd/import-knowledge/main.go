package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/importer"
	importlightrag "github.com/red060324/XiaoLanHe/internal/knowledge/importer/lightrag"
	legacypostgres "github.com/red060324/XiaoLanHe/internal/knowledge/importer/postgres"
	knowledgelightrag "github.com/red060324/XiaoLanHe/internal/knowledge/repository/lightrag"
)

type options struct {
	execute, reconcile                               bool
	limit                                            int
	pollInterval, documentTimeout                    time.Duration
	manifestPath, checkpointPath, reconciliationPath string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fail(err.Error())
	}
}

func run(parent context.Context, arguments []string, output *os.File) error {
	parsed, err := parseFlags(arguments)
	if err != nil {
		return err
	}
	legacyURL := strings.TrimSpace(os.Getenv("XLH_LEGACY_POSTGRES_URL"))
	if legacyURL == "" {
		return errors.New("XLH_LEGACY_POSTGRES_URL is required")
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	source, err := legacypostgres.Open(ctx, legacyURL)
	if err != nil {
		return fmt.Errorf("open frozen legacy PostgreSQL snapshot: %w", err)
	}
	defer source.Close(context.Background())

	if !parsed.execute && !parsed.reconcile {
		manifest, buildErr := importer.BuildManifest(ctx, source, parsed.limit, time.Now().UTC())
		if buildErr != nil {
			return buildErr
		}
		if err := importer.WriteManifest(parsed.manifestPath, manifest); err != nil {
			return fmt.Errorf("write immutable manifest: %w", err)
		}
		checkpoint, err := importer.NewCheckpoint(manifest, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := importer.SaveCheckpoint(parsed.checkpointPath, checkpoint, manifest); err != nil {
			return fmt.Errorf("write initial checkpoint: %w", err)
		}
		return json.NewEncoder(output).Encode(manifest)
	}

	manifest, err := importer.LoadManifest(parsed.manifestPath)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	if err := importer.VerifySource(ctx, source, manifest, parsed.limit); err != nil {
		return fmt.Errorf("verify frozen legacy source: %w", err)
	}
	checkpoint, err := importer.LoadOrCreateCheckpoint(parsed.checkpointPath, manifest, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}
	lightragConfig, err := loadLightRAGConfig()
	if err != nil {
		return err
	}
	client, err := knowledgelightrag.NewClient(lightragConfig)
	if err != nil {
		return fmt.Errorf("configure LightRAG: %w", err)
	}
	if _, err := client.Health(ctx); err != nil {
		return fmt.Errorf("LightRAG readiness check failed: %w", err)
	}
	if parsed.reconcile {
		verifier, err := importlightrag.NewClient(importlightrag.Config{BaseURL: lightragConfig.BaseURL, APIKey: lightragConfig.APIKey, Workspace: lightragConfig.Workspace, AllowInsecure: lightragConfig.AllowInsecure, Timeout: lightragConfig.Timeout})
		if err != nil {
			return fmt.Errorf("configure LightRAG verifier: %w", err)
		}
		report, reconcileErr := importer.Reconcile(ctx, manifest, checkpoint, verifier, 200, time.Now().UTC())
		if err := importer.WriteReconciliationReport(parsed.reconciliationPath, report); err != nil {
			return fmt.Errorf("write immutable reconciliation report: %w", err)
		}
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return fmt.Errorf("encode reconciliation report: %w", err)
		}
		return reconcileErr
	}
	runner, err := importer.New(source, client)
	if err != nil {
		return err
	}
	report, runErr := runner.Run(ctx, importer.Options{
		Execute: true, Limit: parsed.limit, PollInterval: parsed.pollInterval, PerDocumentTimeout: parsed.documentTimeout,
		Manifest: manifest, Checkpoint: checkpoint, SaveCheckpoint: func(value importer.Checkpoint) error {
			return importer.SaveCheckpoint(parsed.checkpointPath, value, manifest)
		},
	})
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return fmt.Errorf("encode import report: %w", err)
	}
	return runErr
}

func parseFlags(arguments []string) (options, error) {
	result := options{}
	flags := flag.NewFlagSet("import-knowledge", flag.ContinueOnError)
	flags.BoolVar(&result.execute, "execute", false, "submit the next checkpoint-bound manifest batch")
	flags.BoolVar(&result.reconcile, "reconcile", false, "verify the complete manifest against all LightRAG pages")
	flags.IntVar(&result.limit, "limit", 100, "source manifest page or import batch size (1-100)")
	flags.DurationVar(&result.pollInterval, "poll-interval", 2*time.Second, "LightRAG track polling interval")
	flags.DurationVar(&result.documentTimeout, "document-timeout", 2*time.Minute, "per-document create and indexing deadline")
	flags.StringVar(&result.manifestPath, "manifest", "legacy-knowledge-manifest.json", "immutable manifest path")
	flags.StringVar(&result.checkpointPath, "checkpoint", "legacy-knowledge-checkpoint.json", "atomic checkpoint path")
	flags.StringVar(&result.reconciliationPath, "reconciliation-report", "legacy-knowledge-reconciliation.json", "immutable reconciliation report path")
	if err := flags.Parse(arguments); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 || result.execute && result.reconcile || result.limit < 1 || result.limit > 100 || result.pollInterval <= 0 || result.documentTimeout <= 0 ||
		strings.TrimSpace(result.manifestPath) == "" || strings.TrimSpace(result.checkpointPath) == "" || strings.TrimSpace(result.reconciliationPath) == "" {
		return options{}, importer.ErrInvalidOptions
	}
	return result, nil
}

func loadLightRAGConfig() (knowledgelightrag.Config, error) {
	allowInsecure, err := strconv.ParseBool(env("XLH_LIGHTRAG_ALLOW_INSECURE", "false"))
	if err != nil {
		return knowledgelightrag.Config{}, errors.New("XLH_LIGHTRAG_ALLOW_INSECURE must be true or false")
	}
	config := knowledgelightrag.Config{
		BaseURL: strings.TrimRight(env("XLH_LIGHTRAG_BASE_URL", "https://127.0.0.1:9621"), "/"), APIKey: strings.TrimSpace(os.Getenv("XLH_LIGHTRAG_API_KEY")),
		Workspace: env("XLH_LIGHTRAG_WORKSPACE", "xiaolanhe_v1"), WorkingDirectory: env("XLH_LIGHTRAG_WORKING_DIR", "/app/data/rag_storage"),
		CoreVersion: "1.5.7", APIVersion: "0344", FencePath: strings.TrimSpace(os.Getenv("XLH_LIGHTRAG_REBUILD_FENCE_DIR")),
		FenceGeneration: strings.TrimSpace(os.Getenv("XLH_LIGHTRAG_DEPLOYMENT_GENERATION")), FenceContractSHA256: strings.TrimSpace(os.Getenv("XLH_LIGHTRAG_REBUILD_CONTRACT_SHA256")),
		AllowInsecure: allowInsecure, Timeout: 15 * time.Second,
	}
	parsed, parseErr := url.Parse(config.BaseURL)
	if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return knowledgelightrag.Config{}, errors.New("XLH_LIGHTRAG_BASE_URL must be an absolute http(s) URL without credentials, query or fragment")
	}
	if parsed.Scheme == "http" && !config.AllowInsecure {
		return knowledgelightrag.Config{}, errors.New("XLH_LIGHTRAG_BASE_URL must use https unless XLH_LIGHTRAG_ALLOW_INSECURE=true")
	}
	if config.APIKey == "" || config.FencePath == "" || config.FenceGeneration == "" || config.FenceContractSHA256 == "" {
		return knowledgelightrag.Config{}, errors.New("LightRAG API key, rebuild fence directory, deployment generation, and contract SHA-256 are required")
	}
	return config, nil
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
