package lightrag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
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
		{name: "legacy nano vectors", auth: map[string]string{"status": "ok"}, health: withConfigurationValue("vector_storage", "NanoVectorDBStorage"), pipeline: map[string]bool{"busy": false, "recovery_required": false}},
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

func TestHealthRejectsInvalidRebuildFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/verify":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/health":
			_ = json.NewEncoder(w).Encode(healthyPayload())
		case "/documents/pipeline_status":
			_ = json.NewEncoder(w).Encode(map[string]bool{"busy": false, "recovery_required": false})
		}
	}))
	defer server.Close()
	client := testClient(t, server.URL)
	markerPath := filepath.Join(client.fencePath, "current.json")
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker map[string]any
	if err := json.Unmarshal(markerBytes, &marker); err != nil {
		t.Fatal(err)
	}
	marker["state"] = "failed"
	markerBytes, _ = json.Marshal(marker)
	if err := os.WriteFile(markerPath, markerBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Health(context.Background()); !errors.Is(err, entity.ErrContract) {
		t.Fatalf("err=%v", err)
	}
}

func TestHealthVerifiesFenceBeforeCallingUpstream(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer server.Close()
	client := testClient(t, server.URL)
	client.metrics = platformmetrics.NewRegistry()
	if err := os.Remove(filepath.Join(client.fencePath, "current.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Health(context.Background()); !errors.Is(err, entity.ErrContract) {
		t.Fatalf("Health() error = %v, want %v", err, entity.ErrContract)
	}
	if requests != 0 {
		t.Fatalf("upstream requests = %d, want 0", requests)
	}
	metrics := string(client.metrics.Prometheus())
	for _, want := range []string{
		`xiaolanhe_lightrag_fence_observations_total{state="absent",operation="unknown",outcome="rejected",reason="marker_missing"} 1`,
		`xiaolanhe_lightrag_fence_state{state="absent"} 1`,
		`xiaolanhe_lightrag_generation_match 0`,
		`xiaolanhe_lightrag_backend_expected 0`,
		`xiaolanhe_lightrag_service_healthy 0`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing %q in metrics:\n%s", want, metrics)
		}
	}
}

func TestVerifyFenceObservedClassifiesBoundedFailures(t *testing.T) {
	tests := []struct {
		name                    string
		mutate                  func(*testing.T, *Client, map[string]any, map[string]any)
		wantState, wantOp       string
		wantOutcome, wantReason string
		wantGenerationMatch     bool
		corruptReportDigest     bool
		removeReport            bool
		damageReport            bool
	}{
		{name: "stale", mutate: func(_ *testing.T, _ *Client, marker, _ map[string]any) {
			marker["state"], marker["reason_code"], marker["report_path"], marker["report_sha256"] = "stale", "contract_changed", nil, nil
		}, wantState: "stale", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "contract_changed", wantGenerationMatch: true},
		{name: "failed", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
		}, wantState: "failed", wantOp: "revalidate", wantOutcome: "error", wantReason: "operation_failed", wantGenerationMatch: true},
		{name: "abandoned rebuilding failed", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "abandoned_rebuilding"
			makeFailedReport(t, report, "abandoned_rebuilding")
		}, wantState: "failed", wantOp: "revalidate", wantOutcome: "error", wantReason: "abandoned_rebuilding", wantGenerationMatch: true},
		{name: "failed generation mismatch", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			marker["generation"], report["generation"] = "private-generation-canary", "private-generation-canary"
			makeFailedReport(t, report, "operation_failed")
		}, wantState: "failed", wantOp: "revalidate", wantOutcome: "error", wantReason: "operation_failed"},
		{name: "failed report missing", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_unreadable", wantGenerationMatch: true, removeReport: true},
		{name: "failed report damaged", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_invalid", wantGenerationMatch: true, damageReport: true},
		{name: "failed report digest mismatch", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_digest_mismatch", wantGenerationMatch: true, corruptReportDigest: true},
		{name: "failed report identity mismatch", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
			report["attempt_id"] = "private-attempt-canary"
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_identity_mismatch", wantGenerationMatch: true},
		{name: "failed report semantics invalid", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
			report["errors"] = []any{}
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_invalid", wantGenerationMatch: true},
		{name: "failed report error code outside vocabulary", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
			report["errors"].([]any)[0].(map[string]any)["code"] = "RuntimeError"
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_invalid", wantGenerationMatch: true},
		{name: "failed report timestamp invalid", mutate: func(t *testing.T, _ *Client, marker, report map[string]any) {
			marker["state"], marker["reason_code"] = "failed", "operation_failed"
			makeFailedReport(t, report, "operation_failed")
			report["started_at"] = "2026-09-07T12:02:00Z"
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_invalid", wantGenerationMatch: true},
		{name: "generation mismatch", mutate: func(_ *testing.T, _ *Client, marker, report map[string]any) {
			marker["generation"], report["generation"] = "private-generation-canary", "private-generation-canary"
		}, wantState: "verified", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "generation_mismatch"},
		{name: "contract mismatch", mutate: func(_ *testing.T, _ *Client, marker, _ map[string]any) { marker["contract_sha256"] = testFenceSHA('f') }, wantState: "verified", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "contract_mismatch", wantGenerationMatch: true},
		{name: "report digest mismatch", mutate: func(_ *testing.T, _ *Client, _, _ map[string]any) {}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_digest_mismatch", wantGenerationMatch: true, corruptReportDigest: true},
		{name: "report invalid", mutate: func(t *testing.T, _ *Client, _, report map[string]any) {
			nestedMap(t, report, "checks")["schema_valid"] = false
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_invalid", wantGenerationMatch: true},
		{name: "report identity mismatch", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			report["attempt_id"] = "private-attempt-canary"
		}, wantState: "invalid", wantOp: "revalidate", wantOutcome: "rejected", wantReason: "report_identity_mismatch", wantGenerationMatch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, "https://lightrag.example")
			marker, report := readFenceFixture(t, client)
			test.mutate(t, client, marker, report)
			writeFenceFixture(t, client, marker, report, true, true)
			reportPath := filepath.Join(client.fencePath, "reports", "attempt-1.json")
			if test.removeReport {
				if removeErr := os.Remove(reportPath); removeErr != nil {
					t.Fatal(removeErr)
				}
			}
			if test.damageReport {
				if writeErr := os.WriteFile(reportPath, []byte("{\n"), 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if test.corruptReportDigest {
				marker["report_sha256"] = testFenceSHA('f')
				data, marshalErr := canonicalJSONBytes(marker)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if writeErr := os.WriteFile(filepath.Join(client.fencePath, "current.json"), append(data, '\n'), 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			observation, err := client.verifyFenceObserved()
			if !errors.Is(err, entity.ErrContract) {
				t.Fatalf("error = %v, want %v", err, entity.ErrContract)
			}
			if observation.State != test.wantState || observation.Operation != test.wantOp || observation.Outcome != test.wantOutcome || observation.Reason != test.wantReason || observation.GenerationMatch != test.wantGenerationMatch {
				t.Fatalf("observation = %+v, want state=%s operation=%s outcome=%s reason=%s generation_match=%t", observation, test.wantState, test.wantOp, test.wantOutcome, test.wantReason, test.wantGenerationMatch)
			}
			if observation.Rebuild != nil {
				t.Fatalf("rebuild snapshot = %+v, want nil for rejected or failed fence", observation.Rebuild)
			}
		})
	}
}

func TestVerifyFenceObservedPublishesImmutableRebuildSnapshot(t *testing.T) {
	client := testClient(t, "https://lightrag.example")
	marker, report := readFenceFixture(t, client)
	marker["operation"], report["operation"] = "migration_rebuild", "migration_rebuild"
	makeMigrationRebuildReport(t, report)
	for index, target := range []string{"entities", "relationships", "chunks"} {
		stats := nestedMap(t, report, "rebuild", target)
		stats["source_total"], stats["prepared"], stats["rebuilt"], stats["batches"] = index+2, index+1, index+1, 1
		stats["duplicates"] = 1
		evidence := nestedMap(t, report, "targets", target)
		evidence["expected_count"], evidence["actual_count"] = index+1, index+1
	}
	chunkStats := nestedMap(t, report, "rebuild", "chunks")
	chunkStats["prepared"], chunkStats["rebuilt"], chunkStats["duplicates"] = 4, 4, 0
	chunkTarget := nestedMap(t, report, "targets", "chunks")
	chunkTarget["expected_count"], chunkTarget["actual_count"] = 4, 4
	source := nestedMap(t, report, "source")
	source["graph_nodes"], source["graph_edges_raw"], source["relationships_normalized"], source["text_chunks"] = 2, 3, 2, 4
	consistency := nestedMap(t, report, "official_consistency")
	consistency["graph_entities"], consistency["graph_relations"] = 1, 2
	policy := nestedMap(t, report, "duplicate_policy")
	policy["applicable"], policy["status"], policy["evidence_sha256"], policy["reviewer"], policy["expires_at"] = true, "succeeded", testFenceSHA('d'), "reviewer", "2026-09-07T13:00:00Z"
	maximum := nestedMap(t, report, "duplicate_policy", "max_duplicates")
	maximum["entities"], maximum["relationships"], maximum["chunks"] = 1, 1, 1
	writeFenceFixture(t, client, marker, report, true, true)

	observation, err := client.verifyFenceObserved()
	if err != nil || observation.Rebuild == nil {
		t.Fatalf("observation=%+v error=%v", observation, err)
	}
	want := []platformmetrics.LightRAGRebuildTarget{
		{Target: "entities", SourceTotal: 2, Prepared: 1, Rebuilt: 1, Duplicates: 1},
		{Target: "relationships", SourceTotal: 3, Prepared: 2, Rebuilt: 2, Duplicates: 1},
		{Target: "chunks", SourceTotal: 4, Prepared: 4, Rebuilt: 4},
	}
	if observation.Rebuild.Duration != time.Minute || fmt.Sprint(observation.Rebuild.Targets) != fmt.Sprint(want) {
		t.Fatalf("snapshot = %+v, want duration=%v targets=%+v", observation.Rebuild, time.Minute, want)
	}
}

func TestVerifyFenceAcceptsExactCanonicalV2Report(t *testing.T) {
	client := testClient(t, "https://lightrag.example")
	if err := client.verifyFence(); err != nil {
		t.Fatalf("verifyFence() error = %v", err)
	}
}

func TestVerifyFenceRejectsExactSchemaAndIdentityDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Client, map[string]any, map[string]any)
	}{
		{name: "marker relabel operation", mutate: func(_ *testing.T, _ *Client, marker, _ map[string]any) { marker["operation"] = "bootstrap_empty" }},
		{name: "report relabel operation", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) { report["operation"] = "bootstrap_empty" }},
		{name: "marker generation mismatch", mutate: func(_ *testing.T, _ *Client, marker, _ map[string]any) { marker["generation"] = "other-generation" }},
		{name: "report generation mismatch", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) { report["generation"] = "other-generation" }},
		{name: "report attempt mismatch", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) { report["attempt_id"] = "other-attempt" }},
		{name: "invalid marker identifier prefix", mutate: func(_ *testing.T, _ *Client, marker, report map[string]any) {
			marker["attempt_id"], marker["report_path"] = "_attempt", "reports/_attempt.json"
			report["attempt_id"] = "_attempt"
		}},
		{name: "unsafe report path", mutate: func(_ *testing.T, _ *Client, marker, _ map[string]any) {
			marker["report_path"] = "reports/../attempt-1.json"
		}},
		{name: "unknown marker key", mutate: func(_ *testing.T, _ *Client, marker, _ map[string]any) { marker["unexpected"] = true }},
		{name: "missing marker key", mutate: func(_ *testing.T, _ *Client, marker, _ map[string]any) { delete(marker, "updated_at") }},
		{name: "unknown report key", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) { report["unexpected"] = true }},
		{name: "missing report key", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) { delete(report, "backup") }},
		{name: "unknown nested key", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			nestedMap(t, report, "targets", "entities")["unexpected"] = true
		}},
		{name: "missing nested key", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			delete(nestedMap(t, report, "contract", "embedding_contract"), "query_prefix")
		}},
		{name: "top level contract digest", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) { report["contract_sha256"] = testFenceSHA('a') }},
		{name: "report identity digest mismatch", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			nestedMap(t, report, "contract")["contract_sha256"] = testFenceSHA('b')
		}},
		{name: "wrong schema version", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) { report["schema_version"] = 1 }},
		{name: "malformed timestamp", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			report["finished_at"] = "2026-09-07T12:01:00+00:00"
		}},
		{name: "comma timestamp", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			report["finished_at"] = "2026-09-07T12:01:00,1Z"
		}},
		{name: "timestamp order", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			report["finished_at"] = "2026-09-07T11:59:59Z"
		}},
		{name: "extra check", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			nestedMap(t, report, "checks")["extra"] = true
		}},
		{name: "missing check", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			delete(nestedMap(t, report, "checks"), "cleanup_valid")
		}},
		{name: "false check", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			nestedMap(t, report, "checks")["fixtures_valid"] = false
		}},
		{name: "nonempty errors", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			report["errors"] = []any{map[string]any{"stage": "verify", "target": "chunks", "code": "schema_mismatch", "retryable": false}}
		}},
		{name: "unknown error key", mutate: func(_ *testing.T, _ *Client, _, report map[string]any) {
			report["errors"] = []any{map[string]any{"stage": "verify", "target": "chunks", "code": "schema_mismatch", "retryable": false, "message": "leak"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, "https://lightrag.example")
			marker, report := readFenceFixture(t, client)
			test.mutate(t, client, marker, report)
			writeFenceFixture(t, client, marker, report, true, true)
			if err := client.verifyFence(); !errors.Is(err, entity.ErrContract) {
				t.Fatalf("verifyFence() error = %v, want %v", err, entity.ErrContract)
			}
		})
	}
}

func TestVerifyFenceRejectsNoncanonicalJSON(t *testing.T) {
	tests := []struct {
		name         string
		markerSuffix []byte
		reportSuffix []byte
		prettyMarker bool
		prettyReport bool
	}{
		{name: "pretty marker", prettyMarker: true},
		{name: "pretty report", prettyReport: true},
		{name: "marker trailing spaces", markerSuffix: []byte(" ")},
		{name: "report trailing spaces", reportSuffix: []byte(" ")},
		{name: "marker missing newline", reportSuffix: []byte("\n")},
		{name: "report missing newline", markerSuffix: []byte("\n")},
		{name: "marker two newlines", markerSuffix: []byte("\n\n")},
		{name: "report two newlines", reportSuffix: []byte("\n\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, "https://lightrag.example")
			marker, report := readFenceFixture(t, client)
			writeFenceFixtureWithEncoding(t, client, marker, report, test.prettyMarker, test.prettyReport, test.markerSuffix, test.reportSuffix)
			if err := client.verifyFence(); !errors.Is(err, entity.ErrContract) {
				t.Fatalf("verifyFence() error = %v, want %v", err, entity.ErrContract)
			}
		})
	}
}

func TestVerifyFenceRejectsNoncanonicalNegativeZero(t *testing.T) {
	client := testClient(t, "https://lightrag.example")
	reportPath := filepath.Join(client.fencePath, "reports", "attempt-1.json")
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	report = bytes.Replace(report, []byte(`"actual_count":0`), []byte(`"actual_count":-0`), 1)
	if err := os.WriteFile(reportPath, report, 0600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(client.fencePath, "current.json")
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var markerValue map[string]any
	if err := json.Unmarshal(marker, &markerValue); err != nil {
		t.Fatal(err)
	}
	markerValue["report_sha256"] = fmt.Sprintf("sha256:%x", sha256.Sum256(bytes.TrimSuffix(report, []byte{'\n'})))
	marker, err = canonicalJSONBytes(markerValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, append(marker, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyFence(); !errors.Is(err, entity.ErrContract) {
		t.Fatalf("verifyFence() error = %v, want %v", err, entity.ErrContract)
	}
}

func TestVerifyFenceRejectsReportDigestMismatch(t *testing.T) {
	client := testClient(t, "https://lightrag.example")
	markerPath := filepath.Join(client.fencePath, "current.json")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker map[string]any
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	marker["report_sha256"] = testFenceSHA('f')
	data, err = json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyFence(); !errors.Is(err, entity.ErrContract) {
		t.Fatalf("verifyFence() error = %v, want %v", err, entity.ErrContract)
	}
}

func TestVerifyFenceAcceptsOneCanonicalTrailingNewline(t *testing.T) {
	client := testClient(t, "https://lightrag.example")
	marker, report := readFenceFixture(t, client)
	writeFenceFixture(t, client, marker, report, true, true)
	if err := client.verifyFence(); err != nil {
		t.Fatalf("canonical JSON with one trailing newline rejected: %v", err)
	}
}

func TestFenceContractDigestMatchesPythonCanonicalGolden(t *testing.T) {
	software := fenceSoftware{LightRAGVersion: "1.5.7", LightRAGCommit: lightragCommit, LightRAGImageDigest: testFenceSHA('9'), MilvusVersion: milvusVersion, EtcdVersion: etcdVersion, MinioVersion: minioVersion}
	contract := fenceContract{Workspace: "xiaolanhe_v1", WorkingDirectoryIdentity: "deployment-volume-id", KVStorage: "JsonKVStorage", VectorStorage: "MilvusVectorDBStorage", GraphStorage: "NetworkXStorage", DocStatusStorage: "JsonDocStatusStorage", MilvusDatabase: "lightrag", EmbeddingContract: fenceEmbeddingContract{ProviderBinding: "openai-v1", EndpointSemantics: "dashscope-compatible-v1", Model: "text-embedding-v4", Dimension: 1024}, IndexType: "AUTOINDEX", MetricType: "COSINE", IDCanonicalizationVersion: fenceIDCanonicalization, FixtureSuiteVersion: "fixtures-v2"}
	const want = "sha256:eb80a7eb8fb4e539d920bf9fbfbdd4628e92b32ad1eb734eef122c8811824db4"
	if got := fenceContractDigest(software, contract); got != want {
		t.Fatalf("fenceContractDigest() = %s, want Python canonical digest %s", got, want)
	}
}

func TestExpectedFenceSchemaDigestsMatchPythonCanonicalGoldens(t *testing.T) {
	wants := map[string]string{
		"entities":      "sha256:c0aba0e7cbb6b0792646c9d7f0ce0a1f12eafe7d5cf1b83e73637f07c729bb23",
		"relationships": "sha256:76e276011ed87bf863ef57efed356a882839fc46abd24aff515ef92e26323724",
		"chunks":        "sha256:4c9aebff6589f3e1f28faa176f79a162dfea9aa65e862037faa449b781d2a7c5",
	}
	for name, want := range wants {
		collection := "xiaolanhe_v1_" + name + "_text_embedding_v4_1024d"
		if got := expectedFenceSchemaSHA256(name, collection); got != want {
			t.Errorf("expectedFenceSchemaSHA256(%q) = %s, want Python canonical digest %s", name, got, want)
		}
	}
}

func TestCanonicalJSONBytesMatchesPythonEscaping(t *testing.T) {
	got, err := canonicalJSONBytes(map[string]any{"literal": `\u2028`, "value": "研发<&>\u2028"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\"literal\":\"\\\\u2028\",\"value\":\"研发<&>\u2028\"}"
	if string(got) != want {
		t.Fatalf("canonicalJSONBytes() = %q, want %q", got, want)
	}
}

func TestVerifyFenceRejectsPinnedEvidenceDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, map[string]any)
	}{
		{name: "software commit", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "software")["lightrag_commit"] = strings.Repeat("0", 40)
		}},
		{name: "workspace", mutate: func(t *testing.T, report map[string]any) { nestedMap(t, report, "contract")["workspace"] = "other" }},
		{name: "working directory identity missing", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "contract")["working_directory_identity"] = ""
		}},
		{name: "working directory identity invalid", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "contract")["working_directory_identity"] = "volume/path"
		}},
		{name: "embedding model", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "contract", "embedding_contract")["model"] = "other"
		}},
		{name: "embedding dimension", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "contract", "embedding_contract")["dimension"] = 1536
		}},
		{name: "embedding prefix", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "contract", "embedding_contract")["query_prefix"] = "query: "
		}},
		{name: "writer not idle", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "writer_fence")["pipeline_idle_observed"] = false
		}},
		{name: "writer expired", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "writer_fence")["expires_at"] = "2026-09-07T12:00:30Z"
		}},
		{name: "writer issued after start", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "writer_fence")["issued_at"] = "2026-09-07T12:00:01Z"
		}},
		{name: "inapplicable backup populated", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "backup")["manifest_sha256"] = testFenceSHA('b')
		}},
		{name: "inapplicable writer backup manifest", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "writer_fence")["backup_manifest_sha256"] = testFenceSHA('b')
		}},
		{name: "source changed", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "source")["after_sha256"] = testFenceSHA('c')
		}},
		{name: "inapplicable rebuild nonempty", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "rebuild", "entities")["source_total"] = 1
		}},
		{name: "target collection", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "entities")["collection"] = "renamed"
		}},
		{name: "target database", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "relationships")["database"] = "default"
		}},
		{name: "target dynamic fields", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "chunks")["dynamic_fields"] = false
		}},
		{name: "target schema digest", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "chunks")["schema_sha256"] = "sha256:bad"
		}},
		{name: "target vector type", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "entities")["vector_field_type"] = "BINARY_VECTOR"
		}},
		{name: "target vector dimension", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "entities")["vector_dimension"] = 1536
		}},
		{name: "target primary key", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "relationships")["primary_key_field"] = "pk"
		}},
		{name: "target index", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "chunks")["index_type"] = "HNSW"
		}},
		{name: "target metric", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "chunks")["metric_type"] = "L2"
		}},
		{name: "target count", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "chunks")["actual_count"] = 1
		}},
		{name: "target id digest", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "targets", "chunks")["actual_ids_sha256"] = testFenceSHA('b')
		}},
		{name: "inapplicable duplicate policy populated", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "duplicate_policy")["reviewer"] = "reviewer"
		}},
		{name: "official consistency relation mismatch", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "official_consistency")["graph_relations"] = 1
		}},
		{name: "official consistency missing entity", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "official_consistency")["missing_entities"] = 1
		}},
		{name: "legacy inapplicable digest", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "legacy_import")["manifest_sha256"] = testFenceSHA('b')
		}},
		{name: "fixtures failed", mutate: func(t *testing.T, report map[string]any) { nestedMap(t, report, "fixtures")["failed"] = 1 }},
		{name: "worker cleanup", mutate: func(t *testing.T, report map[string]any) { nestedMap(t, report, "cleanup")["worker_finalized"] = false }},
		{name: "fresh verifier cleanup", mutate: func(t *testing.T, report map[string]any) {
			nestedMap(t, report, "cleanup")["verifier_finalized"] = false
		}},
		{name: "report reread cleanup", mutate: func(t *testing.T, report map[string]any) { nestedMap(t, report, "cleanup")["report_reread"] = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, "https://lightrag.example")
			marker, report := readFenceFixture(t, client)
			test.mutate(t, report)
			writeFenceFixture(t, client, marker, report, true, true)
			if err := client.verifyFence(); !errors.Is(err, entity.ErrContract) {
				t.Fatalf("verifyFence() error = %v, want %v", err, entity.ErrContract)
			}
		})
	}
}

func TestVerifyFenceValidatesOperationSpecificEvidence(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		prepare   func(*testing.T, map[string]any)
		wantError bool
	}{
		{name: "valid migration rebuild", operation: "migration_rebuild", prepare: func(t *testing.T, report map[string]any) { makeMigrationRebuildReport(t, report) }},
		{name: "valid restore verify", operation: "restore_verify", prepare: func(t *testing.T, report map[string]any) { makeBackupApplicable(t, report, "source-generation") }},
		{name: "valid empty bootstrap", operation: "bootstrap_empty", prepare: func(t *testing.T, report map[string]any) { makeBootstrapReport(t, report) }},
		{name: "rebuild missing backup", operation: "migration_rebuild", prepare: func(t *testing.T, report map[string]any) {
			makeMigrationRebuildReport(t, report)
			backup := nestedMap(t, report, "backup")
			backup["applicable"], backup["status"] = false, "not_applicable"
			clearBackup(backup)
			nestedMap(t, report, "writer_fence")["backup_manifest_sha256"] = nil
		}, wantError: true},
		{name: "restore source generation reused", operation: "restore_verify", prepare: func(t *testing.T, report map[string]any) { makeBackupApplicable(t, report, "generation-1") }, wantError: true},
		{name: "backup manifest mismatch", operation: "restore_verify", prepare: func(t *testing.T, report map[string]any) {
			makeBackupApplicable(t, report, "source-generation")
			nestedMap(t, report, "writer_fence")["backup_manifest_sha256"] = testFenceSHA('a')
		}, wantError: true},
		{name: "bootstrap target nonempty", operation: "bootstrap_empty", prepare: func(t *testing.T, report map[string]any) {
			makeBootstrapReport(t, report)
			target := nestedMap(t, report, "targets", "chunks")
			target["expected_count"], target["actual_count"] = 1, 1
		}, wantError: true},
		{name: "bootstrap source nonempty", operation: "bootstrap_empty", prepare: func(t *testing.T, report map[string]any) {
			makeBootstrapReport(t, report)
			nestedMap(t, report, "source")["text_chunks"] = 1
		}, wantError: true},
		{name: "rebuild stats mismatch", operation: "migration_rebuild", prepare: func(t *testing.T, report map[string]any) {
			makeMigrationRebuildReport(t, report)
			nestedMap(t, report, "rebuild", "entities")["rebuilt"] = 0
		}, wantError: true},
		{name: "rebuild source count mismatch", operation: "migration_rebuild", prepare: func(t *testing.T, report map[string]any) {
			makeMigrationRebuildReport(t, report)
			nestedMap(t, report, "source")["text_chunks"] = 2
		}, wantError: true},
		{name: "duplicate without policy", operation: "migration_rebuild", prepare: func(t *testing.T, report map[string]any) {
			makeMigrationRebuildReport(t, report)
			stats := nestedMap(t, report, "rebuild", "relationships")
			stats["source_total"], stats["duplicates"] = 2, 1
			nestedMap(t, report, "source")["graph_edges_raw"] = 2
		}, wantError: true},
		{name: "duplicate totals cannot overflow policy gate", operation: "migration_rebuild", prepare: func(t *testing.T, report map[string]any) {
			makeMigrationRebuildReport(t, report)
			maximum := int(^uint(0) >> 1)
			for _, name := range []string{"entities", "relationships"} {
				stats := nestedMap(t, report, "rebuild", name)
				stats["source_total"], stats["prepared"], stats["rebuilt"], stats["duplicates"] = maximum, 0, 0, maximum
				target := nestedMap(t, report, "targets", name)
				target["expected_count"], target["actual_count"] = 0, 0
			}
			source := nestedMap(t, report, "source")
			source["graph_nodes"], source["graph_edges_raw"], source["relationships_normalized"] = maximum, maximum, 0
			consistency := nestedMap(t, report, "official_consistency")
			consistency["graph_entities"], consistency["graph_relations"] = 0, 0
		}, wantError: true},
		{name: "valid reviewed duplicate policy", operation: "migration_rebuild", prepare: func(t *testing.T, report map[string]any) {
			makeMigrationRebuildReport(t, report)
			stats := nestedMap(t, report, "rebuild", "relationships")
			stats["source_total"], stats["duplicates"] = 2, 1
			nestedMap(t, report, "source")["graph_edges_raw"] = 2
			policy := nestedMap(t, report, "duplicate_policy")
			policy["applicable"], policy["status"], policy["evidence_sha256"], policy["reviewer"], policy["expires_at"] = true, "succeeded", testFenceSHA('d'), "reviewer", "2026-09-07T13:00:00Z"
			nestedMap(t, report, "duplicate_policy", "max_duplicates")["relationships"] = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, "https://lightrag.example")
			marker, report := readFenceFixture(t, client)
			marker["operation"], report["operation"] = test.operation, test.operation
			test.prepare(t, report)
			writeFenceFixture(t, client, marker, report, true, true)
			err := client.verifyFence()
			if test.wantError && !errors.Is(err, entity.ErrContract) {
				t.Fatalf("verifyFence() error = %v, want %v", err, entity.ErrContract)
			}
			if !test.wantError && err != nil {
				t.Fatalf("verifyFence() error = %v", err)
			}
		})
	}
}

func TestVerifyFenceRejectsOversizedReport(t *testing.T) {
	client := testClient(t, "https://lightrag.example")
	reportPath := filepath.Join(client.fencePath, "reports", "attempt-1.json")
	if err := os.WriteFile(reportPath, bytes.Repeat([]byte("x"), maxFenceBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyFence(); !errors.Is(err, entity.ErrContract) {
		t.Fatalf("err=%v, want %v", err, entity.ErrContract)
	}
}

func TestVerifyFenceRejectsUnsafeReportFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace func(*testing.T, string)
	}{
		{
			name: "symbolic link",
			replace: func(t *testing.T, reportPath string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "report.json")
				if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(reportPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, reportPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			replace: func(t *testing.T, reportPath string) {
				t.Helper()
				if err := os.Remove(reportPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(reportPath, 0700); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, "https://lightrag.example")
			reportPath := filepath.Join(client.fencePath, "reports", "attempt-1.json")
			test.replace(t, reportPath)
			if err := client.verifyFence(); !errors.Is(err, entity.ErrContract) {
				t.Fatalf("err=%v, want %v", err, entity.ErrContract)
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
	base := Config{BaseURL: "https://lightrag.example", APIKey: testAPIKey, Workspace: "xiaolanhe_v1", WorkingDirectory: "/app/data/rag_storage", CoreVersion: "1.5.7", APIVersion: "0344", FencePath: t.TempDir(), FenceGeneration: "generation-1", FenceContractSHA256: "sha256:" + strings.Repeat("a", 64), Timeout: time.Second}
	for name, mutate := range map[string]func(*Config){
		"short key":          func(c *Config) { c.APIKey = "secret" },
		"newline key":        func(c *Config) { c.APIKey = testAPIKey + "\nforged" },
		"workspace":          func(c *Config) { c.Workspace = "../other" },
		"unpinned workspace": func(c *Config) { c.Workspace = "other" },
		"directory":          func(c *Config) { c.WorkingDirectory = "/app/data/../other" },
		"unpinned directory": func(c *Config) { c.WorkingDirectory = "/app/data/other" },
		"core version":       func(c *Config) { c.CoreVersion = "latest" },
		"api version":        func(c *Config) { c.APIVersion = "next" },
		"fence path":         func(c *Config) { c.FencePath = "relative" },
		"generation":         func(c *Config) { c.FenceGeneration = "" },
		"contract hash":      func(c *Config) { c.FenceContractSHA256 = "sha256:bad" },
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

func TestClientRequiresExplicitHTTPOptIn(t *testing.T) {
	base := Config{BaseURL: "http://lightrag.example", APIKey: testAPIKey, Workspace: "xiaolanhe_v1", WorkingDirectory: "/app/data/rag_storage", CoreVersion: "1.5.7", APIVersion: "0344", FencePath: t.TempDir(), FenceGeneration: "generation-1", FenceContractSHA256: "sha256:" + strings.Repeat("a", 64), Timeout: time.Second}
	if _, err := NewClient(base); err == nil || err.Error() != "LightRAG base URL must use https unless insecure transport is explicitly allowed" {
		t.Fatalf("err=%v", err)
	}
	base.AllowInsecure = true
	if _, err := NewClient(base); err != nil {
		t.Fatalf("explicit HTTP opt-in rejected: %v", err)
	}
	base.BaseURL = "https://lightrag.example"
	base.AllowInsecure = false
	if _, err := NewClient(base); err != nil {
		t.Fatalf("HTTPS rejected without insecure opt-in: %v", err)
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
	client := testClient(t, server.URL)
	client.apiKey = keyCanary
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
	fencePath := t.TempDir()
	reportsPath := filepath.Join(fencePath, "reports")
	if err := os.Mkdir(reportsPath, 0700); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{BaseURL: baseURL, APIKey: testAPIKey, Workspace: "xiaolanhe_v1", WorkingDirectory: "/app/data/rag_storage", CoreVersion: "1.5.7", APIVersion: "0344", FencePath: fencePath, FenceGeneration: "generation-1", FenceContractSHA256: testFenceSHA('a'), AllowInsecure: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	marker, report := validFenceFixture(client)
	writeFenceFixture(t, client, marker, report, true, true)
	return client
}

func validFenceFixture(client *Client) (map[string]any, map[string]any) {
	shaB, shaC, imageDigest := testFenceSHA('b'), testFenceSHA('c'), testFenceSHA('9')
	nullStats := func(name string) map[string]any {
		return map[string]any{"label": name, "source_total": 0, "prepared": 0, "rebuilt": 0, "staged": 0, "skipped": 0, "duplicates": 0, "batches": 0, "failed_batches": 0, "errors": []any{}}
	}
	target := func(name string) map[string]any {
		collection := client.workspace + "_" + name + "_text_embedding_v4_1024d"
		return map[string]any{"collection": collection, "database": "lightrag", "dynamic_fields": true, "schema_sha256": expectedFenceSchemaSHA256(name, collection), "vector_field_type": "FLOAT_VECTOR", "vector_dimension": 1024, "primary_key_field": "id", "index_type": "AUTOINDEX", "metric_type": "COSINE", "expected_count": 0, "actual_count": 0, "expected_ids_sha256": shaC, "actual_ids_sha256": shaC}
	}
	report := map[string]any{
		"schema_version": 2, "attempt_id": "attempt-1", "generation": client.fenceGeneration, "operation": "revalidate", "state": "verified", "reason_code": "verified",
		"started_at": "2026-09-07T12:00:00Z", "finished_at": "2026-09-07T12:01:00Z",
		"software":             map[string]any{"lightrag_version": "1.5.7", "lightrag_commit": lightragCommit, "lightrag_image_digest": imageDigest, "milvus_version": milvusVersion, "etcd_version": etcdVersion, "minio_version": minioVersion},
		"contract":             map[string]any{"contract_sha256": "", "workspace": client.workspace, "working_directory_identity": "deployment-volume-id", "kv_storage": "JsonKVStorage", "vector_storage": "MilvusVectorDBStorage", "graph_storage": "NetworkXStorage", "doc_status_storage": "JsonDocStatusStorage", "milvus_database": "lightrag", "embedding_contract": map[string]any{"provider_binding": "openai-v1", "endpoint_semantics": "dashscope-compatible-v1", "model": "text-embedding-v4", "dimension": 1024, "send_dimension": false, "asymmetric": false, "document_prefix": nil, "query_prefix": nil}, "index_type": "AUTOINDEX", "metric_type": "COSINE", "id_canonicalization_version": fenceIDCanonicalization, "fixture_suite_version": "fixtures-v2", "legacy_manifest_sha256": nil},
		"writer_fence":         map[string]any{"applicable": true, "status": "succeeded", "evidence_type": "xlh.lightrag_writer_fence.v2", "evidence_sha256": shaB, "issued_at": "2026-09-07T11:59:00Z", "expires_at": "2026-09-07T13:00:00Z", "pipeline_idle_observed": true, "server_replicas": 0, "automatic_restart_disabled": true, "observer": "deployment-controller", "uncontrolled_writers_attestation": "no_uncontrolled_writers", "backup_manifest_sha256": nil},
		"backup":               map[string]any{"applicable": false, "status": "not_applicable", "evidence_type": "xlh.lightrag_backup.v2", "evidence_sha256": nil, "manifest_sha256": nil, "source_generation": nil, "configuration_sha256": nil, "writer_evidence_sha256": nil, "component_count": 0},
		"source":               map[string]any{"applicable": true, "status": "succeeded", "before_sha256": shaB, "after_sha256": shaB, "graph_nodes": 0, "graph_edges_raw": 0, "relationships_normalized": 0, "text_chunks": 0},
		"rebuild":              map[string]any{"applicable": false, "status": "not_applicable", "entities": nullStats("entities"), "relationships": nullStats("relationships"), "chunks": nullStats("chunks")},
		"targets":              map[string]any{"applicable": true, "status": "succeeded", "entities": target("entities"), "relationships": target("relationships"), "chunks": target("chunks")},
		"duplicate_policy":     map[string]any{"applicable": false, "status": "not_applicable", "evidence_sha256": nil, "reviewer": nil, "expires_at": nil, "max_duplicates": map[string]any{"entities": 0, "relationships": 0, "chunks": 0}},
		"official_consistency": map[string]any{"applicable": true, "status": "succeeded", "graph_entities": 0, "graph_relations": 0, "missing_entities": 0, "missing_relations": 0, "skipped_nodes": 0, "skipped_edges": 0},
		"legacy_import":        map[string]any{"applicable": false, "status": "not_applicable", "required": false, "manifest_sha256": nil, "continuous_success_watermark": 0, "source_rows": 0, "accepted_rows": 0, "terminal_rows": 0, "reconciled": false, "retirement_eligible": false},
		"checks":               map[string]any{"source_unchanged": true, "stats_valid": true, "schema_valid": true, "exact_id_sets_valid": true, "graph_consistency_valid": true, "fixtures_valid": true, "legacy_import_valid": true, "cleanup_valid": true},
		"fixtures":             map[string]any{"applicable": true, "status": "succeeded", "suite_version": "fixtures-v2", "passed": 4, "failed": 0, "results_sha256": shaC},
		"cleanup":              map[string]any{"applicable": true, "status": "succeeded", "worker_finalized": true, "verifier_finalized": true, "report_reread": true},
		"errors":               []any{},
	}
	software := fenceSoftware{LightRAGVersion: "1.5.7", LightRAGCommit: lightragCommit, LightRAGImageDigest: imageDigest, MilvusVersion: milvusVersion, EtcdVersion: etcdVersion, MinioVersion: minioVersion}
	contract := fenceContract{Workspace: client.workspace, WorkingDirectoryIdentity: "deployment-volume-id", KVStorage: "JsonKVStorage", VectorStorage: "MilvusVectorDBStorage", GraphStorage: "NetworkXStorage", DocStatusStorage: "JsonDocStatusStorage", MilvusDatabase: "lightrag", EmbeddingContract: fenceEmbeddingContract{ProviderBinding: "openai-v1", EndpointSemantics: "dashscope-compatible-v1", Model: "text-embedding-v4", Dimension: 1024}, IndexType: "AUTOINDEX", MetricType: "COSINE", IDCanonicalizationVersion: fenceIDCanonicalization, FixtureSuiteVersion: "fixtures-v2"}
	contractHash := fenceContractDigest(software, contract)
	report["contract"].(map[string]any)["contract_sha256"] = contractHash
	client.fenceContractSHA256 = contractHash
	marker := map[string]any{"schema_version": 2, "state": "verified", "generation": client.fenceGeneration, "attempt_id": "attempt-1", "operation": "revalidate", "contract_sha256": contractHash, "report_path": "reports/attempt-1.json", "report_sha256": "", "updated_at": "2026-09-07T12:01:01Z", "reason_code": "verified"}
	return marker, report
}

func testFenceSHA(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

func readFenceFixture(t *testing.T, client *Client) (map[string]any, map[string]any) {
	t.Helper()
	read := func(name string) map[string]any {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	marker := read(filepath.Join(client.fencePath, "current.json"))
	report := read(filepath.Join(client.fencePath, "reports", "attempt-1.json"))
	return marker, report
}

func writeFenceFixture(t *testing.T, client *Client, marker, report map[string]any, markerNewline, reportNewline bool) {
	t.Helper()
	var markerSuffix, reportSuffix []byte
	if markerNewline {
		markerSuffix = []byte{'\n'}
	}
	if reportNewline {
		reportSuffix = []byte{'\n'}
	}
	writeFenceFixtureWithEncoding(t, client, marker, report, false, false, markerSuffix, reportSuffix)
}

func writeFenceFixtureWithEncoding(t *testing.T, client *Client, marker, report map[string]any, prettyMarker, prettyReport bool, markerSuffix, reportSuffix []byte) {
	t.Helper()
	marshal := func(value map[string]any, pretty bool) []byte {
		var data []byte
		var err error
		if pretty {
			data, err = json.MarshalIndent(value, "", "  ")
		} else {
			data, err = json.Marshal(value)
		}
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	canonicalReportBytes := marshal(report, false)
	reportBytes := append(marshal(report, prettyReport), reportSuffix...)
	if marker["report_sha256"] != nil {
		marker["report_sha256"] = fmt.Sprintf("sha256:%x", sha256.Sum256(canonicalReportBytes))
	}
	markerBytes := append(marshal(marker, prettyMarker), markerSuffix...)
	if err := os.WriteFile(filepath.Join(client.fencePath, "reports", "attempt-1.json"), reportBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.fencePath, "current.json"), markerBytes, 0600); err != nil {
		t.Fatal(err)
	}
}

func nestedMap(t *testing.T, value map[string]any, path ...string) map[string]any {
	t.Helper()
	current := value
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("%s is %T, want map[string]any", key, current[key])
		}
		current = next
	}
	return current
}

func makeMigrationRebuildReport(t *testing.T, report map[string]any) {
	t.Helper()
	makeBackupApplicable(t, report, "source-generation")
	rebuild := nestedMap(t, report, "rebuild")
	rebuild["applicable"], rebuild["status"] = true, "succeeded"
	for _, target := range []string{"entities", "relationships", "chunks"} {
		stats := nestedMap(t, report, "rebuild", target)
		stats["label"], stats["source_total"], stats["prepared"], stats["rebuilt"], stats["batches"] = target, 1, 1, 1, 1
		evidence := nestedMap(t, report, "targets", target)
		evidence["expected_count"], evidence["actual_count"] = 1, 1
	}
	source := nestedMap(t, report, "source")
	source["graph_nodes"], source["graph_edges_raw"], source["relationships_normalized"], source["text_chunks"] = 1, 1, 1, 1
	consistency := nestedMap(t, report, "official_consistency")
	consistency["graph_entities"], consistency["graph_relations"] = 1, 1
}

func makeBackupApplicable(t *testing.T, report map[string]any, sourceGeneration string) {
	t.Helper()
	backup := nestedMap(t, report, "backup")
	backup["applicable"], backup["status"] = true, "succeeded"
	backup["evidence_type"], backup["evidence_sha256"], backup["manifest_sha256"] = "xlh.lightrag_backup.v2", testFenceSHA('d'), testFenceSHA('e')
	backup["source_generation"], backup["configuration_sha256"] = sourceGeneration, nestedMap(t, report, "contract")["contract_sha256"]
	backup["writer_evidence_sha256"], backup["component_count"] = testFenceSHA('f'), 4
	nestedMap(t, report, "writer_fence")["backup_manifest_sha256"] = backup["manifest_sha256"]
}

func clearBackup(backup map[string]any) {
	for _, key := range []string{"evidence_sha256", "manifest_sha256", "source_generation", "configuration_sha256", "writer_evidence_sha256"} {
		backup[key] = nil
	}
	backup["evidence_type"] = "xlh.lightrag_backup.v2"
	backup["component_count"] = 0
}

func makeBootstrapReport(t *testing.T, report map[string]any) {
	t.Helper()
	fixtures := nestedMap(t, report, "fixtures")
	fixtures["applicable"], fixtures["status"], fixtures["suite_version"], fixtures["passed"], fixtures["failed"], fixtures["results_sha256"] = false, "not_applicable", nil, 0, 0, nil
}

func makeFailedReport(t *testing.T, report map[string]any, reason string) {
	t.Helper()
	report["state"], report["reason_code"] = "failed", reason
	writer := nestedMap(t, report, "writer_fence")
	writer["applicable"], writer["status"] = true, "not_run"
	writer["evidence_sha256"], writer["issued_at"], writer["expires_at"] = nil, nil, nil
	writer["pipeline_idle_observed"], writer["server_replicas"], writer["automatic_restart_disabled"] = false, 0, false
	writer["observer"], writer["uncontrolled_writers_attestation"], writer["backup_manifest_sha256"] = nil, nil, nil

	operation := report["operation"].(string)
	backup := nestedMap(t, report, "backup")
	backupApplicable := operation == "migration_rebuild" || operation == "restore_verify"
	backup["applicable"] = backupApplicable
	if backupApplicable {
		backup["status"] = "not_run"
	} else {
		backup["status"] = "not_applicable"
	}
	clearBackup(backup)

	source := nestedMap(t, report, "source")
	source["applicable"], source["status"] = true, "not_run"
	source["before_sha256"], source["after_sha256"] = nil, nil
	source["graph_nodes"], source["graph_edges_raw"], source["relationships_normalized"], source["text_chunks"] = 0, 0, 0, 0

	rebuild := nestedMap(t, report, "rebuild")
	rebuildApplicable := operation == "migration_rebuild"
	rebuild["applicable"] = rebuildApplicable
	if rebuildApplicable {
		rebuild["status"] = "not_run"
	} else {
		rebuild["status"] = "not_applicable"
	}
	for _, name := range []string{"entities", "relationships", "chunks"} {
		stats := nestedMap(t, report, "rebuild", name)
		stats["label"] = name
		for _, field := range []string{"source_total", "prepared", "rebuilt", "staged", "skipped", "duplicates", "batches", "failed_batches"} {
			stats[field] = 0
		}
		stats["errors"] = []any{}

		target := nestedMap(t, report, "targets", name)
		for _, field := range []string{"collection", "database", "dynamic_fields", "schema_sha256", "vector_field_type", "primary_key_field", "index_type", "metric_type", "expected_ids_sha256", "actual_ids_sha256"} {
			target[field] = nil
		}
		target["vector_dimension"], target["expected_count"], target["actual_count"] = 0, 0, 0
	}
	targets := nestedMap(t, report, "targets")
	targets["applicable"], targets["status"] = true, "not_run"

	duplicate := nestedMap(t, report, "duplicate_policy")
	duplicate["applicable"], duplicate["status"] = false, "not_applicable"
	duplicate["evidence_sha256"], duplicate["reviewer"], duplicate["expires_at"] = nil, nil, nil
	maximum := nestedMap(t, report, "duplicate_policy", "max_duplicates")
	maximum["entities"], maximum["relationships"], maximum["chunks"] = 0, 0, 0

	official := nestedMap(t, report, "official_consistency")
	official["applicable"], official["status"] = true, "not_run"
	for _, field := range []string{"graph_entities", "graph_relations", "missing_entities", "missing_relations", "skipped_nodes", "skipped_edges"} {
		official[field] = 0
	}

	legacy := nestedMap(t, report, "legacy_import")
	legacy["applicable"], legacy["status"], legacy["required"] = false, "not_applicable", false
	legacy["manifest_sha256"] = nil
	legacy["continuous_success_watermark"], legacy["source_rows"], legacy["accepted_rows"], legacy["terminal_rows"] = 0, 0, 0, 0
	legacy["reconciled"], legacy["retirement_eligible"] = false, false

	checks := nestedMap(t, report, "checks")
	for key := range checks {
		checks[key] = false
	}
	fixtures := nestedMap(t, report, "fixtures")
	fixturesApplicable := operation != "bootstrap_empty"
	fixtures["applicable"] = fixturesApplicable
	if fixturesApplicable {
		fixtures["status"] = "not_run"
	} else {
		fixtures["status"] = "not_applicable"
	}
	fixtures["suite_version"], fixtures["passed"], fixtures["failed"], fixtures["results_sha256"] = nil, 0, 0, nil
	cleanup := nestedMap(t, report, "cleanup")
	cleanup["applicable"], cleanup["status"] = true, "not_run"
	cleanup["worker_finalized"], cleanup["verifier_finalized"], cleanup["report_reread"] = false, false, false
	errorCode, retryable := "unknown", false
	if reason == "abandoned_rebuilding" {
		errorCode, retryable = "abandoned_rebuilding", true
	}
	report["errors"] = []any{map[string]any{"stage": "controller", "target": "all", "code": errorCode, "retryable": retryable}}
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
		"configuration": map[string]string{"workspace": "xiaolanhe_v1", "kv_storage": "JsonKVStorage", "vector_storage": "MilvusVectorDBStorage", "graph_storage": "NetworkXStorage", "doc_status_storage": "JsonDocStatusStorage"},
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
func withConfigurationValue(key string, value string) map[string]any {
	payload := healthyPayload()
	configuration := payload["configuration"].(map[string]string)
	configuration[key] = value
	return payload
}
func paginatedPayload(page int, documents []any, total int) map[string]any {
	totalPages := 0
	if total > 0 {
		totalPages = (total + upstreamPageSize - 1) / upstreamPageSize
	}
	return map[string]any{"documents": documents, "pagination": map[string]any{"page": page, "page_size": upstreamPageSize, "total_count": total, "total_pages": totalPages, "has_next": page < totalPages, "has_prev": page > 1}, "status_counts": map[string]int{"PROCESSED": total}}
}
