package einoadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

type OpenAIEmbedder struct {
	endpoint, apiKey, model string
	client                  *http.Client
}

const maxEmbeddingResponseBytesPerInput = 64 << 10

func NewOpenAIEmbedder(baseURL, apiKey, model string, timeout time.Duration) *OpenAIEmbedder {
	return &OpenAIEmbedder{endpoint: strings.TrimRight(baseURL, "/") + "/embeddings", apiKey: apiKey, model: model, client: &http.Client{Timeout: timeout}}
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, input []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": e.model, "input": input, "dimensions": 1536})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding provider returned HTTP %d", resp.StatusCode)
	}
	limit := int64(max(1, len(input)) * maxEmbeddingResponseBytesPerInput)
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}
	if int64(len(responseBody)) > limit {
		return nil, fmt.Errorf("embedding response exceeds %d bytes", limit)
	}
	var payload struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, err
	}
	result := make([][]float32, len(input))
	for _, item := range payload.Data {
		if item.Index >= 0 && item.Index < len(result) {
			result[item.Index] = item.Embedding
		}
	}
	for _, item := range result {
		if len(item) != 1536 {
			return nil, usecase.ErrEmbeddingUnavailable
		}
	}
	return result, nil
}

type DisabledEmbedder struct{}

func (DisabledEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, usecase.ErrEmbeddingUnavailable
}
