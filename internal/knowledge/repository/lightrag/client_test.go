package lightrag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

const testAPIKey = "test-lightrag-key-at-least-32-chars"

func TestHealthRequiresExpectedOfficialContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != testAPIKey || r.Header.Get("LIGHTRAG-WORKSPACE") != "xiaolanhe_v1" {
			t.Errorf("headers=%v", r.Header)
		}
		switch r.URL.Path {
		case "/auth/verify":
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/health":
			json.NewEncoder(w).Encode(healthyPayload())
		case "/documents/pipeline_status":
			json.NewEncoder(w).Encode(map[string]bool{"busy": false, "recovery_required": false})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := testClient(t, server.URL)
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthRejectsAuthenticationAndRuntimeTopologyDrift(t *testing.T) {
	tests := []struct {
		name     string
		auth     any
		health   map[string]any
		pipeline any
	}{
		{name: "unexpected authentication response", auth: map[string]string{"status": "unexpected"}, health: healthyPayload(), pipeline: map[string]bool{"busy": false, "recovery_required": false}},
		{name: "uvicorn deployment", auth: map[string]string{"status": "ok"}, health: withHealthValue("server_mode", "uvicorn"), pipeline: map[string]bool{"busy": false, "recovery_required": false}},
		{name: "wrong worker count", auth: map[string]string{"status": "ok"}, health: withHealthValue("workers", 1), pipeline: map[string]bool{"busy": false, "recovery_required": false}},
		{name: "missing health pipeline state", auth: map[string]string{"status": "ok"}, health: withoutHealthValue("pipeline_active"), pipeline: map[string]bool{"busy": false, "recovery_required": false}},
		{name: "missing pipeline busy", auth: map[string]string{"status": "ok"}, health: healthyPayload(), pipeline: map[string]bool{"recovery_required": false}},
		{name: "missing recovery fence", auth: map[string]string{"status": "ok"}, health: healthyPayload(), pipeline: map[string]bool{"busy": false}},
		{name: "recovery fenced", auth: map[string]string{"status": "ok"}, health: healthyPayload(), pipeline: map[string]bool{"busy": false, "recovery_required": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth/verify":
					_ = json.NewEncoder(w).Encode(test.auth)
				case "/health":
					_ = json.NewEncoder(w).Encode(test.health)
				case "/documents/pipeline_status":
					_ = json.NewEncoder(w).Encode(test.pipeline)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			if _, err := testClient(t, server.URL).Health(context.Background()); !errors.Is(err, entity.ErrContract) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestSearchUsesFixedCapsAndDropsUnmanagedEvidence(t *testing.T) {
	managed := "xlh-" + strings.Repeat("a", 64) + ".txt"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["mode"] != "mix" || request["top_k"] != float64(20) || request["chunk_top_k"] != float64(12) || request["max_total_tokens"] != float64(12000) || request["conversation_history"] != nil || request["user_prompt"] != nil {
			t.Errorf("request=%#v", request)
		}
		json.NewEncoder(w).Encode(queryPayload([]any{map[string]any{"content": "trusted fact", "file_path": managed, "reference_id": "1"}, map[string]any{"content": "foreign", "file_path": "outside.txt", "reference_id": "2"}}))
	}))
	defer server.Close()
	result, err := testClient(t, server.URL).Search(context.Background(), entity.SearchInput{Query: "guide", Mode: entity.ModeMix})
	if err != nil || len(result.Items) != 1 || result.Items[0].Text != "trusted fact" || result.Items[0].EvidenceID != "ev_001" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCreateTrackListAndDelete(t *testing.T) {
	managed := "xlh-" + strings.Repeat("b", 64) + ".txt"
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/documents/text":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["file_source"] != managed {
				t.Errorf("body=%v", body)
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "success", "track_id": "insert-1"})
		case "/documents/track_status/insert-1":
			json.NewEncoder(w).Encode(trackPayload(managed))
		case "/documents/paginated":
			json.NewEncoder(w).Encode(paginatedPayload(1, []any{documentPayload("doc-1", managed), documentPayload("doc-2", "foreign.txt")}, 2))
		case "/documents/delete_document":
			if r.Method != http.MethodDelete {
				t.Errorf("method=%s", r.Method)
			}
			var body struct {
				DocIDs      []string `json:"doc_ids"`
				DeleteFile  bool     `json:"delete_file"`
				DeleteCache bool     `json:"delete_llm_cache"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if len(body.DocIDs) != 1 || body.DocIDs[0] != "doc-1" || body.DeleteFile || body.DeleteCache {
				t.Errorf("delete=%+v", body)
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "deletion_started", "doc_id": "doc-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := testClient(t, server.URL)
	accepted, err := client.Create(context.Background(), managed, "body")
	if err != nil || accepted.TrackID != "insert-1" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	track, err := client.Track(context.Background(), "insert-1")
	if err != nil || len(track.Documents) != 1 || track.Documents[0].DocumentID != "doc-1" {
		t.Fatalf("track=%+v err=%v", track, err)
	}
	list, err := client.List(context.Background(), entity.ListInput{Page: 1, PageSize: 20, SortField: "updatedAt", SortDirection: "desc"})
	if err != nil || len(list.Items) != 1 || list.TotalCount != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	deleted, err := client.Delete(context.Background(), "doc-1")
	if err != nil || deleted.Status != "deletion_started" || calls["/documents/paginated"] != 2 {
		t.Fatalf("deleted=%+v calls=%v err=%v", deleted, calls, err)
	}
	if _, err := client.Delete(context.Background(), "doc-2"); !errors.Is(err, entity.ErrNotFound) {
		t.Fatalf("foreign delete err=%v", err)
	}
}

func TestLightRAGStatusAndResponseFailures(t *testing.T) {
	statusCases := map[int]error{
		http.StatusBadRequest: entity.ErrInvalidInput, http.StatusRequestEntityTooLarge: entity.ErrInvalidInput, http.StatusUnauthorized: entity.ErrContract,
		http.StatusForbidden: entity.ErrContract, http.StatusNotFound: entity.ErrNotFound,
		http.StatusConflict: entity.ErrConflict, http.StatusUnprocessableEntity: entity.ErrInvalidInput,
		http.StatusTooManyRequests: entity.ErrCapacity, http.StatusInternalServerError: entity.ErrUnavailable,
		http.StatusBadGateway: entity.ErrUnavailable, http.StatusServiceUnavailable: entity.ErrUnavailable,
		http.StatusGatewayTimeout: entity.ErrUnavailable, http.StatusTeapot: entity.ErrContract,
	}
	for status, want := range statusCases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			_, err := testClient(t, server.URL).Search(context.Background(), entity.SearchInput{Query: "guide", Mode: entity.ModeMix})
			if !errors.Is(err, want) {
				t.Fatalf("status=%d err=%v want=%v", status, err, want)
			}
		})
	}

	for _, test := range []struct {
		name, body string
	}{
		{name: "malformed", body: `{`},
		{name: "trailing payload", body: `{"status":"success","data":{}} {}`},
		{name: "oversize", body: strings.Repeat("x", maxResponseBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			_, err := testClient(t, server.URL).Search(context.Background(), entity.SearchInput{Query: "guide", Mode: entity.ModeMix})
			if !errors.Is(err, entity.ErrContract) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestSearchRejectsIncompleteOfficialShapeAndBoundsEvidence(t *testing.T) {
	managed := "xlh-" + strings.Repeat("d", 64) + ".txt"
	t.Run("missing required envelope", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"chunks": []any{}}})
		}))
		defer server.Close()
		if _, err := testClient(t, server.URL).Search(context.Background(), entity.SearchInput{Query: "guide", Mode: entity.ModeMix}); !errors.Is(err, entity.ErrContract) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("evidence count content and attributes are bounded", func(t *testing.T) {
		chunks := make([]any, 0, maxEvidenceItems+10)
		for index := 0; index < maxEvidenceItems+10; index++ {
			chunks = append(chunks, map[string]any{"content": strings.Repeat("界", index+1) + strings.Repeat("x", 2100-index), "file_path": managed, "reference_id": strings.Repeat("x", 200)})
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(queryPayload(chunks)) }))
		defer server.Close()
		result, err := testClient(t, server.URL).Search(context.Background(), entity.SearchInput{Query: "guide", Mode: entity.ModeMix})
		if err != nil || len(result.Items) != maxEvidenceItems || len([]rune(result.Items[0].Text)) != 2000 || result.Items[0].ReferenceID != "" {
			t.Fatalf("items=%d first=%+v err=%v", len(result.Items), result.Items[0], err)
		}
	})
}

func TestManagedListingFailsClosedOnCapacityAndSnapshotDrift(t *testing.T) {
	managed := "xlh-" + strings.Repeat("e", 64) + ".txt"
	t.Run("capacity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"documents": make([]any, upstreamPageSize), "pagination": map[string]any{"page": 1, "page_size": upstreamPageSize, "total_count": maxUpstreamPages*upstreamPageSize + 1, "total_pages": maxUpstreamPages + 1, "has_next": true, "has_prev": false}, "status_counts": map[string]int{}})
		}))
		defer server.Close()
		_, err := testClient(t, server.URL).List(context.Background(), entity.ListInput{Page: 1, PageSize: 20})
		if !errors.Is(err, entity.ErrCapacity) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("snapshot drift", func(t *testing.T) {
		page := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			page++
			documents := make([]any, upstreamPageSize)
			if page == 2 {
				documents = documents[:2]
			}
			for index := range documents {
				documents[index] = documentPayload("foreign-doc-"+string(rune('a'+index%26)), "foreign.txt")
			}
			json.NewEncoder(w).Encode(map[string]any{"documents": documents, "pagination": map[string]any{"page": page, "page_size": upstreamPageSize, "total_count": 200 + page, "total_pages": 2, "has_next": page == 1, "has_prev": page > 1}, "status_counts": map[string]int{}})
		}))
		defer server.Close()
		_, err := testClient(t, server.URL).List(context.Background(), entity.ListInput{Page: 1, PageSize: 20})
		if !errors.Is(err, entity.ErrConflict) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("invalid managed document", func(t *testing.T) {
		document := documentPayload("doc-1", managed)
		document["status"] = "UNKNOWN"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(paginatedPayload(1, []any{document}, 1))
		}))
		defer server.Close()
		_, err := testClient(t, server.URL).List(context.Background(), entity.ListInput{Page: 1, PageSize: 20})
		if !errors.Is(err, entity.ErrContract) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("duplicate managed source", func(t *testing.T) {
		documents := []any{documentPayload("doc-1", managed), documentPayload("doc-2", managed)}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(paginatedPayload(1, documents, 2))
		}))
		defer server.Close()
		_, err := testClient(t, server.URL).List(context.Background(), entity.ListInput{Page: 1, PageSize: 20})
		if !errors.Is(err, entity.ErrContract) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestCreatePreservesDeterministicSourceKeyOnAmbiguousFailure(t *testing.T) {
	managed := "xlh-" + strings.Repeat("f", 64) + ".txt"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	accepted, err := testClient(t, server.URL).Create(context.Background(), managed, "body")
	if !errors.Is(err, entity.ErrUnavailable) || accepted.SourceKey != managed || accepted.TrackID != "" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
}

func TestClientRejectsUnpinnedOrUnsafeConfiguration(t *testing.T) {
	base := Config{BaseURL: "https://lightrag.example", APIKey: testAPIKey, Workspace: "xiaolanhe_v1", WorkingDirectory: "/app/data/rag_storage", CoreVersion: "1.5.7", APIVersion: "0344", Timeout: time.Second}
	for name, mutate := range map[string]func(*Config){
		"short key":    func(c *Config) { c.APIKey = "secret" },
		"newline key":  func(c *Config) { c.APIKey = testAPIKey + "\nforged" },
		"workspace":    func(c *Config) { c.Workspace = "../other" },
		"directory":    func(c *Config) { c.WorkingDirectory = "/app/data/../other" },
		"core version": func(c *Config) { c.CoreVersion = "latest" },
		"api version":  func(c *Config) { c.APIVersion = "next" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if _, err := NewClient(candidate); err == nil {
				t.Fatal("expected configuration rejection")
			}
		})
	}
}

func TestLightRAGRejectsRedirectAndHonorsCancellation(t *testing.T) {
	reachedTarget := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reachedTarget = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	if _, err := testClient(t, redirect.URL).Search(context.Background(), entity.SearchInput{Query: "guide", Mode: entity.ModeMix}); !errors.Is(err, entity.ErrContract) || reachedTarget {
		t.Fatalf("redirect err=%v reached=%t", err, reachedTarget)
	}

	blocked := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer blocked.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := testClient(t, blocked.URL).Search(ctx, entity.SearchInput{Query: "guide", Mode: entity.ModeMix}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestLightRAGTelemetryExcludesSecretsAndContent(t *testing.T) {
	const keyCanary = "CANARY_LIGHTRAG_API_KEY_32_CHARS_LONG"
	const queryCanary = "CANARY_PRIVATE_QUERY"
	const responseCanary = "CANARY_PROVIDER_CONTENT"
	managed := "xlh-" + strings.Repeat("c", 64) + ".txt"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(queryPayload([]any{map[string]any{"content": responseCanary, "file_path": managed}}))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: keyCanary, Workspace: "xiaolanhe_v1", WorkingDirectory: "/app/data/rag_storage", CoreVersion: "1.5.7", APIVersion: "0344", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if _, err := client.Search(context.Background(), entity.SearchInput{Query: queryCanary, Mode: entity.ModeMix}); err != nil {
		t.Fatal(err)
	}
	logs := output.String()
	for _, secret := range []string{keyCanary, queryCanary, responseCanary, managed} {
		if strings.Contains(logs, secret) {
			t.Fatalf("secret %q reached telemetry: %s", secret, logs)
		}
	}
	if !strings.Contains(logs, `"operation":"query"`) || !strings.Contains(logs, `"outcome":"ok"`) {
		t.Fatalf("missing bounded telemetry: %s", logs)
	}
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(Config{BaseURL: baseURL, APIKey: testAPIKey, Workspace: "xiaolanhe_v1", WorkingDirectory: "/app/data/rag_storage", CoreVersion: "1.5.7", APIVersion: "0344", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func documentPayload(id, source string) map[string]any {
	return map[string]any{"id": id, "status": "PROCESSED", "content_length": 10, "chunks_count": 1, "created_at": "2026-09-04T10:00:00Z", "updated_at": "2026-09-04T10:00:01Z", "track_id": "insert-1", "file_path": source}
}
func trackPayload(source string) map[string]any {
	return map[string]any{"track_id": "insert-1", "documents": []any{documentPayload("doc-1", source)}, "total_count": 1, "status_summary": map[string]int{"PROCESSED": 1}}
}
func queryPayload(chunks []any) map[string]any {
	return map[string]any{"status": "success", "message": "ok", "data": map[string]any{"chunks": chunks, "entities": []any{}, "relationships": []any{}, "references": []any{}}, "metadata": map[string]any{}}
}
func healthyPayload() map[string]any {
	return map[string]any{
		"status": "healthy", "core_version": "1.5.7", "api_version": "0344",
		"working_directory": "/app/data/rag_storage", "server_mode": "gunicorn", "workers": 2, "pipeline_active": false,
		"configuration": map[string]string{"workspace": "xiaolanhe_v1", "kv_storage": "JsonKVStorage", "vector_storage": "NanoVectorDBStorage", "graph_storage": "NetworkXStorage", "doc_status_storage": "JsonDocStatusStorage"},
	}
}
func withoutHealthValue(key string) map[string]any {
	payload := healthyPayload()
	delete(payload, key)
	return payload
}
func withHealthValue(key string, value any) map[string]any {
	payload := healthyPayload()
	payload[key] = value
	return payload
}
func paginatedPayload(page int, documents []any, total int) map[string]any {
	totalPages := 0
	if total > 0 {
		totalPages = (total + upstreamPageSize - 1) / upstreamPageSize
	}
	return map[string]any{"documents": documents, "pagination": map[string]any{"page": page, "page_size": upstreamPageSize, "total_count": total, "total_pages": totalPages, "has_next": page < totalPages, "has_prev": page > 1}, "status_counts": map[string]int{"PROCESSED": total}}
}
