package lightrag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

const (
	maxResponseBytes = 2 << 20
	maxUpstreamPages = 20
	upstreamPageSize = 200
	maxEvidenceItems = 32
)

var workspacePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

type Config struct {
	BaseURL, APIKey, Workspace, WorkingDirectory string
	CoreVersion, APIVersion                      string
	Timeout                                      time.Duration
}

type Client struct {
	baseURL, apiKey, workspace, workingDirectory string
	coreVersion, apiVersion                      string
	http                                         *http.Client
}

func NewClient(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("LightRAG base URL must be an absolute http(s) URL without credentials, query or fragment")
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	workingDirectory := strings.TrimSpace(cfg.WorkingDirectory)
	if len(apiKey) < 32 || len(apiKey) > 512 || strings.ContainsAny(apiKey, "\r\n") || !workspacePattern.MatchString(cfg.Workspace) || !strings.HasPrefix(workingDirectory, "/") || path.Clean(workingDirectory) != workingDirectory || cfg.Timeout <= 0 || cfg.CoreVersion != "1.5.7" || cfg.APIVersion != "0344" {
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
		http: &http.Client{Transport: transport, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *Client) Health(ctx context.Context) (result entity.Health, resultErr error) {
	defer func() {
		platformmetrics.Default().SetLightRAGHealth(resultErr == nil, result.PipelineActive, result.RecoveryRequired)
	}()
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
		health.KVStorage != "JsonKVStorage" || health.VectorStorage != "NanoVectorDBStorage" || health.GraphStorage != "NetworkXStorage" || health.DocStatusStorage != "JsonDocStatusStorage" ||
		health.ServerMode != "gunicorn" || health.Workers != 2 || health.RecoveryRequired {
		return health, entity.ErrContract
	}
	slog.InfoContext(ctx, "lightrag health verified", "event", "lightrag.health", "operation", "ready", "outcome", "healthy", "core_version", health.CoreVersion, "api_version", health.APIVersion, "server_mode", health.ServerMode, "workers", health.Workers, "kv_storage", health.KVStorage, "vector_storage", health.VectorStorage, "graph_storage", health.GraphStorage, "doc_status_storage", health.DocStatusStorage, "pipeline_active", health.PipelineActive, "pipeline_busy", *pipeline.Busy, "recovery_required", health.RecoveryRequired)
	return health, nil
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
