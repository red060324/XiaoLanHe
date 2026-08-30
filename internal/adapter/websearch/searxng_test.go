package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearXNGSearch(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		result, err := NewSearXNG(false, "searxng", "", time.Second).Search(context.Background(), "q")
		if err != nil || result.Enabled || result.Query != "q" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("q") != "hello world" {
				t.Fatalf("query=%q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"A","url":"https://a","content":"fact","engine":"google"}]}`))
		}))
		defer server.Close()
		result, err := NewSearXNG(true, "searxng", server.URL, time.Second).Search(context.Background(), "hello world")
		if err != nil || len(result.Items) != 1 || result.Items[0].Source != "google" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("provider failure is represented in response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer server.Close()
		result, err := NewSearXNG(true, "searxng", server.URL, time.Second).Search(context.Background(), "q")
		if err != nil || !result.Enabled || !strings.Contains(result.Note, "HTTP 500") {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}
