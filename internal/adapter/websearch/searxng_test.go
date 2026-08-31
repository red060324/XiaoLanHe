package websearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearXNGSearch(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		result, err := NewSearXNG(false, "", time.Second).Search(context.Background(), "q")
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
		result, err := NewSearXNG(true, server.URL, time.Second).Search(context.Background(), "hello world")
		if err != nil || result.Provider != "searxng" || len(result.Items) != 1 || result.Items[0].Source != "google" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("empty result is successful", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[]}`))
		}))
		defer server.Close()
		result, err := NewSearXNG(true, server.URL, time.Second).Search(context.Background(), "q")
		if err != nil || !result.Enabled || len(result.Items) != 0 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("cancelled request returns error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewSearXNG(true, "http://127.0.0.1", time.Second).Search(ctx, "q")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled", err)
		}
	})
	t.Run("non success response returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer server.Close()
		_, err := NewSearXNG(true, server.URL, time.Second).Search(context.Background(), "q")
		if err == nil {
			t.Fatal("expected provider error")
		}
	})
	t.Run("invalid response returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		}))
		defer server.Close()
		_, err := NewSearXNG(true, server.URL, time.Second).Search(context.Background(), "q")
		if err == nil {
			t.Fatal("expected decode error")
		}
	})
	t.Run("oversized response returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"title":"A","url":"https://a","content":"` + strings.Repeat("x", 1<<20) + `"}]}`))
		}))
		defer server.Close()
		if _, err := NewSearXNG(true, server.URL, time.Second).Search(context.Background(), "q"); err == nil {
			t.Fatal("expected oversized response error")
		}
	})
}
