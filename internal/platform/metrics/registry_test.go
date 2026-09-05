package metrics

import (
	"fmt"
	"strings"
	"sync"
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
