package lightrag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

const (
	maxResponseBytes        = 2 << 20
	maxUpstreamPages        = 20
	upstreamPageSize        = 200
	maxEvidenceItems        = 32
	maxFenceBytes           = 2 << 20
	fenceSchemaVersion      = 2
	lightragCommit          = "28ff1b05f2ac3f3e6fa14dd2cd33656579bd0c9c"
	milvusVersion           = "2.6.11"
	etcdVersion             = "3.5.25"
	minioVersion            = "RELEASE.2025-09-07T16-13-09Z"
	fenceSucceeded          = "succeeded"
	fenceNotApplicable      = "not_applicable"
	fenceIDCanonicalization = "lightrag-v1.5.7"
)

var (
	workspacePattern      = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	fenceTimePattern      = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z$`)
	errFenceUnreadable    = errors.New("fence file unreadable")
	fenceReportErrorCodes = map[string]struct{}{
		"abandoned_rebuilding":   {},
		"context_validation":     {},
		"controller_lock_busy":   {},
		"fixture_cleanup_failed": {},
		"fixture_failed":         {},
		"fixture_spawn_failed":   {},
		"fixture_timeout":        {},
		"io_failed":              {},
		"operation_cancelled":    {},
		"serving_lock_busy":      {},
		"unknown":                {},
		"validation_failed":      {},
	}
)

type Config struct {
	BaseURL, APIKey, Workspace, WorkingDirectory    string
	CoreVersion, APIVersion                         string
	FencePath, FenceGeneration, FenceContractSHA256 string
	AllowInsecure                                   bool
	Timeout                                         time.Duration
}

type Client struct {
	baseURL, apiKey, workspace, workingDirectory    string
	coreVersion, apiVersion                         string
	fencePath, fenceGeneration, fenceContractSHA256 string
	http                                            *http.Client
	metrics                                         *platformmetrics.Registry
}

func NewClient(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("LightRAG base URL must be an absolute http(s) URL without credentials, query or fragment")
	}
	if base.Scheme == "http" && !cfg.AllowInsecure {
		return nil, errors.New("LightRAG base URL must use https unless insecure transport is explicitly allowed")
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	workingDirectory := strings.TrimSpace(cfg.WorkingDirectory)
	fencePath := filepath.Clean(strings.TrimSpace(cfg.FencePath))
	if len(apiKey) < 32 || len(apiKey) > 512 || strings.ContainsAny(apiKey, "\r\n") || cfg.Workspace != "xiaolanhe_v1" || !workspacePattern.MatchString(cfg.Workspace) || workingDirectory != "/app/data/rag_storage" || !strings.HasPrefix(workingDirectory, "/") || path.Clean(workingDirectory) != workingDirectory || !filepath.IsAbs(fencePath) || !validFenceID(cfg.FenceGeneration) || !validSHA256(cfg.FenceContractSHA256) || cfg.Timeout <= 0 || cfg.CoreVersion != "1.5.7" || cfg.APIVersion != "0344" {
		return nil, errors.New("LightRAG API key, workspace, working directory and positive timeout are invalid")
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 20, MaxIdleConnsPerHost: 10, IdleConnTimeout: 60 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: cfg.Timeout, ExpectContinueTimeout: time.Second,
	}
	return &Client{
		baseURL: strings.TrimRight(base.String(), "/"), apiKey: apiKey, workspace: cfg.Workspace,
		workingDirectory: workingDirectory, coreVersion: cfg.CoreVersion, apiVersion: cfg.APIVersion,
		fencePath: fencePath, fenceGeneration: cfg.FenceGeneration, fenceContractSHA256: cfg.FenceContractSHA256,
		http:    &http.Client{Transport: transport, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		metrics: platformmetrics.Default(),
	}, nil
}

func (c *Client) Health(ctx context.Context) (result entity.Health, resultErr error) {
	registry := c.metricsRegistry()
	vectorExpected, serviceHealthy, generationMatch := false, false, false
	defer func() {
		registry.SetLightRAGHealth(resultErr == nil, result.PipelineActive, result.RecoveryRequired)
		registry.SetLightRAGBackend(vectorExpected, serviceHealthy, generationMatch)
	}()
	fence, err := c.verifyFenceObserved()
	generationMatch = fence.GenerationMatch
	registry.ObserveLightRAGFence(platformmetrics.LightRAGFenceObservation{
		State: fence.State, Operation: fence.Operation, Outcome: fence.Outcome, Reason: fence.Reason,
	})
	if fence.Rebuild != nil {
		registry.SetLightRAGRebuild(*fence.Rebuild)
	}
	slog.InfoContext(ctx, "lightrag rebuild fence observed",
		"event", "lightrag.fence", "state", fence.State, "operation", fence.Operation,
		"outcome", fence.Outcome, "reason", fence.Reason, "generation_match", fence.GenerationMatch)
	if err != nil {
		return entity.Health{}, fmt.Errorf("verify lightrag rebuild fence: %w", entity.ErrContract)
	}
	var authentication struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/auth/verify", nil, &authentication); err != nil {
		return entity.Health{}, fmt.Errorf("verify lightrag authentication: %w", err)
	}
	if authentication.Status != "ok" {
		return entity.Health{}, fmt.Errorf("verify lightrag authentication: %w", entity.ErrContract)
	}
	var response struct {
		Status           string `json:"status"`
		CoreVersion      string `json:"core_version"`
		APIVersion       string `json:"api_version"`
		WorkingDirectory string `json:"working_directory"`
		PipelineActive   *bool  `json:"pipeline_active"`
		ServerMode       string `json:"server_mode"`
		Workers          int    `json:"workers"`
		Configuration    struct {
			Workspace        string `json:"workspace"`
			KVStorage        string `json:"kv_storage"`
			VectorStorage    string `json:"vector_storage"`
			GraphStorage     string `json:"graph_storage"`
			DocStatusStorage string `json:"doc_status_storage"`
		} `json:"configuration"`
	}
	if err := c.do(ctx, http.MethodGet, "/health", nil, &response); err != nil {
		return entity.Health{}, fmt.Errorf("read lightrag health: %w", err)
	}
	vectorExpected = response.Configuration.VectorStorage == "MilvusVectorDBStorage"
	var pipeline struct {
		Busy             *bool `json:"busy"`
		RecoveryRequired *bool `json:"recovery_required"`
	}
	if err := c.do(ctx, http.MethodGet, "/documents/pipeline_status", nil, &pipeline); err != nil {
		return entity.Health{}, fmt.Errorf("read lightrag pipeline health: %w", err)
	}
	if response.PipelineActive == nil || pipeline.Busy == nil || pipeline.RecoveryRequired == nil {
		return entity.Health{}, fmt.Errorf("read lightrag pipeline health: %w", entity.ErrContract)
	}
	health := entity.Health{
		CoreVersion: response.CoreVersion, APIVersion: response.APIVersion, Workspace: response.Configuration.Workspace,
		WorkingDirectory: path.Clean(response.WorkingDirectory), KVStorage: response.Configuration.KVStorage,
		VectorStorage: response.Configuration.VectorStorage, GraphStorage: response.Configuration.GraphStorage,
		DocStatusStorage: response.Configuration.DocStatusStorage, ServerMode: response.ServerMode, Workers: response.Workers,
		PipelineActive: *response.PipelineActive, RecoveryRequired: *pipeline.RecoveryRequired,
	}
	if response.Status != "healthy" || health.CoreVersion != c.coreVersion || health.APIVersion != c.apiVersion || health.Workspace != c.workspace || health.WorkingDirectory != c.workingDirectory ||
		health.KVStorage != "JsonKVStorage" || health.VectorStorage != "MilvusVectorDBStorage" || health.GraphStorage != "NetworkXStorage" || health.DocStatusStorage != "JsonDocStatusStorage" ||
		health.ServerMode != "gunicorn" || health.Workers != 2 || health.RecoveryRequired {
		return health, entity.ErrContract
	}
	serviceHealthy = true
	slog.InfoContext(ctx, "lightrag health verified", "event", "lightrag.health", "operation", "ready", "outcome", "healthy", "core_version", health.CoreVersion, "api_version", health.APIVersion, "server_mode", health.ServerMode, "workers", health.Workers, "kv_storage", health.KVStorage, "vector_storage", health.VectorStorage, "graph_storage", health.GraphStorage, "doc_status_storage", health.DocStatusStorage, "pipeline_active", health.PipelineActive, "pipeline_busy", *pipeline.Busy, "recovery_required", health.RecoveryRequired)
	return health, nil
}

func (c *Client) metricsRegistry() *platformmetrics.Registry {
	if c.metrics != nil {
		return c.metrics
	}
	return platformmetrics.Default()
}

type fenceMarker struct {
	SchemaVersion  int     `json:"schema_version"`
	State          string  `json:"state"`
	Generation     string  `json:"generation"`
	AttemptID      string  `json:"attempt_id"`
	ContractSHA256 string  `json:"contract_sha256"`
	ReportPath     *string `json:"report_path"`
	ReportSHA256   *string `json:"report_sha256"`
	Operation      string  `json:"operation"`
	UpdatedAt      string  `json:"updated_at"`
	ReasonCode     string  `json:"reason_code"`
}

type fenceVerification struct {
	State, Operation, Outcome, Reason string
	GenerationMatch                   bool
	Rebuild                           *platformmetrics.LightRAGRebuildObservation
}

type fenceSoftware struct {
	LightRAGVersion     string `json:"lightrag_version"`
	LightRAGCommit      string `json:"lightrag_commit"`
	LightRAGImageDigest string `json:"lightrag_image_digest"`
	MilvusVersion       string `json:"milvus_version"`
	EtcdVersion         string `json:"etcd_version"`
	MinioVersion        string `json:"minio_version"`
}

type fenceEmbeddingContract struct {
	ProviderBinding   string  `json:"provider_binding"`
	EndpointSemantics string  `json:"endpoint_semantics"`
	Model             string  `json:"model"`
	Dimension         int     `json:"dimension"`
	SendDimension     bool    `json:"send_dimension"`
	Asymmetric        bool    `json:"asymmetric"`
	DocumentPrefix    *string `json:"document_prefix"`
	QueryPrefix       *string `json:"query_prefix"`
}

type fenceContract struct {
	ContractSHA256            string                 `json:"contract_sha256"`
	Workspace                 string                 `json:"workspace"`
	WorkingDirectoryIdentity  string                 `json:"working_directory_identity"`
	KVStorage                 string                 `json:"kv_storage"`
	VectorStorage             string                 `json:"vector_storage"`
	GraphStorage              string                 `json:"graph_storage"`
	DocStatusStorage          string                 `json:"doc_status_storage"`
	MilvusDatabase            string                 `json:"milvus_database"`
	EmbeddingContract         fenceEmbeddingContract `json:"embedding_contract"`
	IndexType                 string                 `json:"index_type"`
	MetricType                string                 `json:"metric_type"`
	IDCanonicalizationVersion string                 `json:"id_canonicalization_version"`
	FixtureSuiteVersion       string                 `json:"fixture_suite_version"`
	LegacyManifestSHA256      *string                `json:"legacy_manifest_sha256"`
}

type fenceWriterEvidence struct {
	Applicable                     bool    `json:"applicable"`
	Status                         string  `json:"status"`
	EvidenceType                   *string `json:"evidence_type"`
	EvidenceSHA256                 *string `json:"evidence_sha256"`
	IssuedAt                       *string `json:"issued_at"`
	ExpiresAt                      *string `json:"expires_at"`
	PipelineIdleObserved           bool    `json:"pipeline_idle_observed"`
	ServerReplicas                 int     `json:"server_replicas"`
	AutomaticRestartDisabled       bool    `json:"automatic_restart_disabled"`
	Observer                       *string `json:"observer"`
	UncontrolledWritersAttestation *string `json:"uncontrolled_writers_attestation"`
	BackupManifestSHA256           *string `json:"backup_manifest_sha256"`
}

type fenceBackupEvidence struct {
	Applicable           bool    `json:"applicable"`
	Status               string  `json:"status"`
	EvidenceType         *string `json:"evidence_type"`
	EvidenceSHA256       *string `json:"evidence_sha256"`
	ManifestSHA256       *string `json:"manifest_sha256"`
	SourceGeneration     *string `json:"source_generation"`
	ConfigurationSHA256  *string `json:"configuration_sha256"`
	WriterEvidenceSHA256 *string `json:"writer_evidence_sha256"`
	ComponentCount       int     `json:"component_count"`
}

type fenceSourceEvidence struct {
	Applicable              bool    `json:"applicable"`
	Status                  string  `json:"status"`
	BeforeSHA256            *string `json:"before_sha256"`
	AfterSHA256             *string `json:"after_sha256"`
	GraphNodes              int     `json:"graph_nodes"`
	GraphEdgesRaw           int     `json:"graph_edges_raw"`
	RelationshipsNormalized int     `json:"relationships_normalized"`
	TextChunks              int     `json:"text_chunks"`
}

type fenceError struct {
	Stage     string `json:"stage"`
	Target    string `json:"target"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

type fenceRebuildStats struct {
	Label         string       `json:"label"`
	SourceTotal   int          `json:"source_total"`
	Prepared      int          `json:"prepared"`
	Rebuilt       int          `json:"rebuilt"`
	Staged        int          `json:"staged"`
	Skipped       int          `json:"skipped"`
	Duplicates    int          `json:"duplicates"`
	Batches       int          `json:"batches"`
	FailedBatches int          `json:"failed_batches"`
	Errors        []fenceError `json:"errors"`
}

type fenceRebuildEvidence struct {
	Applicable    bool              `json:"applicable"`
	Status        string            `json:"status"`
	Entities      fenceRebuildStats `json:"entities"`
	Relationships fenceRebuildStats `json:"relationships"`
	Chunks        fenceRebuildStats `json:"chunks"`
}

type fenceTarget struct {
	Collection        *string `json:"collection"`
	Database          *string `json:"database"`
	DynamicFields     *bool   `json:"dynamic_fields"`
	SchemaSHA256      *string `json:"schema_sha256"`
	VectorFieldType   *string `json:"vector_field_type"`
	VectorDimension   int     `json:"vector_dimension"`
	PrimaryKeyField   *string `json:"primary_key_field"`
	IndexType         *string `json:"index_type"`
	MetricType        *string `json:"metric_type"`
	ExpectedCount     int     `json:"expected_count"`
	ActualCount       int     `json:"actual_count"`
	ExpectedIDsSHA256 *string `json:"expected_ids_sha256"`
	ActualIDsSHA256   *string `json:"actual_ids_sha256"`
}

type fenceTargetsEvidence struct {
	Applicable    bool        `json:"applicable"`
	Status        string      `json:"status"`
	Entities      fenceTarget `json:"entities"`
	Relationships fenceTarget `json:"relationships"`
	Chunks        fenceTarget `json:"chunks"`
}

type fenceTargetCounts struct {
	Entities      int `json:"entities"`
	Relationships int `json:"relationships"`
	Chunks        int `json:"chunks"`
}

type fenceDuplicatePolicy struct {
	Applicable     bool              `json:"applicable"`
	Status         string            `json:"status"`
	EvidenceSHA256 *string           `json:"evidence_sha256"`
	Reviewer       *string           `json:"reviewer"`
	ExpiresAt      *string           `json:"expires_at"`
	MaxDuplicates  fenceTargetCounts `json:"max_duplicates"`
}

type fenceOfficialConsistency struct {
	Applicable       bool   `json:"applicable"`
	Status           string `json:"status"`
	GraphEntities    int    `json:"graph_entities"`
	GraphRelations   int    `json:"graph_relations"`
	MissingEntities  int    `json:"missing_entities"`
	MissingRelations int    `json:"missing_relations"`
	SkippedNodes     int    `json:"skipped_nodes"`
	SkippedEdges     int    `json:"skipped_edges"`
}

type fenceLegacyImport struct {
	Applicable                 bool    `json:"applicable"`
	Status                     string  `json:"status"`
	Required                   bool    `json:"required"`
	ManifestSHA256             *string `json:"manifest_sha256"`
	ContinuousSuccessWatermark int     `json:"continuous_success_watermark"`
	SourceRows                 int     `json:"source_rows"`
	AcceptedRows               int     `json:"accepted_rows"`
	TerminalRows               int     `json:"terminal_rows"`
	Reconciled                 bool    `json:"reconciled"`
	RetirementEligible         bool    `json:"retirement_eligible"`
}

type fenceChecks struct {
	SourceUnchanged       bool `json:"source_unchanged"`
	StatsValid            bool `json:"stats_valid"`
	SchemaValid           bool `json:"schema_valid"`
	ExactIDSetsValid      bool `json:"exact_id_sets_valid"`
	GraphConsistencyValid bool `json:"graph_consistency_valid"`
	FixturesValid         bool `json:"fixtures_valid"`
	LegacyImportValid     bool `json:"legacy_import_valid"`
	CleanupValid          bool `json:"cleanup_valid"`
}

type fenceFixtures struct {
	Applicable    bool    `json:"applicable"`
	Status        string  `json:"status"`
	SuiteVersion  *string `json:"suite_version"`
	Passed        int     `json:"passed"`
	Failed        int     `json:"failed"`
	ResultsSHA256 *string `json:"results_sha256"`
}

type fenceCleanup struct {
	Applicable        bool   `json:"applicable"`
	Status            string `json:"status"`
	WorkerFinalized   bool   `json:"worker_finalized"`
	VerifierFinalized bool   `json:"verifier_finalized"`
	ReportReread      bool   `json:"report_reread"`
}

type fenceReport struct {
	SchemaVersion       int                      `json:"schema_version"`
	AttemptID           string                   `json:"attempt_id"`
	Generation          string                   `json:"generation"`
	Operation           string                   `json:"operation"`
	State               string                   `json:"state"`
	ReasonCode          string                   `json:"reason_code"`
	StartedAt           string                   `json:"started_at"`
	FinishedAt          string                   `json:"finished_at"`
	Software            fenceSoftware            `json:"software"`
	Contract            fenceContract            `json:"contract"`
	WriterFence         fenceWriterEvidence      `json:"writer_fence"`
	Backup              fenceBackupEvidence      `json:"backup"`
	Source              fenceSourceEvidence      `json:"source"`
	Rebuild             fenceRebuildEvidence     `json:"rebuild"`
	Targets             fenceTargetsEvidence     `json:"targets"`
	DuplicatePolicy     fenceDuplicatePolicy     `json:"duplicate_policy"`
	OfficialConsistency fenceOfficialConsistency `json:"official_consistency"`
	LegacyImport        fenceLegacyImport        `json:"legacy_import"`
	Checks              fenceChecks              `json:"checks"`
	Fixtures            fenceFixtures            `json:"fixtures"`
	Cleanup             fenceCleanup             `json:"cleanup"`
	Errors              []fenceError             `json:"errors"`
}

func (c *Client) verifyFence() error {
	_, err := c.verifyFenceObserved()
	return err
}

func (c *Client) verifyFenceObserved() (fenceVerification, error) {
	var current fenceMarker
	if _, _, err := readCanonicalJSON(filepath.Join(c.fencePath, "current.json"), &current); err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return rejectedFence("absent", "unknown", "marker_missing", false), entity.ErrContract
		case errors.Is(err, errFenceUnreadable):
			return failedFence("invalid", "unknown", "marker_unreadable", false), entity.ErrContract
		default:
			return rejectedFence("invalid", "unknown", "marker_invalid", false), entity.ErrContract
		}
	}
	operation := observedFenceOperation(current.Operation)
	updatedAt, markerOK := validObservedFenceMarker(current)
	if !markerOK {
		return rejectedFence("invalid", operation, "marker_invalid", false), entity.ErrContract
	}
	generationMatch := current.Generation == c.fenceGeneration
	if current.State != "verified" && current.State != "failed" {
		return rejectedFence(current.State, operation, current.ReasonCode, generationMatch), entity.ErrContract
	}
	if current.State == "verified" && !generationMatch {
		return rejectedFence("verified", operation, "generation_mismatch", false), entity.ErrContract
	}
	if current.State == "verified" && current.ContractSHA256 != c.fenceContractSHA256 {
		return rejectedFence("verified", operation, "contract_mismatch", true), entity.ErrContract
	}
	var report fenceReport
	_, canonicalReport, err := readCanonicalJSON(filepath.Join(c.fencePath, filepath.FromSlash(*current.ReportPath)), &report)
	if err != nil {
		if errors.Is(err, errFenceUnreadable) {
			return failedFence("invalid", operation, "report_unreadable", generationMatch), entity.ErrContract
		}
		if errors.Is(err, os.ErrNotExist) {
			return rejectedFence("invalid", operation, "report_unreadable", generationMatch), entity.ErrContract
		}
		return rejectedFence("invalid", operation, "report_invalid", generationMatch), entity.ErrContract
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(canonicalReport)) != *current.ReportSHA256 {
		return rejectedFence("invalid", operation, "report_digest_mismatch", generationMatch), entity.ErrContract
	}
	finishedAt, finishedOK := parseFenceTime(report.FinishedAt)
	if !finishedOK || updatedAt.Before(finishedAt) {
		return rejectedFence("invalid", operation, "report_invalid", generationMatch), entity.ErrContract
	}
	if report.AttemptID != current.AttemptID || report.Generation != current.Generation || report.Operation != current.Operation || report.State != current.State || report.ReasonCode != current.ReasonCode || report.Contract.ContractSHA256 != current.ContractSHA256 {
		return rejectedFence("invalid", operation, "report_identity_mismatch", generationMatch), entity.ErrContract
	}
	if current.State == "failed" {
		if !c.validFailedFenceReport(report) {
			return rejectedFence("invalid", operation, "report_invalid", generationMatch), entity.ErrContract
		}
		return failedFence("failed", operation, current.ReasonCode, generationMatch), entity.ErrContract
	}
	if !c.validVerifiedFenceReport(report) {
		return rejectedFence("invalid", operation, "report_invalid", true), entity.ErrContract
	}
	startedAt, _ := parseFenceTime(report.StartedAt)
	return fenceVerification{
		State: "verified", Operation: operation, Outcome: "verified", Reason: "verified", GenerationMatch: true,
		Rebuild: &platformmetrics.LightRAGRebuildObservation{
			Operation: operation, Outcome: "verified", Reason: "verified", Duration: finishedAt.Sub(startedAt),
			Targets: []platformmetrics.LightRAGRebuildTarget{
				fenceRebuildTarget("entities", report.Rebuild.Entities),
				fenceRebuildTarget("relationships", report.Rebuild.Relationships),
				fenceRebuildTarget("chunks", report.Rebuild.Chunks),
			},
		},
	}, nil
}

func validObservedFenceMarker(value fenceMarker) (time.Time, bool) {
	updatedAt, updatedOK := parseFenceTime(value.UpdatedAt)
	if value.SchemaVersion != fenceSchemaVersion || !validSerializedFenceState(value.State) || !validFenceOperation(value.Operation) || !validFenceID(value.AttemptID) || !validFenceID(value.Generation) || !validSHA256(value.ContractSHA256) || !updatedOK || !validFenceReason(value.State, value.ReasonCode) {
		return time.Time{}, false
	}
	terminal := value.State == "verified" || value.State == "failed"
	if !terminal {
		return updatedAt, value.ReportPath == nil && value.ReportSHA256 == nil
	}
	if value.ReportPath == nil || value.ReportSHA256 == nil || !validSHA256(*value.ReportSHA256) {
		return time.Time{}, false
	}
	return updatedAt, *value.ReportPath == "reports/"+value.AttemptID+".json"
}

func validSerializedFenceState(value string) bool {
	switch value {
	case "stale", "rebuilding", "failed", "verified":
		return true
	default:
		return false
	}
}

func validFenceReason(state, reason string) bool {
	switch state {
	case "stale":
		return reason == "restore_pending_verification" || reason == "contract_changed" || reason == "generation_changed" || reason == "legacy_manifest_changed" || reason == "invalidated"
	case "rebuilding":
		return reason == "in_progress"
	case "failed":
		return reason == "operation_failed" || reason == "abandoned_rebuilding"
	case "verified":
		return reason == "verified"
	default:
		return false
	}
}

func observedFenceOperation(value string) string {
	if validFenceOperation(value) {
		return value
	}
	return "unknown"
}

func rejectedFence(state, operation, reason string, generationMatch bool) fenceVerification {
	return fenceVerification{State: state, Operation: operation, Outcome: "rejected", Reason: reason, GenerationMatch: generationMatch}
}

func failedFence(state, operation, reason string, generationMatch bool) fenceVerification {
	return fenceVerification{State: state, Operation: operation, Outcome: "error", Reason: reason, GenerationMatch: generationMatch}
}

func fenceRebuildTarget(target string, stats fenceRebuildStats) platformmetrics.LightRAGRebuildTarget {
	return platformmetrics.LightRAGRebuildTarget{
		Target: target, SourceTotal: stats.SourceTotal, Prepared: stats.Prepared, Rebuilt: stats.Rebuilt,
		Skipped: stats.Skipped, Duplicates: stats.Duplicates, FailedBatches: stats.FailedBatches,
	}
}

func (c *Client) validVerifiedFenceReport(report fenceReport) bool {
	startedAt, startedOK := parseFenceTime(report.StartedAt)
	finishedAt, finishedOK := parseFenceTime(report.FinishedAt)
	if report.SchemaVersion != fenceSchemaVersion || report.State != "verified" || report.ReasonCode != "verified" || !validFenceOperation(report.Operation) || !validFenceID(report.AttemptID) || !validFenceID(report.Generation) || report.Generation != c.fenceGeneration || !startedOK || !finishedOK || finishedAt.Before(startedAt) || len(report.Errors) != 0 {
		return false
	}
	if !c.validFenceSoftware(report.Software) || !c.validFenceContract(report.Contract, report.Software) || !validFenceWriter(report.WriterFence, startedAt, finishedAt) || !validFenceBackup(report.Backup, report.WriterFence, report.Operation, report.Generation, report.Contract.ContractSHA256) || !validFenceSource(report.Source) || report.Operation == "bootstrap_empty" && !emptyFenceSource(report.Source) {
		return false
	}
	rebuildOK, duplicates := validFenceRebuild(report.Rebuild, report.Operation)
	if !rebuildOK || report.Operation == "migration_rebuild" && !fenceRebuildMatchesSource(report.Rebuild, report.Source) || !c.validFenceTargets(report.Targets, report.Rebuild, report.Operation) || !validFenceDuplicatePolicy(report.DuplicatePolicy, duplicates, finishedAt) || !validFenceConsistency(report.OfficialConsistency, report.Source) || !fenceTargetsMatchSource(report.Targets, report.OfficialConsistency, report.Source) {
		return false
	}
	if !validFenceLegacy(report.LegacyImport, report.Contract.LegacyManifestSHA256) || !validFenceFixtures(report.Fixtures, report.Contract.FixtureSuiteVersion, report.Operation) || !validFenceCleanup(report.Cleanup) {
		return false
	}
	checks := report.Checks
	return checks.SourceUnchanged && checks.StatsValid && checks.SchemaValid && checks.ExactIDSetsValid && checks.GraphConsistencyValid && checks.FixturesValid && checks.LegacyImportValid && checks.CleanupValid
}

func (c *Client) validFailedFenceReport(report fenceReport) bool {
	startedAt, startedOK := parseFenceTime(report.StartedAt)
	finishedAt, finishedOK := parseFenceTime(report.FinishedAt)
	if report.SchemaVersion != fenceSchemaVersion || report.State != "failed" || !validFenceReason(report.State, report.ReasonCode) || !validFenceOperation(report.Operation) || !validFenceID(report.AttemptID) || !validFenceID(report.Generation) || !startedOK || !finishedOK || finishedAt.Before(startedAt) || len(report.Errors) == 0 || !validFenceErrors(report.Errors) {
		return false
	}
	if !c.validFenceSoftware(report.Software) || !c.validFenceReportContract(report.Contract, report.Software) || !validFailedFenceWriter(report.WriterFence) || !validFailedFenceBackup(report.Backup) || !validFailedFenceSource(report.Source) || !validFailedFenceRebuild(report.Rebuild) || !validFailedFenceTargets(report.Targets) || !validFailedFenceDuplicatePolicy(report.DuplicatePolicy) || !validFailedFenceConsistency(report.OfficialConsistency) || !validFailedFenceLegacy(report.LegacyImport) || !validFailedFenceFixtures(report.Fixtures) || !validFailedFenceCleanup(report.Cleanup) {
		return false
	}
	checks := report.Checks
	if checks.SourceUnchanged || checks.StatsValid || checks.SchemaValid || checks.ExactIDSetsValid || checks.GraphConsistencyValid || checks.FixturesValid || checks.LegacyImportValid || checks.CleanupValid {
		return false
	}
	if report.ReasonCode != "abandoned_rebuilding" {
		return true
	}
	wantError := fenceError{Stage: "controller", Target: "all", Code: "abandoned_rebuilding", Retryable: true}
	if len(report.Errors) != 1 || report.Errors[0] != wantError {
		return false
	}
	return statusesNotExecuted(
		report.WriterFence.Status, report.Backup.Status, report.Source.Status, report.Rebuild.Status, report.Targets.Status,
		report.DuplicatePolicy.Status, report.OfficialConsistency.Status, report.LegacyImport.Status, report.Fixtures.Status, report.Cleanup.Status,
	)
}

func (c *Client) validFenceSoftware(value fenceSoftware) bool {
	return value.LightRAGVersion == c.coreVersion && value.LightRAGCommit == lightragCommit && validSHA256(value.LightRAGImageDigest) && value.MilvusVersion == milvusVersion && value.EtcdVersion == etcdVersion && value.MinioVersion == minioVersion
}

func (c *Client) validFenceContract(value fenceContract, software fenceSoftware) bool {
	return value.ContractSHA256 == c.fenceContractSHA256 && c.validFenceReportContract(value, software)
}

func (c *Client) validFenceReportContract(value fenceContract, software fenceSoftware) bool {
	embedding := value.EmbeddingContract
	return fenceContractDigest(software, value) == value.ContractSHA256 && value.Workspace == c.workspace && validFenceID(value.WorkingDirectoryIdentity) && value.KVStorage == "JsonKVStorage" && value.VectorStorage == "MilvusVectorDBStorage" && value.GraphStorage == "NetworkXStorage" && value.DocStatusStorage == "JsonDocStatusStorage" && value.MilvusDatabase == "lightrag" && validFenceID(embedding.ProviderBinding) && validFenceID(embedding.EndpointSemantics) && embedding.Model == "text-embedding-v4" && embedding.Dimension == 1024 && !embedding.SendDimension && !embedding.Asymmetric && embedding.DocumentPrefix == nil && embedding.QueryPrefix == nil && value.IndexType == "AUTOINDEX" && value.MetricType == "COSINE" && value.IDCanonicalizationVersion == fenceIDCanonicalization && validFenceID(value.FixtureSuiteVersion) && validOptionalSHA256(value.LegacyManifestSHA256)
}

func fenceContractDigest(software fenceSoftware, value fenceContract) string {
	embedding := value.EmbeddingContract
	contract := map[string]any{
		"schema_version": fenceSchemaVersion, "lightrag_version": software.LightRAGVersion, "lightrag_commit": software.LightRAGCommit, "lightrag_image_digest": software.LightRAGImageDigest,
		"milvus_version": software.MilvusVersion, "milvus_database": value.MilvusDatabase, "workspace": value.Workspace, "working_directory_identity": value.WorkingDirectoryIdentity,
		"storage":    map[string]any{"kv": value.KVStorage, "vector": value.VectorStorage, "graph": value.GraphStorage, "doc_status": value.DocStatusStorage},
		"embedding":  map[string]any{"binding": embedding.ProviderBinding, "endpoint_profile": embedding.EndpointSemantics, "model": embedding.Model, "dimension": embedding.Dimension, "send_dimension": fmt.Sprintf("%t", embedding.SendDimension), "asymmetric": fmt.Sprintf("%t", embedding.Asymmetric), "document_prefix": embedding.DocumentPrefix, "query_prefix": embedding.QueryPrefix},
		"index_type": value.IndexType, "metric_type": value.MetricType, "id_canonicalization_version": value.IDCanonicalizationVersion, "fixture_suite_version": value.FixtureSuiteVersion, "legacy_manifest_sha256": value.LegacyManifestSHA256,
	}
	canonical, err := canonicalJSONBytes(contract)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
}

func validFenceWriter(value fenceWriterEvidence, startedAt, finishedAt time.Time) bool {
	issuedAt, issuedOK := parseOptionalFenceTime(value.IssuedAt)
	expiresAt, expiresOK := parseOptionalFenceTime(value.ExpiresAt)
	return validFenceSection(value.Applicable, value.Status, true) && stringPointerEquals(value.EvidenceType, "xlh.lightrag_writer_fence.v2") && validSHA256Pointer(value.EvidenceSHA256) && issuedOK && expiresOK && !issuedAt.After(startedAt) && expiresAt.After(finishedAt) && value.PipelineIdleObserved && value.ServerReplicas == 0 && value.AutomaticRestartDisabled && value.Observer != nil && validFenceID(*value.Observer) && stringPointerEquals(value.UncontrolledWritersAttestation, "no_uncontrolled_writers") && validOptionalSHA256(value.BackupManifestSHA256)
}

func validFenceBackup(value fenceBackupEvidence, writer fenceWriterEvidence, operation, generation, contractHash string) bool {
	wantApplicable := operation == "migration_rebuild" || operation == "restore_verify"
	if !validFenceSection(value.Applicable, value.Status, wantApplicable) {
		return false
	}
	if !wantApplicable {
		return stringPointerEquals(value.EvidenceType, "xlh.lightrag_backup.v2") && value.EvidenceSHA256 == nil && value.ManifestSHA256 == nil && value.SourceGeneration == nil && value.ConfigurationSHA256 == nil && value.WriterEvidenceSHA256 == nil && value.ComponentCount == 0 && writer.BackupManifestSHA256 == nil
	}
	if !stringPointerEquals(value.EvidenceType, "xlh.lightrag_backup.v2") || !validSHA256Pointer(value.EvidenceSHA256) || !validSHA256Pointer(value.ManifestSHA256) || value.SourceGeneration == nil || !validFenceID(*value.SourceGeneration) || !stringPointerEquals(value.ConfigurationSHA256, contractHash) || !validSHA256Pointer(value.WriterEvidenceSHA256) || !sameStringPointers(writer.BackupManifestSHA256, value.ManifestSHA256) || value.ComponentCount != 4 {
		return false
	}
	return operation != "restore_verify" || *value.SourceGeneration != generation
}

func validFenceSource(value fenceSourceEvidence) bool {
	return validFenceSection(value.Applicable, value.Status, true) && validSHA256Pointer(value.BeforeSHA256) && sameStringPointers(value.BeforeSHA256, value.AfterSHA256) && nonnegative(value.GraphNodes, value.GraphEdgesRaw, value.RelationshipsNormalized, value.TextChunks) && value.RelationshipsNormalized <= value.GraphEdgesRaw
}

func emptyFenceSource(value fenceSourceEvidence) bool {
	return value.GraphNodes == 0 && value.GraphEdgesRaw == 0 && value.RelationshipsNormalized == 0 && value.TextChunks == 0
}

func validFenceRebuild(value fenceRebuildEvidence, operation string) (bool, fenceTargetCounts) {
	wantApplicable := operation == "migration_rebuild"
	if !validFenceSection(value.Applicable, value.Status, wantApplicable) {
		return false, fenceTargetCounts{}
	}
	stats := []struct {
		name  string
		value fenceRebuildStats
	}{
		{name: "entities", value: value.Entities},
		{name: "relationships", value: value.Relationships},
		{name: "chunks", value: value.Chunks},
	}
	duplicates := fenceTargetCounts{}
	for _, target := range stats {
		if wantApplicable {
			if !validFenceStats(target.name, target.value) {
				return false, fenceTargetCounts{}
			}
		} else if !emptyFenceStats(target.name, target.value) {
			return false, fenceTargetCounts{}
		}
	}
	duplicates.Entities = value.Entities.Duplicates
	duplicates.Relationships = value.Relationships.Duplicates
	duplicates.Chunks = value.Chunks.Duplicates
	return true, duplicates
}

func validFenceStats(name string, value fenceRebuildStats) bool {
	return value.Label == name && nonnegative(value.SourceTotal, value.Prepared, value.Rebuilt, value.Staged, value.Skipped, value.Duplicates, value.Batches, value.FailedBatches) && len(value.Errors) == 0 && value.FailedBatches == 0 && value.Staged == 0 && value.Skipped == 0 && value.Rebuilt == value.Prepared && value.Prepared+value.Skipped+value.Duplicates == value.SourceTotal
}

func emptyFenceStats(name string, value fenceRebuildStats) bool {
	return value.Label == name && value.SourceTotal == 0 && value.Prepared == 0 && value.Rebuilt == 0 && value.Staged == 0 && value.Skipped == 0 && value.Duplicates == 0 && value.Batches == 0 && value.FailedBatches == 0 && len(value.Errors) == 0
}

func fenceRebuildMatchesSource(rebuild fenceRebuildEvidence, source fenceSourceEvidence) bool {
	return rebuild.Entities.SourceTotal == source.GraphNodes && rebuild.Relationships.SourceTotal == source.GraphEdgesRaw && rebuild.Relationships.Prepared == source.RelationshipsNormalized && rebuild.Chunks.SourceTotal == source.TextChunks
}

func (c *Client) validFenceTargets(value fenceTargetsEvidence, rebuild fenceRebuildEvidence, operation string) bool {
	if !validFenceSection(value.Applicable, value.Status, true) {
		return false
	}
	targets := []struct {
		name       string
		statistics fenceRebuildStats
		value      fenceTarget
	}{
		{name: "entities", statistics: rebuild.Entities, value: value.Entities},
		{name: "relationships", statistics: rebuild.Relationships, value: value.Relationships},
		{name: "chunks", statistics: rebuild.Chunks, value: value.Chunks},
	}
	for _, target := range targets {
		collection := c.workspace + "_" + target.name + "_text_embedding_v4_1024d"
		evidence := target.value
		if evidence.Collection == nil || *evidence.Collection != collection || evidence.Database == nil || *evidence.Database != "lightrag" || evidence.DynamicFields == nil || !*evidence.DynamicFields || evidence.SchemaSHA256 == nil || *evidence.SchemaSHA256 != expectedFenceSchemaSHA256(target.name, collection) || evidence.VectorFieldType == nil || *evidence.VectorFieldType != "FLOAT_VECTOR" || evidence.VectorDimension != 1024 || evidence.PrimaryKeyField == nil || *evidence.PrimaryKeyField != "id" || evidence.IndexType == nil || *evidence.IndexType != "AUTOINDEX" || evidence.MetricType == nil || *evidence.MetricType != "COSINE" || !nonnegative(evidence.ExpectedCount, evidence.ActualCount) || evidence.ActualCount != evidence.ExpectedCount || !validSHA256Pointer(evidence.ExpectedIDsSHA256) || !sameStringPointers(evidence.ExpectedIDsSHA256, evidence.ActualIDsSHA256) {
			return false
		}
		if operation == "migration_rebuild" {
			if evidence.ExpectedCount != target.statistics.Prepared {
				return false
			}
		} else if operation == "bootstrap_empty" && evidence.ActualCount != 0 {
			return false
		}
	}
	return true
}

func expectedFenceSchemaSHA256(name, collection string) string {
	field := func(name, fieldType string, maxLength any, nullable, primary bool, dimension int) map[string]any {
		return map[string]any{"name": name, "type": fieldType, "max_length": maxLength, "nullable": nullable, "primary": primary, "dimension": dimension}
	}
	fields := map[string][]map[string]any{
		"entities": {
			field("content", "VARCHAR", 65535, true, false, 0), field("created_at", "INT64", nil, false, false, 0), field("entity_name", "VARCHAR", 512, true, false, 0), field("file_path", "VARCHAR", 32768, true, false, 0), field("id", "VARCHAR", 64, false, true, 0), field("source_id", "VARCHAR", 65535, true, false, 0), field("vector", "FLOAT_VECTOR", nil, false, false, 1024),
		},
		"relationships": {
			field("content", "VARCHAR", 65535, true, false, 0), field("created_at", "INT64", nil, false, false, 0), field("file_path", "VARCHAR", 32768, true, false, 0), field("id", "VARCHAR", 64, false, true, 0), field("source_id", "VARCHAR", 65535, true, false, 0), field("src_id", "VARCHAR", 512, true, false, 0), field("tgt_id", "VARCHAR", 512, true, false, 0), field("vector", "FLOAT_VECTOR", nil, false, false, 1024),
		},
		"chunks": {
			field("content", "VARCHAR", 65535, true, false, 0), field("created_at", "INT64", nil, false, false, 0), field("file_path", "VARCHAR", 32768, true, false, 0), field("full_doc_id", "VARCHAR", 64, true, false, 0), field("id", "VARCHAR", 64, false, true, 0), field("vector", "FLOAT_VECTOR", nil, false, false, 1024),
		},
	}
	canonical, err := canonicalJSONBytes(map[string]any{"database": "lightrag", "collection": collection, "enable_dynamic_field": true, "fields": fields[name]})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
}

func validFenceDuplicatePolicy(value fenceDuplicatePolicy, duplicates fenceTargetCounts, finishedAt time.Time) bool {
	wantApplicable := duplicates.Entities > 0 || duplicates.Relationships > 0 || duplicates.Chunks > 0
	if !validFenceSection(value.Applicable, value.Status, wantApplicable) || !nonnegative(value.MaxDuplicates.Entities, value.MaxDuplicates.Relationships, value.MaxDuplicates.Chunks) {
		return false
	}
	if !wantApplicable {
		return value.EvidenceSHA256 == nil && value.Reviewer == nil && value.ExpiresAt == nil && value.MaxDuplicates == (fenceTargetCounts{})
	}
	expiresAt, expiresOK := parseOptionalFenceTime(value.ExpiresAt)
	return validSHA256Pointer(value.EvidenceSHA256) && value.Reviewer != nil && validFenceID(*value.Reviewer) && expiresOK && expiresAt.After(finishedAt) && duplicates.Entities <= value.MaxDuplicates.Entities && duplicates.Relationships <= value.MaxDuplicates.Relationships && duplicates.Chunks <= value.MaxDuplicates.Chunks
}

func validFenceConsistency(value fenceOfficialConsistency, source fenceSourceEvidence) bool {
	return validFenceSection(value.Applicable, value.Status, true) && nonnegative(value.GraphEntities, value.GraphRelations, value.MissingEntities, value.MissingRelations, value.SkippedNodes, value.SkippedEdges) && value.GraphEntities <= source.GraphNodes && value.GraphRelations == source.RelationshipsNormalized && value.MissingEntities == 0 && value.MissingRelations == 0 && value.SkippedNodes == 0 && value.SkippedEdges == 0
}

func fenceTargetsMatchSource(targets fenceTargetsEvidence, official fenceOfficialConsistency, source fenceSourceEvidence) bool {
	return targets.Entities.ExpectedCount == official.GraphEntities && targets.Relationships.ExpectedCount == source.RelationshipsNormalized && targets.Chunks.ExpectedCount == source.TextChunks
}

func validFenceLegacy(value fenceLegacyImport, contractManifest *string) bool {
	if !validFenceSection(value.Applicable, value.Status, value.Required) || !nonnegative(value.SourceRows, value.AcceptedRows, value.TerminalRows) {
		return false
	}
	if !value.Required {
		return contractManifest == nil && value.ManifestSHA256 == nil && value.ContinuousSuccessWatermark == 0 && value.SourceRows == 0 && value.AcceptedRows == 0 && value.TerminalRows == 0 && !value.Reconciled && !value.RetirementEligible
	}
	return validSHA256Pointer(value.ManifestSHA256) && sameStringPointers(value.ManifestSHA256, contractManifest) && value.ContinuousSuccessWatermark == value.SourceRows && value.AcceptedRows == value.SourceRows && value.TerminalRows == value.SourceRows && value.Reconciled
}

func validFenceFixtures(value fenceFixtures, suiteVersion, operation string) bool {
	wantApplicable := operation != "bootstrap_empty"
	if !validFenceSection(value.Applicable, value.Status, wantApplicable) || !nonnegative(value.Passed, value.Failed) {
		return false
	}
	if !wantApplicable {
		return value.SuiteVersion == nil && value.Passed == 0 && value.Failed == 0 && value.ResultsSHA256 == nil
	}
	return value.SuiteVersion != nil && *value.SuiteVersion == suiteVersion && value.Passed > 0 && value.Failed == 0 && validSHA256Pointer(value.ResultsSHA256)
}

func validFenceCleanup(value fenceCleanup) bool {
	return validFenceSection(value.Applicable, value.Status, true) && value.WorkerFinalized && value.VerifierFinalized && value.ReportReread
}

func validFailedFenceWriter(value fenceWriterEvidence) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && stringPointerEquals(value.EvidenceType, "xlh.lightrag_writer_fence.v2") &&
		validOptionalSHA256(value.EvidenceSHA256) && validOptionalFenceTime(value.IssuedAt) && validOptionalFenceTime(value.ExpiresAt) &&
		nonnegative(value.ServerReplicas) && validOptionalFenceID(value.Observer) &&
		validOptionalSHA256(value.BackupManifestSHA256)
}

func validFailedFenceBackup(value fenceBackupEvidence) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && stringPointerEquals(value.EvidenceType, "xlh.lightrag_backup.v2") &&
		validOptionalSHA256(value.EvidenceSHA256) && validOptionalSHA256(value.ManifestSHA256) && validOptionalFenceID(value.SourceGeneration) &&
		validOptionalSHA256(value.ConfigurationSHA256) && validOptionalSHA256(value.WriterEvidenceSHA256) && nonnegative(value.ComponentCount)
}

func validFailedFenceSource(value fenceSourceEvidence) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && validOptionalSHA256(value.BeforeSHA256) && validOptionalSHA256(value.AfterSHA256) &&
		nonnegative(value.GraphNodes, value.GraphEdgesRaw, value.RelationshipsNormalized, value.TextChunks) && value.RelationshipsNormalized <= value.GraphEdgesRaw
}

func validFailedFenceRebuild(value fenceRebuildEvidence) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && validFailedFenceStats("entities", value.Entities) &&
		validFailedFenceStats("relationships", value.Relationships) && validFailedFenceStats("chunks", value.Chunks)
}

func validFailedFenceStats(name string, value fenceRebuildStats) bool {
	return value.Label == name && nonnegative(value.SourceTotal, value.Prepared, value.Rebuilt, value.Staged, value.Skipped, value.Duplicates, value.Batches, value.FailedBatches) && validFenceErrors(value.Errors)
}

func validFailedFenceTargets(value fenceTargetsEvidence) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && validFailedFenceTarget(value.Entities) && validFailedFenceTarget(value.Relationships) && validFailedFenceTarget(value.Chunks)
}

func validFailedFenceTarget(value fenceTarget) bool {
	return validOptionalSHA256(value.SchemaSHA256) && validOptionalSHA256(value.ExpectedIDsSHA256) && validOptionalSHA256(value.ActualIDsSHA256) &&
		nonnegative(value.VectorDimension, value.ExpectedCount, value.ActualCount)
}

func validFailedFenceDuplicatePolicy(value fenceDuplicatePolicy) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && validOptionalSHA256(value.EvidenceSHA256) && validOptionalFenceID(value.Reviewer) &&
		validOptionalFenceTime(value.ExpiresAt) && nonnegative(value.MaxDuplicates.Entities, value.MaxDuplicates.Relationships, value.MaxDuplicates.Chunks)
}

func validFailedFenceConsistency(value fenceOfficialConsistency) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && nonnegative(value.GraphEntities, value.GraphRelations, value.MissingEntities, value.MissingRelations, value.SkippedNodes, value.SkippedEdges)
}

func validFailedFenceLegacy(value fenceLegacyImport) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && validOptionalSHA256(value.ManifestSHA256) &&
		nonnegative(value.ContinuousSuccessWatermark, value.SourceRows, value.AcceptedRows, value.TerminalRows)
}

func validFailedFenceFixtures(value fenceFixtures) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && validOptionalFenceID(value.SuiteVersion) && validOptionalSHA256(value.ResultsSHA256) && nonnegative(value.Passed, value.Failed)
}

func validFailedFenceCleanup(value fenceCleanup) bool {
	return validFenceSectionShape(value.Applicable, value.Status) && value.Status != fenceSucceeded && !value.ReportReread
}

func validFenceSectionShape(applicable bool, status string) bool {
	switch status {
	case fenceSucceeded, "failed", "not_run":
		return applicable
	case fenceNotApplicable:
		return !applicable
	default:
		return false
	}
}

func statusesNotExecuted(statuses ...string) bool {
	for _, status := range statuses {
		if status != "not_run" && status != fenceNotApplicable {
			return false
		}
	}
	return true
}

func validFenceErrors(values []fenceError) bool {
	for _, value := range values {
		if !validBoundedFenceString(value.Stage) || !validBoundedFenceString(value.Target) {
			return false
		}
		if _, ok := fenceReportErrorCodes[value.Code]; !ok {
			return false
		}
	}
	return true
}

func validBoundedFenceString(value string) bool {
	length := utf8.RuneCountInString(value)
	if length == 0 || length > 64 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validFenceSection(applicable bool, status string, wantApplicable bool) bool {
	if applicable != wantApplicable {
		return false
	}
	if applicable {
		return status == fenceSucceeded
	}
	return status == fenceNotApplicable
}

func validFenceOperation(value string) bool {
	switch value {
	case "migration_rebuild", "bootstrap_empty", "restore_verify", "revalidate":
		return true
	default:
		return false
	}
}

func validFenceID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && !asciiAlphanumeric(character) {
			return false
		}
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func asciiAlphanumeric(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}

func stringPointerEquals(value *string, expected string) bool {
	return value != nil && *value == expected
}

func validOptionalSHA256(value *string) bool {
	return value == nil || validSHA256(*value)
}

func validOptionalFenceID(value *string) bool {
	return value == nil || validFenceID(*value)
}

func validOptionalFenceTime(value *string) bool {
	if value == nil {
		return true
	}
	_, ok := parseFenceTime(*value)
	return ok
}

func validSHA256Pointer(value *string) bool {
	return value != nil && validSHA256(*value)
}

func sameStringPointers(left, right *string) bool {
	return left != nil && right != nil && *left == *right
}

func nonnegative(values ...int) bool {
	for _, value := range values {
		if value < 0 {
			return false
		}
	}
	return true
}

func parseFenceTime(value string) (time.Time, bool) {
	if !fenceTimePattern.MatchString(value) {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func parseOptionalFenceTime(value *string) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	return parseFenceTime(*value)
}

func readCanonicalJSON(name string, target any) ([]byte, []byte, error) {
	data, err := readBoundedRegularFile(name)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.HasSuffix(data, []byte{'\n'}) {
		return nil, nil, entity.ErrContract
	}
	payload := data[:len(data)-1]
	if len(payload) == 0 {
		return nil, nil, entity.ErrContract
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, entity.ErrContract
	}
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer || targetType.Elem().Kind() != reflect.Struct || !validExactJSONShape(generic, targetType.Elem()) {
		return nil, nil, entity.ErrContract
	}
	canonical, err := canonicalJSONBytes(generic)
	if err != nil || !bytes.Equal(payload, canonical) {
		return nil, nil, entity.ErrContract
	}
	if err := json.Unmarshal(canonical, target); err != nil {
		return nil, nil, entity.ErrContract
	}
	return data, canonical, nil
}

func canonicalJSONBytes(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := output.Bytes()
	data = data[:len(data)-1]
	return unescapeJSONLineSeparators(data), nil
}

func unescapeJSONLineSeparators(data []byte) []byte {
	result := make([]byte, 0, len(data))
	for index := 0; index < len(data); index++ {
		if data[index] != '\\' || index+6 > len(data) || string(data[index:index+6]) != `\u2028` && string(data[index:index+6]) != `\u2029` {
			result = append(result, data[index])
			continue
		}
		precedingBackslashes := 0
		for previous := index - 1; previous >= 0 && data[previous] == '\\'; previous-- {
			precedingBackslashes++
		}
		if precedingBackslashes%2 != 0 {
			result = append(result, data[index])
			continue
		}
		if data[index+5] == '8' {
			result = append(result, "\u2028"...)
		} else {
			result = append(result, "\u2029"...)
		}
		index += 5
	}
	return result
}

func validExactJSONShape(value any, expected reflect.Type) bool {
	if expected.Kind() == reflect.Pointer {
		if value == nil {
			return true
		}
		return validExactJSONShape(value, expected.Elem())
	}
	if value == nil {
		return false
	}
	switch expected.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok || len(object) != expected.NumField() {
			return false
		}
		for index := 0; index < expected.NumField(); index++ {
			field := expected.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			fieldValue, exists := object[name]
			if name == "" || name == "-" || !exists || !validExactJSONShape(fieldValue, field.Type) {
				return false
			}
		}
		return true
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range items {
			if !validExactJSONShape(item, expected.Elem()) {
				return false
			}
		}
		return true
	case reflect.String:
		_, ok := value.(string)
		return ok
	case reflect.Bool:
		_, ok := value.(bool)
		return ok
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		integer, err := number.Int64()
		return err == nil && number.String() == strconv.FormatInt(integer, 10) && !reflect.New(expected).Elem().OverflowInt(integer)
	default:
		return false
	}
}

func readBoundedRegularFile(name string) ([]byte, error) {
	pathInfo, err := os.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, errFenceUnreadable
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, entity.ErrContract
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, errFenceUnreadable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errFenceUnreadable
	}
	if !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		return nil, entity.ErrContract
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFenceBytes+1))
	if err != nil {
		return nil, errFenceUnreadable
	}
	if len(data) == 0 || len(data) > maxFenceBytes {
		return nil, entity.ErrContract
	}
	return data, nil
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func (c *Client) Search(ctx context.Context, input entity.SearchInput) (result entity.SearchResult, resultErr error) {
	started := time.Now()
	defer func() {
		platformmetrics.Default().ObserveLightRAGQuery(string(input.Mode), lightragErrorClass(resultErr), time.Since(started))
	}()
	query := input.Query
	if input.GameCode != "" {
		query += "\nGame-Code: " + input.GameCode
	}
	if input.RegionCode != "" {
		query += "\nRegion-Code: " + input.RegionCode
	}
	payload := map[string]any{
		"query": query, "mode": input.Mode, "top_k": 20, "chunk_top_k": 12,
		"max_total_tokens": 12000, "include_references": true,
	}
	var response queryDataResponse
	if err := c.do(ctx, http.MethodPost, "/query/data", payload, &response); err != nil {
		return entity.SearchResult{}, err
	}
	if response.Status != "success" {
		return entity.SearchResult{}, entity.ErrUnavailable
	}
	if strings.TrimSpace(response.Message) == "" || response.Data == nil || response.Metadata == nil || response.Data.Entities == nil || response.Data.Relationships == nil || response.Data.Chunks == nil || response.Data.References == nil {
		return entity.SearchResult{}, entity.ErrContract
	}
	items := normalizeEvidence(*response.Data)
	return entity.SearchResult{Query: input.Query, Provider: "lightrag", Mode: input.Mode, Items: items}, nil
}

func (c *Client) Create(ctx context.Context, sourceKey, text string) (entity.AcceptedDocument, error) {
	var response struct {
		Status  string `json:"status"`
		TrackID string `json:"track_id"`
	}
	err := c.do(ctx, http.MethodPost, "/documents/text", map[string]string{"text": text, "file_source": sourceKey}, &response)
	if errors.Is(err, entity.ErrConflict) {
		documents, reconcileErr := c.allManagedDocuments(ctx, entity.ListInput{Page: 1, PageSize: 100, SortField: "sourceKey", SortDirection: "asc"})
		if reconcileErr != nil {
			return entity.AcceptedDocument{SourceKey: sourceKey}, err
		}
		matches := make([]entity.Document, 0, 1)
		for _, document := range documents {
			if document.SourceKey == sourceKey {
				matches = append(matches, document)
			}
		}
		if len(matches) == 1 && matches[0].TrackID != "" {
			return entity.AcceptedDocument{TrackID: matches[0].TrackID, SourceKey: sourceKey, Status: "accepted", Replayed: true}, nil
		}
		return entity.AcceptedDocument{SourceKey: sourceKey}, entity.ErrConflict
	}
	if err != nil {
		return entity.AcceptedDocument{SourceKey: sourceKey}, err
	}
	if response.Status != "success" || !validOpaque(response.TrackID) {
		return entity.AcceptedDocument{SourceKey: sourceKey}, entity.ErrContract
	}
	return entity.AcceptedDocument{TrackID: response.TrackID, SourceKey: sourceKey, Status: "accepted"}, nil
}

func (c *Client) Track(ctx context.Context, trackID string) (entity.Track, error) {
	var response trackResponse
	if err := c.do(ctx, http.MethodGet, "/documents/track_status/"+url.PathEscape(trackID), nil, &response); err != nil {
		return entity.Track{}, err
	}
	if response.TrackID != trackID {
		return entity.Track{}, entity.ErrContract
	}
	documents, err := managedDocuments(response.Documents)
	if err != nil || response.TotalCount != len(response.Documents) {
		return entity.Track{}, entity.ErrContract
	}
	if len(documents) == 0 {
		return entity.Track{}, entity.ErrNotFound
	}
	return entity.Track{TrackID: trackID, Documents: documents, TotalCount: len(documents), StatusCounts: documentStatusCounts(documents)}, nil
}

func (c *Client) List(ctx context.Context, input entity.ListInput) (entity.DocumentList, error) {
	documents, err := c.allManagedDocuments(ctx, input)
	if err != nil {
		return entity.DocumentList{}, err
	}
	start := (input.Page - 1) * input.PageSize
	if start > len(documents) {
		start = len(documents)
	}
	end := start + input.PageSize
	if end > len(documents) {
		end = len(documents)
	}
	totalPages := 0
	if len(documents) > 0 {
		totalPages = (len(documents) + input.PageSize - 1) / input.PageSize
	}
	return entity.DocumentList{Items: documents[start:end], Page: input.Page, PageSize: input.PageSize, TotalCount: len(documents), TotalPages: totalPages}, nil
}

func (c *Client) Delete(ctx context.Context, documentID string) (entity.DeleteResult, error) {
	documents, err := c.allManagedDocuments(ctx, entity.ListInput{Page: 1, PageSize: 100, SortField: "documentId", SortDirection: "asc"})
	if err != nil {
		return entity.DeleteResult{}, err
	}
	found := false
	for _, document := range documents {
		if document.DocumentID == documentID {
			found = true
			break
		}
	}
	if !found {
		return entity.DeleteResult{}, entity.ErrNotFound
	}
	var response struct {
		Status string `json:"status"`
		DocID  string `json:"doc_id"`
	}
	payload := map[string]any{"doc_ids": []string{documentID}, "delete_file": false, "delete_llm_cache": false}
	if err := c.do(ctx, http.MethodDelete, "/documents/delete_document", payload, &response); err != nil {
		return entity.DeleteResult{}, err
	}
	if response.Status == "busy" {
		return entity.DeleteResult{}, entity.ErrConflict
	}
	if response.Status != "deletion_started" || response.DocID != documentID {
		return entity.DeleteResult{}, entity.ErrContract
	}
	return entity.DeleteResult{DocumentID: documentID, Status: response.Status}, nil
}

func (c *Client) allManagedDocuments(ctx context.Context, input entity.ListInput) ([]entity.Document, error) {
	sortField := map[string]string{"createdAt": "created_at", "updatedAt": "updated_at", "documentId": "id", "sourceKey": "file_path"}[input.SortField]
	if sortField == "" {
		sortField = "updated_at"
	}
	var managed []entity.Document
	seenIDs, seenSources := map[string]bool{}, map[string]bool{}
	expectedTotalCount, expectedTotalPages := -1, -1
	for pageNumber := 1; pageNumber <= maxUpstreamPages; pageNumber++ {
		payload := map[string]any{"page": pageNumber, "page_size": upstreamPageSize, "sort_field": sortField, "sort_direction": input.SortDirection}
		if input.Status != "" {
			payload["status_filter"] = strings.ToUpper(input.Status)
		}
		var response paginatedResponse
		if err := c.do(ctx, http.MethodPost, "/documents/paginated", payload, &response); err != nil {
			return nil, err
		}
		if !validPagination(response.Pagination, pageNumber, len(response.Documents)) {
			return nil, entity.ErrContract
		}
		if expectedTotalCount < 0 {
			expectedTotalCount, expectedTotalPages = response.Pagination.TotalCount, response.Pagination.TotalPages
			if expectedTotalPages > maxUpstreamPages {
				return nil, entity.ErrCapacity
			}
		} else if response.Pagination.TotalCount != expectedTotalCount || response.Pagination.TotalPages != expectedTotalPages {
			return nil, entity.ErrConflict
		}
		pageDocuments, mapErr := managedDocuments(response.Documents)
		if mapErr != nil {
			return nil, mapErr
		}
		for _, document := range pageDocuments {
			if seenIDs[document.DocumentID] || seenSources[document.SourceKey] {
				return nil, entity.ErrContract
			}
			seenIDs[document.DocumentID], seenSources[document.SourceKey] = true, true
			managed = append(managed, document)
		}
		if pageNumber >= response.Pagination.TotalPages || len(response.Documents) == 0 {
			break
		}
		if pageNumber == maxUpstreamPages {
			return nil, entity.ErrCapacity
		}
	}
	platformmetrics.Default().SetLightRAGDocumentStatuses(documentStatusCounts(managed))
	return managed, nil
}

func (c *Client) do(ctx context.Context, method, endpoint string, requestBody, responseBody any) (resultErr error) {
	started := time.Now()
	statusCode := 0
	defer func() {
		operation, outcome, duration := lightragOperation(method, endpoint), lightragErrorClass(resultErr), time.Since(started)
		slog.InfoContext(ctx, "lightrag request completed", "event", "lightrag.request", "operation", operation, "outcome", outcome, "status_code", statusCode, "latency_ms", duration.Milliseconds())
		platformmetrics.Default().ObserveLightRAGRequest(operation, outcome, duration)
	}()
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-API-Key", c.apiKey)
	request.Header.Set("LIGHTRAG-WORKSPACE", c.workspace)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return entity.ErrUnavailable
	}
	defer response.Body.Close()
	statusCode = response.StatusCode
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return entity.ErrUnavailable
	}
	if len(data) > maxResponseBytes {
		return entity.ErrContract
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return mapStatus(response.StatusCode)
	}
	if responseBody == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(responseBody); err != nil {
		return entity.ErrContract
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return entity.ErrContract
	}
	return nil
}

func lightragOperation(method, endpoint string) string {
	switch {
	case method == http.MethodGet && endpoint == "/auth/verify":
		return "auth_verify"
	case method == http.MethodGet && endpoint == "/health":
		return "health"
	case method == http.MethodGet && endpoint == "/documents/pipeline_status":
		return "pipeline_status"
	case method == http.MethodPost && endpoint == "/query/data":
		return "query"
	case method == http.MethodPost && endpoint == "/documents/text":
		return "document_create"
	case method == http.MethodGet && strings.HasPrefix(endpoint, "/documents/track_status/"):
		return "document_track"
	case method == http.MethodPost && endpoint == "/documents/paginated":
		return "document_list"
	case method == http.MethodDelete && endpoint == "/documents/delete_document":
		return "document_delete"
	default:
		return "unexpected"
	}
}

func lightragErrorClass(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, entity.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, entity.ErrNotFound):
		return "not_found"
	case errors.Is(err, entity.ErrConflict):
		return "conflict"
	case errors.Is(err, entity.ErrCapacity):
		return "capacity"
	case errors.Is(err, entity.ErrUnavailable):
		return "unavailable"
	default:
		return "contract"
	}
}

func mapStatus(status int) error {
	switch status {
	case 400, 413, 422:
		return entity.ErrInvalidInput
	case 401, 403:
		return entity.ErrContract
	case 404:
		return entity.ErrNotFound
	case 409:
		return entity.ErrConflict
	case 429:
		return entity.ErrCapacity
	case 500, 502, 503, 504:
		return entity.ErrUnavailable
	default:
		return entity.ErrContract
	}
}

type rawDocument struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	TrackID       string `json:"track_id"`
	ErrorMsg      string `json:"error_msg"`
	FilePath      string `json:"file_path"`
	ContentLength int    `json:"content_length"`
	ChunksCount   *int   `json:"chunks_count"`
}
type trackResponse struct {
	TrackID       string         `json:"track_id"`
	Documents     []rawDocument  `json:"documents"`
	TotalCount    int            `json:"total_count"`
	StatusSummary map[string]int `json:"status_summary"`
}
type paginatedResponse struct {
	Documents  []rawDocument  `json:"documents"`
	Pagination paginationInfo `json:"pagination"`
}
type paginationInfo struct {
	Page       int  `json:"page"`
	PageSize   int  `json:"page_size"`
	TotalCount int  `json:"total_count"`
	TotalPages int  `json:"total_pages"`
	HasNext    bool `json:"has_next"`
	HasPrev    bool `json:"has_prev"`
}
type queryDataResponse struct {
	Status   string         `json:"status"`
	Message  string         `json:"message"`
	Data     *queryData     `json:"data"`
	Metadata map[string]any `json:"metadata"`
}
type queryData struct{ Entities, Relationships, Chunks, References []map[string]any }

func managedDocuments(raw []rawDocument) ([]entity.Document, error) {
	result := make([]entity.Document, 0, len(raw))
	seenIDs, seenSources := map[string]bool{}, map[string]bool{}
	for _, item := range raw {
		source := strings.TrimSpace(item.FilePath)
		if !entity.IsManagedSource(source) {
			continue
		}
		status := strings.ToUpper(strings.TrimSpace(item.Status))
		createdAt, createdErr := time.Parse(time.RFC3339Nano, item.CreatedAt)
		updatedAt, updatedErr := time.Parse(time.RFC3339Nano, item.UpdatedAt)
		chunks := 0
		if item.ChunksCount != nil {
			chunks = *item.ChunksCount
		}
		if !validOpaque(item.ID) || item.TrackID != "" && !validOpaque(item.TrackID) || !validDocumentStatus(status) || item.ContentLength < 0 || chunks < 0 || createdErr != nil || updatedErr != nil || seenIDs[item.ID] || seenSources[source] {
			return nil, entity.ErrContract
		}
		seenIDs[item.ID], seenSources[source] = true, true
		result = append(result, entity.Document{DocumentID: item.ID, SourceKey: source, Status: status, ContentLength: item.ContentLength, ChunksCount: chunks, CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC(), FailureCode: safeFailureCode(item.ErrorMsg), TrackID: item.TrackID})
	}
	return result, nil
}
func safeFailureCode(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "processing_failed"
}
func documentStatusCounts(documents []entity.Document) map[string]int {
	result := make(map[string]int)
	for _, document := range documents {
		result[document.Status]++
	}
	return result
}
func validDocumentStatus(value string) bool {
	switch value {
	case "PENDING", "PARSING", "ANALYZING", "PREPROCESSED", "PROCESSING", "PROCESSED", "FAILED":
		return true
	default:
		return false
	}
}
func validPagination(value paginationInfo, page, documents int) bool {
	if value.Page != page || value.PageSize != upstreamPageSize || value.TotalCount < 0 || value.TotalPages < 0 || documents < 0 || documents > upstreamPageSize || value.HasPrev != (page > 1) || value.HasNext != (page < value.TotalPages) {
		return false
	}
	expectedPages := 0
	if value.TotalCount > 0 {
		expectedPages = (value.TotalCount + upstreamPageSize - 1) / upstreamPageSize
	}
	if value.TotalPages != expectedPages || page < value.TotalPages && documents != upstreamPageSize || page == value.TotalPages && documents != value.TotalCount-(page-1)*upstreamPageSize || value.TotalPages == 0 && documents != 0 {
		return false
	}
	return true
}
func validOpaque(value string) bool {
	return len(value) > 0 && len(value) <= 128 && !strings.ContainsAny(value, "/\\?&#%\r\n\t ")
}

func normalizeEvidence(data queryData) []entity.Evidence {
	referenceSources := map[string]string{}
	for _, raw := range data.References {
		id := stringValue(raw["reference_id"])
		source := managedSource(stringValue(raw["file_path"]))
		if id != "" && source != "" {
			referenceSources[id] = source
		}
	}
	type candidate struct {
		kind, text, source, reference string
		attributes                    map[string]string
	}
	items := make([]candidate, 0, len(data.Chunks)+len(data.Entities)+len(data.Relationships))
	appendItem := func(kind, text string, raw map[string]any, attributes map[string]string) {
		reference := stringValue(raw["reference_id"])
		source := managedSource(stringValue(raw["file_path"]))
		if source == "" {
			source = referenceSources[reference]
		}
		text = strings.TrimSpace(text)
		if source == "" || text == "" {
			return
		}
		runes := []rune(text)
		if len(runes) > 2000 {
			text = string(runes[:1999]) + "…"
		}
		items = append(items, candidate{kind, text, source, reference, attributes})
	}
	for _, raw := range data.Chunks {
		appendItem("chunk", stringValue(raw["content"]), raw, nil)
	}
	for _, raw := range data.Entities {
		appendItem("entity", stringValue(raw["description"]), raw, map[string]string{"name": stringValue(raw["entity_name"]), "type": stringValue(raw["entity_type"])})
	}
	for _, raw := range data.Relationships {
		appendItem("relationship", stringValue(raw["description"]), raw, map[string]string{"source": stringValue(raw["src_id"]), "target": stringValue(raw["tgt_id"])})
	}
	result := make([]entity.Evidence, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if len(result) == maxEvidenceItems {
			break
		}
		key := item.kind + "\x00" + item.source + "\x00" + item.text
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, entity.Evidence{EvidenceID: fmt.Sprintf("ev_%03d", len(result)+1), Kind: item.kind, Text: item.text, SourceKey: item.source, ReferenceID: boundedOpaque(item.reference), Attributes: boundedAttributes(item.attributes)})
	}
	return result
}
func managedSource(value string) string {
	value = strings.TrimSpace(value)
	if entity.IsManagedSource(value) {
		return value
	}
	return ""
}
func stringValue(value any) string { text, _ := value.(string); return strings.TrimSpace(text) }
func boundedOpaque(value string) string {
	value = strings.TrimSpace(value)
	if !validOpaque(value) {
		return ""
	}
	return value
}
func boundedAttributes(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		runes := []rune(strings.TrimSpace(value))
		if len(runes) > 256 {
			runes = runes[:256]
		}
		if len(runes) > 0 {
			result[key] = string(runes)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

var _ interface {
	Health(context.Context) (entity.Health, error)
	Search(context.Context, entity.SearchInput) (entity.SearchResult, error)
	Create(context.Context, string, string) (entity.AcceptedDocument, error)
	Track(context.Context, string) (entity.Track, error)
	List(context.Context, entity.ListInput) (entity.DocumentList, error)
	Delete(context.Context, string) (entity.DeleteResult, error)
} = (*Client)(nil)
