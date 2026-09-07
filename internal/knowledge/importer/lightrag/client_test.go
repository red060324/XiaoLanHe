package lightrag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

const testTimeout = time.Second

func TestListPageUsesExactPageWithoutFourThousandCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/documents/paginated" || request.Header.Get("X-API-Key") != strings.Repeat("k", 32) || request.Header.Get("LIGHTRAG-WORKSPACE") != "xiaolanhe_v1" {
			t.Fatalf("request=%s headers=%v", request.URL.Path, request.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["page"] != float64(21) || payload["page_size"] != float64(200) {
			t.Fatalf("payload=%v", payload)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"documents":  []any{map[string]any{"id": "doc-4001", "status": "PROCESSED", "track_id": "track-4001", "file_path": "xlh-legacy-4001.txt", "content_length": 123, "chunks_count": 7}},
			"pagination": map[string]any{"page": 21, "page_size": 200, "total_count": 4001, "total_pages": 21, "has_next": false, "has_prev": true},
		})
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: strings.Repeat("k", 32), Workspace: "xiaolanhe_v1", AllowInsecure: true, Timeout: testTimeout})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ListPage(context.Background(), 21, 200)
	if err != nil || page.RawTotal != 4001 || len(page.Documents) != 1 || page.Documents[0].SourceKey != "xlh-legacy-4001.txt" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestHealthCombinesPipelineSignals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			_, _ = fmt.Fprint(writer, `{"pipeline_active":false}`)
		case "/documents/pipeline_status":
			_, _ = fmt.Fprint(writer, `{"busy":true,"recovery_required":false}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, APIKey: strings.Repeat("k", 32), Workspace: "xiaolanhe_v1", AllowInsecure: true, Timeout: testTimeout})
	health, err := client.Health(context.Background())
	if err != nil || !health.PipelineActive || health.RecoveryRequired {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestClientRequiresExplicitHTTPOptIn(t *testing.T) {
	config := Config{BaseURL: "http://lightrag.example", APIKey: strings.Repeat("k", 32), Workspace: "xiaolanhe_v1", Timeout: testTimeout}
	if _, err := NewClient(config); err == nil {
		t.Fatal("HTTP without explicit opt-in unexpectedly accepted")
	}
	config.AllowInsecure = true
	if _, err := NewClient(config); err != nil {
		t.Fatalf("explicit HTTP opt-in rejected: %v", err)
	}
	config.BaseURL = "https://lightrag.example"
	config.AllowInsecure = false
	if _, err := NewClient(config); err != nil {
		t.Fatalf("HTTPS rejected without insecure opt-in: %v", err)
	}
}

func TestClientDoesNotForwardCredentialsAcrossRedirects(t *testing.T) {
	reachedTarget := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reachedTarget = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	client, err := NewClient(Config{BaseURL: redirect.URL, APIKey: strings.Repeat("k", 32), Workspace: "xiaolanhe_v1", AllowInsecure: true, Timeout: testTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListPage(context.Background(), 1, 20); !errors.Is(err, entity.ErrContract) || reachedTarget {
		t.Fatalf("err=%v reached=%t", err, reachedTarget)
	}
}
