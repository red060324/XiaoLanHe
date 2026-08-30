package einoadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/usecase"
)

func TestOpenAIEmbedderEmbed(t *testing.T) {
	t.Run("maps indexed embeddings", func(t *testing.T) {
		vector := make([]float32, 1536)
		vector[0] = 1
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer key" {
				t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}})
		}))
		defer server.Close()
		result, err := NewOpenAIEmbedder(server.URL, "key", "embedding", time.Second).Embed(context.Background(), []string{"q"})
		if err != nil || len(result) != 1 || result[0][0] != 1 {
			t.Fatalf("length=%d err=%v", len(result), err)
		}
	})
	t.Run("rejects incompatible dimensions", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 2}}}})
		}))
		defer server.Close()
		_, err := NewOpenAIEmbedder(server.URL, "key", "embedding", time.Second).Embed(context.Background(), []string{"q"})
		if !errors.Is(err, usecase.ErrEmbeddingUnavailable) {
			t.Fatalf("err=%v", err)
		}
	})
}
