package lightrag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	"github.com/red060324/XiaoLanHe/internal/knowledge/importer"
)

const maxResponseBytes = 2 << 20

type Config struct {
	BaseURL, APIKey, Workspace string
	AllowInsecure              bool
	Timeout                    time.Duration
}

// Client is a migration-only, unbounded-by-total-pages verifier. It intentionally
// does not expose mutation methods; normal create/track/health operations continue
// through the repository-owned official LightRAG client.
type Client struct {
	baseURL, apiKey, workspace string
	http                       *http.Client
}

func NewClient(config Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"))
	apiKey := strings.TrimSpace(config.APIKey)
	workspace := strings.TrimSpace(config.Workspace)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, importer.ErrInvalidOptions
	}
	if base.Scheme == "http" && !config.AllowInsecure {
		return nil, importer.ErrInvalidOptions
	}
	if len(apiKey) < 32 || len(apiKey) > 512 || strings.ContainsAny(apiKey, "\r\n") || workspace == "" || len(workspace) > 64 || config.Timeout <= 0 {
		return nil, importer.ErrInvalidOptions
	}
	return &Client{baseURL: strings.TrimRight(base.String(), "/"), apiKey: apiKey, workspace: workspace, http: &http.Client{Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (client *Client) ListPage(ctx context.Context, page, pageSize int) (importer.TargetPage, error) {
	if page < 1 || pageSize < 1 || pageSize > 1000 {
		return importer.TargetPage{}, importer.ErrInvalidOptions
	}
	var response struct {
		Documents []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			TrackID       string `json:"track_id"`
			FilePath      string `json:"file_path"`
			ErrorMsg      string `json:"error_msg"`
			ContentLength int    `json:"content_length"`
			ChunksCount   *int   `json:"chunks_count"`
		} `json:"documents"`
		Pagination struct {
			Page       int  `json:"page"`
			PageSize   int  `json:"page_size"`
			TotalCount int  `json:"total_count"`
			TotalPages int  `json:"total_pages"`
			HasNext    bool `json:"has_next"`
			HasPrev    bool `json:"has_prev"`
		} `json:"pagination"`
	}
	payload := map[string]any{"page": page, "page_size": pageSize, "sort_field": "file_path", "sort_direction": "asc"}
	if err := client.do(ctx, http.MethodPost, "/documents/paginated", payload, &response); err != nil {
		return importer.TargetPage{}, err
	}
	if response.Pagination.Page != page || response.Pagination.PageSize != pageSize || response.Pagination.TotalCount < 0 || response.Pagination.TotalPages < 0 ||
		response.Pagination.HasPrev != (page > 1) || response.Pagination.HasNext != (page < response.Pagination.TotalPages) || len(response.Documents) > pageSize {
		return importer.TargetPage{}, entity.ErrContract
	}
	documents := make([]entity.Document, 0, len(response.Documents))
	for _, raw := range response.Documents {
		sourceKey := strings.TrimSpace(raw.FilePath)
		if !strings.HasPrefix(sourceKey, "xlh-legacy-") || !entity.IsManagedSource(sourceKey) {
			continue
		}
		chunks := 0
		if raw.ChunksCount != nil {
			chunks = *raw.ChunksCount
		}
		if raw.ID == "" || raw.ContentLength < 0 || chunks < 0 {
			return importer.TargetPage{}, entity.ErrContract
		}
		failureCode := ""
		if strings.TrimSpace(raw.ErrorMsg) != "" {
			failureCode = "processing_failed"
		}
		documents = append(documents, entity.Document{DocumentID: raw.ID, SourceKey: sourceKey, Status: strings.ToUpper(strings.TrimSpace(raw.Status)), ContentLength: raw.ContentLength, ChunksCount: chunks, FailureCode: failureCode, TrackID: raw.TrackID})
	}
	return importer.TargetPage{Documents: documents, Page: page, PageSize: pageSize, RawCount: len(response.Documents), RawTotal: response.Pagination.TotalCount, HasNext: response.Pagination.HasNext}, nil
}

func (client *Client) Health(ctx context.Context) (entity.Health, error) {
	var response struct {
		PipelineActive *bool `json:"pipeline_active"`
	}
	if err := client.do(ctx, http.MethodGet, "/health", nil, &response); err != nil {
		return entity.Health{}, err
	}
	var pipeline struct {
		Busy             *bool `json:"busy"`
		RecoveryRequired *bool `json:"recovery_required"`
	}
	if err := client.do(ctx, http.MethodGet, "/documents/pipeline_status", nil, &pipeline); err != nil {
		return entity.Health{}, err
	}
	if response.PipelineActive == nil || pipeline.Busy == nil || pipeline.RecoveryRequired == nil {
		return entity.Health{}, entity.ErrContract
	}
	return entity.Health{PipelineActive: *response.PipelineActive || *pipeline.Busy, RecoveryRequired: *pipeline.RecoveryRequired}, nil
}

func (client *Client) do(ctx context.Context, method, endpoint string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("X-API-Key", client.apiKey)
	request.Header.Set("LIGHTRAG-WORKSPACE", client.workspace)
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return entity.ErrUnavailable
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return entity.ErrUnavailable
	}
	if len(data) > maxResponseBytes {
		return entity.ErrContract
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return mapStatus(response.StatusCode)
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

func mapStatus(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return entity.ErrContract
	case http.StatusTooManyRequests:
		return entity.ErrCapacity
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return entity.ErrUnavailable
	default:
		return entity.ErrContract
	}
}
