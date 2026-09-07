package metrics

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryExportsBoundedPrometheusMetrics(t *testing.T) {
	registry := NewRegistry()
	const canary = "CANARY_PRIVATE_PROMPT_SESSION_KEY"
	registry.ObserveAssistant(AssistantObservation{
		Event: canary, Role: canary, Operation: canary, Outcome: canary, StopReason: canary, Route: canary, Skill: canary,
		Duration: 125 * time.Millisecond, ModelCalls: 2, ToolCalls: 3, Delegations: 1,
	})
	registry.ObserveModel(ModelObservation{Operation: "generate", Outcome: "ok", UsageReported: true, Duration: 50 * time.Millisecond, PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18})
	registry.ObserveLightRAGRequest("query", "ok", 80*time.Millisecond)
	registry.ObserveLightRAGQuery("mix", "ok", 90*time.Millisecond)
	registry.SetLightRAGHealth(true, false, false)
	registry.SetLightRAGDocumentStatuses(map[string]int{"PROCESSED": 4, "FAILED": 1})
	registry.ObserveMemory(MemoryObservation{Outcome: "updated", ErrorClass: "none", Duration: 20 * time.Millisecond, WorkingSetRunes: 12_500, MessageCount: 9})
	registry.ObserveFlashSale("consume", "success", 12*time.Millisecond, 2, 35*time.Second)
	registry.ObserveFlashSale(canary, canary, 0, 0, 0)

	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_assistant_operations_total{event="unknown",agent_role="unknown",operation="unknown",outcome="unknown",stop_reason="unknown",route="unknown",skill="unknown"} 1`,
		`xiaolanhe_model_tokens_total{kind="total"} 18`,
		`xiaolanhe_lightrag_queries_total{mode="mix",outcome="ok"} 1`,
		`xiaolanhe_lightrag_storage_contract_healthy 1`,
		`xiaolanhe_lightrag_documents{status="processed"} 4`,
		`xiaolanhe_assistant_memory_working_set_runes_bucket{le="20000"} 1`,
		`xiaolanhe_flash_sale_operations_total{operation="consume",outcome="success"} 1`,
		`xiaolanhe_flash_sale_items_total{operation="consume",outcome="success"} 2`,
		`xiaolanhe_flash_sale_pending_age_seconds_bucket{stage="consume",le="60"} 1`,
		`xiaolanhe_flash_sale_operations_total{operation="unknown",outcome="unknown"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
	if strings.Contains(output, canary) {
		t.Fatalf("unbounded value leaked to metrics: %s", output)
	}
}

func TestRegistryIsConcurrentAndCardinalityBounded(t *testing.T) {
	registry := NewRegistry()
	var wait sync.WaitGroup
	for index := range 200 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			registry.ObserveAssistant(AssistantObservation{Event: "assistant.tool", Role: "research", Operation: fmt.Sprintf("user-%d", index), Outcome: "ok", StopReason: "complete", Route: "research", Skill: "research_guide"})
		}()
	}
	wait.Wait()
	output := string(registry.Prometheus())
	if strings.Count(output, "xiaolanhe_assistant_operations_total{") != 1 || !strings.Contains(output, `operation="unknown"`) || !strings.Contains(output, "} 200\n") {
		t.Fatalf("unexpected metric cardinality:\n%s", output)
	}
}

func TestRegistryExportsBoundedMySQLAndLightRAGMigrationMetrics(t *testing.T) {
	registry := NewRegistry()
	const canary = "CANARY_DSN_KEY_CONTENT_USER_ID"
	registry.ObserveMySQLOperation(MySQLOperationObservation{Operation: canary, Outcome: canary, Duration: -time.Second})
	registry.ObserveMySQLRetry(MySQLRetryObservation{Reason: canary, Outcome: canary})
	registry.SetMySQLMigrations(-1, 2, 1)
	registry.ObserveMySQLCutover(MySQLCutoverObservation{
		Operation: canary, Outcome: canary, Duration: -time.Second,
		HasReconciliation: true, SourceRows: -1, TargetRows: 8, MismatchCount: 2,
	})
	registry.ObserveLightRAGFence(LightRAGFenceObservation{State: canary, Operation: canary, Outcome: canary, Reason: canary})
	registry.SetLightRAGRebuild(LightRAGRebuildObservation{
		Operation: canary, Outcome: canary, Reason: canary, Duration: -time.Second,
		Targets: []LightRAGRebuildTarget{{Target: canary, SourceTotal: -1, Prepared: 2, Rebuilt: 1, Skipped: -2, Duplicates: 3, FailedBatches: 4}},
	})
	registry.SetLightRAGBackend(true, false, true)

	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_mysql_operations_total{operation="unknown",outcome="unknown"} 1`,
		`xiaolanhe_mysql_operation_duration_seconds_sum{operation="unknown",outcome="unknown"} 0`,
		`# HELP xiaolanhe_mysql_transaction_retry_events_total MySQL retryable transaction failure decisions by reason and outcome.`,
		`xiaolanhe_mysql_transaction_retry_events_total{reason="unknown",outcome="unknown"} 1`,
		`xiaolanhe_mysql_migrations{state="applied"} 0`,
		`xiaolanhe_mysql_migrations{state="pending"} 2`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="source"} 0`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="target"} 8`,
		`xiaolanhe_mysql_cutover_last_reconciliation_mismatches 2`,
		`xiaolanhe_lightrag_fence_observations_total{state="unknown",operation="unknown",outcome="unknown",reason="unknown"} 1`,
		`xiaolanhe_lightrag_fence_state{state="unknown"} 1`,
		`xiaolanhe_lightrag_rebuild_last{operation="unknown",outcome="unknown",reason="unknown",target="unknown",stat="source_total"} 0`,
		`xiaolanhe_lightrag_rebuild_last{operation="unknown",outcome="unknown",reason="unknown",target="unknown",stat="prepared"} 2`,
		`xiaolanhe_lightrag_rebuild_last_duration_seconds{operation="unknown",outcome="unknown",reason="unknown"} 0`,
		`xiaolanhe_lightrag_backend_expected 1`,
		`xiaolanhe_lightrag_service_healthy 0`,
		`xiaolanhe_lightrag_generation_match 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
	if strings.Contains(output, canary) {
		t.Fatalf("unbounded value leaked to metrics: %s", output)
	}
	if strings.Contains(output, "xiaolanhe_mysql_transaction_retries_total") {
		t.Fatalf("legacy retry metric name is still exported: %s", output)
	}
}

func TestRegistryLatestSeriesAreReplacedAsOneSnapshot(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveLightRAGFence(LightRAGFenceObservation{State: "verified", Operation: "revalidate", Outcome: "verified", Reason: "verified"})
	registry.ObserveLightRAGFence(LightRAGFenceObservation{State: "failed", Operation: "migration_rebuild", Outcome: "error", Reason: "operation_failed"})
	registry.SetLightRAGRebuild(LightRAGRebuildObservation{
		Operation: "migration_rebuild", Outcome: "verified", Reason: "verified", Duration: 5 * time.Second,
		Targets: []LightRAGRebuildTarget{{Target: "entities", Rebuilt: 11}, {Target: "relationships", Rebuilt: 12}},
	})
	registry.SetLightRAGRebuild(LightRAGRebuildObservation{
		Operation: "restore_verify", Outcome: "rejected", Reason: "report_invalid", Duration: 2 * time.Second,
		Targets: []LightRAGRebuildTarget{{Target: "chunks", Rebuilt: 7}},
	})
	registry.SetMySQLMigrations(20, 3, 1)
	registry.SetMySQLMigrations(24, 0, 0)
	registry.ObserveMySQLCutover(MySQLCutoverObservation{Operation: "copy", Outcome: "rejected", HasReconciliation: true, SourceRows: 10, TargetRows: 9, MismatchCount: 1})
	registry.ObserveMySQLCutover(MySQLCutoverObservation{Operation: "verify", Outcome: "success", HasReconciliation: true, SourceRows: 10, TargetRows: 10, Ready: true})

	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_lightrag_fence_state{state="failed"} 1`,
		`xiaolanhe_lightrag_fence_state{state="verified"} 0`,
		`xiaolanhe_lightrag_rebuild_last{operation="restore_verify",outcome="rejected",reason="report_invalid",target="chunks",stat="rebuilt"} 7`,
		`xiaolanhe_mysql_migrations{state="applied"} 24`,
		`xiaolanhe_mysql_migrations{state="pending"} 0`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="source"} 10`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="target"} 10`,
		`xiaolanhe_mysql_cutover_last_reconciliation_mismatches 0`,
		`xiaolanhe_mysql_cutover_last_reconciliation_ready 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
	for _, stale := range []string{`target="entities"`, `target="relationships"`, `} 20\n`, `} 3\n`} {
		if strings.Contains(output, stale) {
			t.Fatalf("stale latest-snapshot series %q was retained:\n%s", stale, output)
		}
	}
}

func TestRegistryCutoverRunsWithoutReconciliationPreserveVerifiedSnapshot(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveMySQLCutover(MySQLCutoverObservation{
		Operation: "verify", Outcome: "success", Duration: 3 * time.Second, HasReconciliation: true,
		SourceRows: 10, TargetRows: 10, Ready: true,
	})
	registry.ObserveMySQLCutover(MySQLCutoverObservation{
		Operation: "inspect", Outcome: "success", Duration: 2 * time.Second,
		SourceRows: 101, TargetRows: 102, MismatchCount: 103,
	})
	registry.ObserveMySQLCutover(MySQLCutoverObservation{
		Operation: "copy", Outcome: "error", Duration: time.Second,
		SourceRows: 201, TargetRows: 202, MismatchCount: 203,
	})

	output := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_mysql_cutover_runs_total{operation="verify",outcome="success"} 1`,
		`xiaolanhe_mysql_cutover_runs_total{operation="inspect",outcome="success"} 1`,
		`xiaolanhe_mysql_cutover_runs_total{operation="copy",outcome="error"} 1`,
		`xiaolanhe_mysql_cutover_run_duration_seconds_count{operation="verify",outcome="success"} 1`,
		`xiaolanhe_mysql_cutover_run_duration_seconds_count{operation="inspect",outcome="success"} 1`,
		`xiaolanhe_mysql_cutover_run_duration_seconds_count{operation="copy",outcome="error"} 1`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="source"} 10`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="target"} 10`,
		`xiaolanhe_mysql_cutover_last_reconciliation_mismatches 0`,
		`xiaolanhe_mysql_cutover_last_reconciliation_ready 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, output)
		}
	}
	for _, stale := range []string{
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="source"} 101`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="target"} 102`,
		`xiaolanhe_mysql_cutover_last_reconciliation_mismatches 103`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="source"} 201`,
		`xiaolanhe_mysql_cutover_last_reconciliation_rows{side="target"} 202`,
		`xiaolanhe_mysql_cutover_last_reconciliation_mismatches 203`,
	} {
		if strings.Contains(output, stale) {
			t.Fatalf("run without reconciliation overwrote verified snapshot with %q:\n%s", stale, output)
		}
	}
}

func TestRegistryMySQLPoolUsesAbsoluteSnapshotsAndSurvivesPanic(t *testing.T) {
	registry := NewRegistry()
	var scrape int
	registry.BindMySQLPool(func() MySQLPoolObservation {
		scrape++
		// Calling back into the registry proves the collector callback is not
		// running while the registry lock is held.
		registry.SetMySQLMigrations(scrape, 0, 0)
		if scrape == 3 {
			panic("pool unavailable")
		}
		return MySQLPoolObservation{
			MaxOpenConnections: 20, OpenConnections: int64(scrape + 1), InUse: 1, Idle: int64(scrape),
			WaitCount: int64(scrape * 7), WaitDuration: time.Duration(scrape) * time.Second, MaxIdleClosed: int64(scrape * 2),
		}
	})

	first := string(registry.Prometheus())
	second := string(registry.Prometheus())
	third := string(registry.Prometheus())
	for _, expected := range []string{
		`xiaolanhe_mysql_pool_open_connections 3`,
		`xiaolanhe_mysql_pool_wait_count_total 14`,
		`xiaolanhe_mysql_pool_wait_duration_seconds_total 2`,
	} {
		if !strings.Contains(second, expected) {
			t.Fatalf("second scrape missing absolute value %q:\n%s", expected, second)
		}
		if !strings.Contains(third, expected) {
			t.Fatalf("panic scrape did not retain prior snapshot %q:\n%s", expected, third)
		}
	}
	if !strings.Contains(first, `xiaolanhe_mysql_pool_wait_count_total 7`) {
		t.Fatalf("first scrape missing initial absolute value:\n%s", first)
	}
	if strings.Contains(second, `xiaolanhe_mysql_pool_wait_count_total 21`) {
		t.Fatalf("pool counter was accumulated between scrapes:\n%s", second)
	}
}

func TestRegistryConcurrentPoolBindingAndScraping(t *testing.T) {
	registry := NewRegistry()
	var value atomic.Int64
	registry.BindMySQLPool(func() MySQLPoolObservation {
		return MySQLPoolObservation{OpenConnections: value.Add(1)}
	})

	var wait sync.WaitGroup
	for index := range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if index%5 == 0 {
				registry.BindMySQLPool(func() MySQLPoolObservation {
					return MySQLPoolObservation{OpenConnections: value.Add(1)}
				})
			}
			_ = registry.Prometheus()
		}()
	}
	wait.Wait()
	if output := string(registry.Prometheus()); !strings.Contains(output, "xiaolanhe_mysql_pool_open_connections ") {
		t.Fatalf("final scrape has no pool snapshot:\n%s", output)
	}
}
