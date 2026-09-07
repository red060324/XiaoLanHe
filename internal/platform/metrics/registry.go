package metrics

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Registry is a process-local Prometheus text collector. Every public record
// method applies a fixed vocabulary before values become labels, so user and
// provider-controlled strings cannot create unbounded time series.
type Registry struct {
	mu                   sync.RWMutex
	counters             map[string]map[string]uint64
	gauges               map[string]map[string]float64
	histograms           map[string]map[string]*histogram
	mysqlPoolSnapshot    func() MySQLPoolObservation
	mysqlPoolSnapshotSeq uint64
	mysqlPoolSnapshotMu  sync.Mutex
}

type histogram struct {
	count       uint64
	sum         float64
	buckets     []uint64
	upperBounds []float64
	labels      labels
}

type label struct{ name, value string }
type labels []label

type AssistantObservation struct {
	Event, Role, Operation, Outcome, StopReason, Route, Skill string
	Duration                                                  time.Duration
	ModelCalls, ToolCalls, Delegations                        int
}

type ModelObservation struct {
	Operation, Outcome                          string
	Duration                                    time.Duration
	UsageReported                               bool
	PromptTokens, CompletionTokens, TotalTokens int
	CachedTokens, ReasoningTokens               int
}

type MemoryObservation struct {
	Outcome, ErrorClass           string
	Duration                      time.Duration
	WorkingSetRunes, MessageCount int
}

type MySQLOperationObservation struct {
	Operation, Outcome string
	Duration           time.Duration
}

type MySQLRetryObservation struct {
	Reason, Outcome string
}

// MySQLPoolObservation is one absolute database/sql pool snapshot. BindMySQLPool
// refreshes these values before every scrape; cumulative DBStats fields must not
// be added to values from an earlier scrape.
type MySQLPoolObservation struct {
	MaxOpenConnections, OpenConnections, InUse, Idle               int64
	WaitCount, MaxIdleClosed, MaxIdleTimeClosed, MaxLifetimeClosed int64
	WaitDuration                                                   time.Duration
}

type MySQLCutoverObservation struct {
	Operation, Outcome string
	Duration           time.Duration
	// HasReconciliation distinguishes a complete source/target reconciliation
	// from inspect, dry-run, early-error and source-only preflight outcomes.
	HasReconciliation      bool
	SourceRows, TargetRows int64
	MismatchCount          int64
	Ready                  bool
}

type LightRAGFenceObservation struct {
	State, Operation, Outcome, Reason string
}

type LightRAGRebuildObservation struct {
	Operation, Outcome, Reason string
	Duration                   time.Duration
	Targets                    []LightRAGRebuildTarget
}

type LightRAGRebuildTarget struct {
	Target                                              string
	SourceTotal, Prepared, Rebuilt, Skipped, Duplicates int
	FailedBatches                                       int
}

var defaultRegistry = NewRegistry()

func Default() *Registry { return defaultRegistry }

func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]map[string]uint64),
		gauges:     make(map[string]map[string]float64),
		histograms: make(map[string]map[string]*histogram),
	}
}

func (r *Registry) ObserveAssistant(value AssistantObservation) {
	metricLabels := labels{
		{"event", bounded(value.Event, assistantEvents)},
		{"agent_role", bounded(value.Role, assistantRoles)},
		{"operation", bounded(value.Operation, assistantOperations)},
		{"outcome", bounded(value.Outcome, assistantOutcomes)},
		{"stop_reason", bounded(value.StopReason, assistantStopReasons)},
		{"route", bounded(strings.ToLower(value.Route), assistantRoutes)},
		{"skill", bounded(value.Skill, assistantSkills)},
	}
	r.increment("xiaolanhe_assistant_operations_total", metricLabels, 1)
	r.observe("xiaolanhe_assistant_operation_duration_seconds", metricLabels, seconds(value.Duration))
	if value.Event != "assistant.run" {
		return
	}
	for kind, count := range map[string]int{"model": value.ModelCalls, "tool": value.ToolCalls, "delegation": value.Delegations} {
		if count > 0 {
			r.increment("xiaolanhe_assistant_budget_units_total", labels{{"kind", kind}}, uint64(min(count, 1_000_000)))
		}
	}
}

func (r *Registry) ObserveModel(value ModelObservation) {
	metricLabels := labels{
		{"operation", bounded(value.Operation, modelOperations)},
		{"outcome", bounded(value.Outcome, modelOutcomes)},
		{"usage_reported", strconv.FormatBool(value.UsageReported)},
	}
	r.increment("xiaolanhe_model_requests_total", metricLabels, 1)
	r.observe("xiaolanhe_model_request_duration_seconds", metricLabels, seconds(value.Duration))
	if !value.UsageReported {
		return
	}
	for kind, count := range map[string]int{
		"prompt": value.PromptTokens, "completion": value.CompletionTokens, "total": value.TotalTokens,
		"cached": value.CachedTokens, "reasoning": value.ReasoningTokens,
	} {
		if count > 0 {
			r.increment("xiaolanhe_model_tokens_total", labels{{"kind", kind}}, uint64(min(count, 100_000_000)))
		}
	}
}

func (r *Registry) ObserveLightRAGRequest(operation, outcome string, duration time.Duration) {
	metricLabels := labels{{"operation", bounded(operation, lightragOperations)}, {"outcome", bounded(outcome, lightragOutcomes)}}
	r.increment("xiaolanhe_lightrag_requests_total", metricLabels, 1)
	r.observe("xiaolanhe_lightrag_request_duration_seconds", metricLabels, seconds(duration))
}

func (r *Registry) ObserveLightRAGQuery(mode, outcome string, duration time.Duration) {
	metricLabels := labels{{"mode", bounded(mode, lightragModes)}, {"outcome", bounded(outcome, lightragOutcomes)}}
	r.increment("xiaolanhe_lightrag_queries_total", metricLabels, 1)
	r.observe("xiaolanhe_lightrag_query_duration_seconds", metricLabels, seconds(duration))
}

func (r *Registry) SetLightRAGHealth(contractHealthy, pipelineActive, recoveryRequired bool) {
	r.setGauge("xiaolanhe_lightrag_storage_contract_healthy", nil, boolFloat(contractHealthy))
	r.setGauge("xiaolanhe_lightrag_pipeline_active", nil, boolFloat(pipelineActive))
	r.setGauge("xiaolanhe_lightrag_recovery_required", nil, boolFloat(recoveryRequired))
}

func (r *Registry) SetLightRAGDocumentStatuses(counts map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	series := make(map[string]float64, len(documentStatuses))
	for status := range documentStatuses {
		value := counts[strings.ToUpper(status)]
		if value < 0 {
			value = 0
		}
		series[labels{{"status", status}}.key()] = float64(value)
	}
	r.gauges["xiaolanhe_lightrag_documents"] = series
}

func (r *Registry) ObserveMySQLOperation(value MySQLOperationObservation) {
	metricLabels := labels{
		{"operation", boundedUnknown(value.Operation, mysqlOperations)},
		{"outcome", boundedUnknown(value.Outcome, mysqlOperationOutcomes)},
	}
	r.increment("xiaolanhe_mysql_operations_total", metricLabels, 1)
	r.observe("xiaolanhe_mysql_operation_duration_seconds", metricLabels, seconds(value.Duration))
}

func (r *Registry) ObserveMySQLRetry(value MySQLRetryObservation) {
	r.increment("xiaolanhe_mysql_transaction_retry_events_total", labels{
		{"reason", boundedUnknown(value.Reason, mysqlRetryReasons)},
		{"outcome", boundedUnknown(value.Outcome, mysqlRetryOutcomes)},
	}, 1)
}

// BindMySQLPool installs an absolute snapshot callback. The callback runs once
// immediately before each Prometheus scrape, outside the registry lock. A panic
// leaves the prior snapshot intact and does not prevent the scrape.
func (r *Registry) BindMySQLPool(snapshot func() MySQLPoolObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mysqlPoolSnapshot = snapshot
	r.mysqlPoolSnapshotSeq++
	if snapshot == nil {
		for _, name := range mysqlPoolMetricNames {
			delete(r.gauges, name)
		}
	}
}

func (r *Registry) SetMySQLMigrations(applied, pending, dirty int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges["xiaolanhe_mysql_migrations"] = map[string]float64{
		labels{{"state", "applied"}}.key(): float64(nonNegativeInt(applied)),
		labels{{"state", "pending"}}.key(): float64(nonNegativeInt(pending)),
		labels{{"state", "dirty"}}.key():   float64(nonNegativeInt(dirty)),
	}
}

func (r *Registry) ObserveMySQLCutover(value MySQLCutoverObservation) {
	metricLabels := labels{
		{"operation", boundedUnknown(value.Operation, mysqlCutoverOperations)},
		{"outcome", boundedUnknown(value.Outcome, mysqlCutoverOutcomes)},
	}
	r.increment("xiaolanhe_mysql_cutover_runs_total", metricLabels, 1)
	r.observe("xiaolanhe_mysql_cutover_run_duration_seconds", metricLabels, seconds(value.Duration))
	if !value.HasReconciliation {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges["xiaolanhe_mysql_cutover_last_reconciliation_rows"] = map[string]float64{
		labels{{"side", "source"}}.key(): float64(nonNegativeInt64(value.SourceRows)),
		labels{{"side", "target"}}.key(): float64(nonNegativeInt64(value.TargetRows)),
	}
	r.gauges["xiaolanhe_mysql_cutover_last_reconciliation_mismatches"] = map[string]float64{"": float64(nonNegativeInt64(value.MismatchCount))}
	r.gauges["xiaolanhe_mysql_cutover_last_reconciliation_ready"] = map[string]float64{"": boolFloat(value.Ready)}
}

func (r *Registry) ObserveLightRAGFence(value LightRAGFenceObservation) {
	state := boundedUnknown(value.State, lightragFenceStates)
	metricLabels := labels{
		{"state", state},
		{"operation", boundedUnknown(value.Operation, lightragFenceOperations)},
		{"outcome", boundedUnknown(value.Outcome, lightragFenceOutcomes)},
		{"reason", boundedUnknown(value.Reason, lightragFenceReasons)},
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters["xiaolanhe_lightrag_fence_observations_total"] == nil {
		r.counters["xiaolanhe_lightrag_fence_observations_total"] = make(map[string]uint64)
	}
	r.counters["xiaolanhe_lightrag_fence_observations_total"][metricLabels.key()]++
	series := make(map[string]float64, len(lightragFenceStateValues))
	for _, candidate := range lightragFenceStateValues {
		series[labels{{"state", candidate}}.key()] = boolFloat(candidate == state)
	}
	r.gauges["xiaolanhe_lightrag_fence_state"] = series
}

func (r *Registry) SetLightRAGRebuild(value LightRAGRebuildObservation) {
	operation := boundedUnknown(value.Operation, lightragFenceOperations)
	outcome := boundedUnknown(value.Outcome, lightragFenceOutcomes)
	reason := boundedUnknown(value.Reason, lightragFenceReasons)
	baseLabels := labels{{"operation", operation}, {"outcome", outcome}, {"reason", reason}}
	series := make(map[string]float64, len(value.Targets)*6)
	for _, target := range value.Targets {
		targetName := boundedUnknown(target.Target, lightragRebuildTargets)
		for _, stat := range []struct {
			name  string
			value int
		}{
			{"source_total", target.SourceTotal},
			{"prepared", target.Prepared},
			{"rebuilt", target.Rebuilt},
			{"skipped", target.Skipped},
			{"duplicates", target.Duplicates},
			{"failed_batches", target.FailedBatches},
		} {
			metricLabels := append(labels(nil), baseLabels...)
			metricLabels = append(metricLabels, label{"target", targetName}, label{"stat", stat.name})
			series[metricLabels.key()] = float64(nonNegativeInt(stat.value))
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges["xiaolanhe_lightrag_rebuild_last"] = series
	r.gauges["xiaolanhe_lightrag_rebuild_last_duration_seconds"] = map[string]float64{baseLabels.key(): seconds(value.Duration)}
}

func (r *Registry) SetLightRAGBackend(vectorExpected, serviceHealthy, generationMatch bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges["xiaolanhe_lightrag_backend_expected"] = map[string]float64{"": boolFloat(vectorExpected)}
	r.gauges["xiaolanhe_lightrag_service_healthy"] = map[string]float64{"": boolFloat(serviceHealthy)}
	r.gauges["xiaolanhe_lightrag_generation_match"] = map[string]float64{"": boolFloat(generationMatch)}
}

func (r *Registry) ObserveMemory(value MemoryObservation) {
	metricLabels := labels{{"outcome", bounded(value.Outcome, memoryOutcomes)}, {"error_class", bounded(value.ErrorClass, memoryErrorClasses)}}
	r.increment("xiaolanhe_assistant_memory_refresh_total", metricLabels, 1)
	r.observe("xiaolanhe_assistant_memory_refresh_duration_seconds", metricLabels, seconds(value.Duration))
	if value.WorkingSetRunes > 0 {
		r.observeWithBuckets("xiaolanhe_assistant_memory_working_set_runes", nil, float64(min(value.WorkingSetRunes, 1_000_000)), memoryRuneBuckets)
	}
	if value.MessageCount > 0 {
		r.observeWithBuckets("xiaolanhe_assistant_memory_messages", nil, float64(min(value.MessageCount, 10_000)), messageCountBuckets)
	}
}

func (r *Registry) ObserveFlashSale(operation, outcome string, duration time.Duration, items int, pendingAge time.Duration) {
	metricLabels := labels{
		{"operation", bounded(operation, flashSaleOperations)},
		{"outcome", bounded(outcome, flashSaleOutcomes)},
	}
	r.increment("xiaolanhe_flash_sale_operations_total", metricLabels, 1)
	r.observe("xiaolanhe_flash_sale_operation_duration_seconds", metricLabels, seconds(duration))
	if items > 0 {
		r.increment("xiaolanhe_flash_sale_items_total", metricLabels, uint64(min(items, 1_000_000)))
	}
	if pendingAge > 0 {
		r.observeWithBuckets("xiaolanhe_flash_sale_pending_age_seconds", labels{{"stage", bounded(operation, flashSaleAgeStages)}}, seconds(pendingAge), pendingAgeBuckets)
	}
}

func (r *Registry) Prometheus() []byte {
	r.refreshMySQLPool()
	r.mu.RLock()
	defer r.mu.RUnlock()
	var output bytes.Buffer
	for _, descriptor := range descriptors {
		fmt.Fprintf(&output, "# HELP %s %s\n# TYPE %s %s\n", descriptor.name, descriptor.help, descriptor.name, descriptor.kind)
		switch descriptor.kind {
		case "counter":
			renderUnsigned(&output, descriptor.name, r.counters[descriptor.name])
		case "gauge":
			renderFloat(&output, descriptor.name, r.gauges[descriptor.name])
		case "histogram":
			renderHistograms(&output, descriptor.name, r.histograms[descriptor.name])
		}
	}
	return output.Bytes()
}

func (r *Registry) refreshMySQLPool() {
	// Serialize callbacks so two concurrent scrapes cannot publish an older
	// snapshot after a newer one. BindMySQLPool itself remains independent.
	r.mysqlPoolSnapshotMu.Lock()
	defer r.mysqlPoolSnapshotMu.Unlock()

	r.mu.RLock()
	snapshot, sequence := r.mysqlPoolSnapshot, r.mysqlPoolSnapshotSeq
	r.mu.RUnlock()
	if snapshot == nil {
		return
	}
	value, ok := safeMySQLPoolSnapshot(snapshot)
	if !ok {
		return
	}
	series := map[string]float64{
		"xiaolanhe_mysql_pool_max_open_connections":        float64(nonNegativeInt64(value.MaxOpenConnections)),
		"xiaolanhe_mysql_pool_open_connections":            float64(nonNegativeInt64(value.OpenConnections)),
		"xiaolanhe_mysql_pool_in_use_connections":          float64(nonNegativeInt64(value.InUse)),
		"xiaolanhe_mysql_pool_idle_connections":            float64(nonNegativeInt64(value.Idle)),
		"xiaolanhe_mysql_pool_wait_count_total":            float64(nonNegativeInt64(value.WaitCount)),
		"xiaolanhe_mysql_pool_wait_duration_seconds_total": seconds(value.WaitDuration),
		"xiaolanhe_mysql_pool_max_idle_closed_total":       float64(nonNegativeInt64(value.MaxIdleClosed)),
		"xiaolanhe_mysql_pool_max_idle_time_closed_total":  float64(nonNegativeInt64(value.MaxIdleTimeClosed)),
		"xiaolanhe_mysql_pool_max_lifetime_closed_total":   float64(nonNegativeInt64(value.MaxLifetimeClosed)),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if sequence != r.mysqlPoolSnapshotSeq || snapshot == nil {
		return
	}
	for name, metricValue := range series {
		r.gauges[name] = map[string]float64{"": metricValue}
	}
}

func safeMySQLPoolSnapshot(snapshot func() MySQLPoolObservation) (value MySQLPoolObservation, ok bool) {
	ok = true
	defer func() {
		if recover() != nil {
			value = MySQLPoolObservation{}
			ok = false
		}
	}()
	value = snapshot()
	return value, ok
}

func (r *Registry) increment(name string, metricLabels labels, delta uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters[name] == nil {
		r.counters[name] = make(map[string]uint64)
	}
	r.counters[name][metricLabels.key()] += delta
}

func (r *Registry) setGauge(name string, metricLabels labels, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gauges[name] == nil {
		r.gauges[name] = make(map[string]float64)
	}
	r.gauges[name][metricLabels.key()] = value
}

func (r *Registry) observe(name string, metricLabels labels, value float64) {
	r.observeWithBuckets(name, metricLabels, value, durationBuckets)
}

func (r *Registry) observeWithBuckets(name string, metricLabels labels, value float64, upperBounds []float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.histograms[name] == nil {
		r.histograms[name] = make(map[string]*histogram)
	}
	key := metricLabels.key()
	item := r.histograms[name][key]
	if item == nil {
		item = &histogram{buckets: make([]uint64, len(upperBounds)), upperBounds: append([]float64(nil), upperBounds...), labels: append(labels(nil), metricLabels...)}
		r.histograms[name][key] = item
	}
	item.count++
	item.sum += value
	for index, upperBound := range item.upperBounds {
		if value <= upperBound {
			item.buckets[index]++
		}
	}
}

func (v labels) key() string {
	if len(v) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteByte('{')
	for index, item := range v {
		if index > 0 {
			result.WriteByte(',')
		}
		result.WriteString(item.name)
		result.WriteString("=\"")
		result.WriteString(escapeLabel(item.value))
		result.WriteByte('"')
	}
	result.WriteByte('}')
	return result.String()
}

func renderUnsigned(output *bytes.Buffer, name string, values map[string]uint64) {
	for _, key := range sortedKeys(values) {
		fmt.Fprintf(output, "%s%s %d\n", name, key, values[key])
	}
}
func renderFloat(output *bytes.Buffer, name string, values map[string]float64) {
	for _, key := range sortedKeys(values) {
		fmt.Fprintf(output, "%s%s %s\n", name, key, formatFloat(values[key]))
	}
}
func renderHistograms(output *bytes.Buffer, name string, values map[string]*histogram) {
	for _, key := range sortedKeys(values) {
		item := values[key]
		for index, upperBound := range item.upperBounds {
			fmt.Fprintf(output, "%s_bucket%s %d\n", name, withLabel(item.labels, "le", formatFloat(upperBound)), item.buckets[index])
		}
		fmt.Fprintf(output, "%s_bucket%s %d\n", name, withLabel(item.labels, "le", "+Inf"), item.count)
		fmt.Fprintf(output, "%s_sum%s %s\n%s_count%s %d\n", name, key, formatFloat(item.sum), name, key, item.count)
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func withLabel(values labels, name, value string) string {
	copyValues := append(labels(nil), values...)
	copyValues = append(copyValues, label{name, value})
	return copyValues.key()
}
func escapeLabel(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
}
func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
func seconds(value time.Duration) float64 {
	if value < 0 {
		return 0
	}
	return value.Seconds()
}
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
func bounded(value string, allowed map[string]bool) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "none"
	}
	if allowed[value] {
		return value
	}
	return "unknown"
}

func boundedUnknown(value string, allowed map[string]bool) string {
	value = strings.TrimSpace(value)
	if allowed[value] {
		return value
	}
	return "unknown"
}

func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

var (
	durationBuckets     = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
	memoryRuneBuckets   = []float64{1_000, 2_000, 4_000, 8_000, 12_000, 20_000, 50_000, 100_000}
	messageCountBuckets = []float64{1, 2, 4, 8, 16, 32, 64, 128}
	pendingAgeBuckets   = []float64{1, 5, 10, 30, 60, 120, 300, 900, 3_600}
)

type descriptor struct{ name, help, kind string }

var descriptors = []descriptor{
	{"xiaolanhe_assistant_operations_total", "Bounded Assistant operations.", "counter"},
	{"xiaolanhe_assistant_operation_duration_seconds", "Assistant operation duration.", "histogram"},
	{"xiaolanhe_assistant_budget_units_total", "Assistant budget units consumed by completed runs.", "counter"},
	{"xiaolanhe_model_requests_total", "Chat model requests and token-report availability.", "counter"},
	{"xiaolanhe_model_request_duration_seconds", "Chat model request duration.", "histogram"},
	{"xiaolanhe_model_tokens_total", "Provider-reported chat model tokens.", "counter"},
	{"xiaolanhe_lightrag_requests_total", "Official LightRAG API requests.", "counter"},
	{"xiaolanhe_lightrag_request_duration_seconds", "Official LightRAG API request duration.", "histogram"},
	{"xiaolanhe_lightrag_queries_total", "Validated LightRAG queries by mode.", "counter"},
	{"xiaolanhe_lightrag_query_duration_seconds", "End-to-end LightRAG query duration.", "histogram"},
	{"xiaolanhe_lightrag_storage_contract_healthy", "Whether the latest LightRAG storage contract check passed.", "gauge"},
	{"xiaolanhe_lightrag_pipeline_active", "Whether the latest LightRAG health check reported an active pipeline.", "gauge"},
	{"xiaolanhe_lightrag_recovery_required", "Whether the latest LightRAG health check requires pipeline recovery.", "gauge"},
	{"xiaolanhe_lightrag_documents", "Managed LightRAG documents by validated status after a complete scan.", "gauge"},
	{"xiaolanhe_lightrag_fence_observations_total", "Bounded LightRAG rebuild-fence observations.", "counter"},
	{"xiaolanhe_lightrag_fence_state", "Latest LightRAG rebuild-fence state as a one-hot vector.", "gauge"},
	{"xiaolanhe_lightrag_rebuild_last", "Last LightRAG rebuild snapshot values by bounded target and statistic.", "gauge"},
	{"xiaolanhe_lightrag_rebuild_last_duration_seconds", "Duration of the last LightRAG rebuild snapshot.", "gauge"},
	{"xiaolanhe_lightrag_backend_expected", "Whether the latest LightRAG health response reported the expected vector backend.", "gauge"},
	{"xiaolanhe_lightrag_service_healthy", "Whether the latest official LightRAG health and storage contract check passed.", "gauge"},
	{"xiaolanhe_lightrag_generation_match", "Whether the latest LightRAG fence matched the required generation.", "gauge"},
	{"xiaolanhe_assistant_memory_refresh_total", "Conversation summary refresh attempts.", "counter"},
	{"xiaolanhe_assistant_memory_refresh_duration_seconds", "Conversation summary refresh duration.", "histogram"},
	{"xiaolanhe_assistant_memory_working_set_runes", "Conversation memory candidate size in Unicode code points.", "histogram"},
	{"xiaolanhe_assistant_memory_messages", "Conversation memory candidate message count.", "histogram"},
	{"xiaolanhe_flash_sale_operations_total", "Bounded flash-sale operations.", "counter"},
	{"xiaolanhe_flash_sale_operation_duration_seconds", "Flash-sale operation duration.", "histogram"},
	{"xiaolanhe_flash_sale_items_total", "Items processed by bounded flash-sale operations.", "counter"},
	{"xiaolanhe_flash_sale_pending_age_seconds", "Age of flash-sale work when consumed, recovered or released.", "histogram"},
	{"xiaolanhe_mysql_operations_total", "Bounded MySQL operations.", "counter"},
	{"xiaolanhe_mysql_operation_duration_seconds", "MySQL operation duration.", "histogram"},
	{"xiaolanhe_mysql_transaction_retry_events_total", "MySQL retryable transaction failure decisions by reason and outcome.", "counter"},
	{"xiaolanhe_mysql_pool_max_open_connections", "Configured maximum MySQL pool connections.", "gauge"},
	{"xiaolanhe_mysql_pool_open_connections", "Current open MySQL pool connections.", "gauge"},
	{"xiaolanhe_mysql_pool_in_use_connections", "Current in-use MySQL pool connections.", "gauge"},
	{"xiaolanhe_mysql_pool_idle_connections", "Current idle MySQL pool connections.", "gauge"},
	{"xiaolanhe_mysql_pool_wait_count_total", "Absolute database/sql MySQL pool wait count.", "gauge"},
	{"xiaolanhe_mysql_pool_wait_duration_seconds_total", "Absolute database/sql MySQL pool wait duration.", "gauge"},
	{"xiaolanhe_mysql_pool_max_idle_closed_total", "Absolute MySQL pool connections closed by the idle limit.", "gauge"},
	{"xiaolanhe_mysql_pool_max_idle_time_closed_total", "Absolute MySQL pool connections closed by the idle-time limit.", "gauge"},
	{"xiaolanhe_mysql_pool_max_lifetime_closed_total", "Absolute MySQL pool connections closed by the lifetime limit.", "gauge"},
	{"xiaolanhe_mysql_migrations", "Latest MySQL migration count by state.", "gauge"},
	{"xiaolanhe_mysql_cutover_runs_total", "Bounded PostgreSQL-to-MySQL cutover tool runs.", "counter"},
	{"xiaolanhe_mysql_cutover_run_duration_seconds", "PostgreSQL-to-MySQL cutover tool run duration.", "histogram"},
	{"xiaolanhe_mysql_cutover_last_reconciliation_rows", "Row totals from the last complete PostgreSQL-to-MySQL reconciliation by side.", "gauge"},
	{"xiaolanhe_mysql_cutover_last_reconciliation_mismatches", "Mismatch count from the last complete PostgreSQL-to-MySQL reconciliation.", "gauge"},
	{"xiaolanhe_mysql_cutover_last_reconciliation_ready", "Whether the last complete PostgreSQL-to-MySQL reconciliation was cutover-ready.", "gauge"},
}

func vocabulary(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

var (
	assistantEvents         = vocabulary("assistant.run", "assistant.route", "assistant.query_plan", "assistant.copilot", "assistant.agent", "assistant.delegate", "assistant.tool")
	assistantRoles          = vocabulary("game_copilot", "router", "query_planner", "research", "planning", "answer")
	assistantOperations     = vocabulary("prepare", "answer", "stream_prepare", "stream_answer", "route", "plan", "supervise", "retrieve", "select", "search_lightrag", "search_catalog", "search_forum", "search_web", "read_catalog", "read_entitlements", "score_constraints")
	assistantOutcomes       = vocabulary("ok", "error", "bounded", "selected", "no_result", "failed", "invalid", "cancelled")
	assistantStopReasons    = vocabulary("complete", "cancelled", "deadline", "max_model_calls", "max_tool_calls", "max_delegations", "invalid_output", "dependency_unavailable")
	assistantRoutes         = vocabulary("direct", "clarify", "research", "planning")
	assistantSkills         = vocabulary("generic_qa", "research_guide", "recommend_games", "build_team")
	modelOperations         = vocabulary("generate", "stream")
	modelOutcomes           = vocabulary("ok", "error", "cancelled", "deadline")
	lightragOperations      = vocabulary("auth_verify", "health", "pipeline_status", "query", "document_create", "document_track", "document_list", "document_delete")
	lightragOutcomes        = vocabulary("ok", "cancelled", "deadline", "invalid_input", "not_found", "conflict", "capacity", "unavailable", "contract")
	lightragModes           = vocabulary("local", "global", "hybrid", "mix")
	documentStatuses        = vocabulary("pending", "parsing", "analyzing", "preprocessed", "processing", "processed", "failed")
	memoryOutcomes          = vocabulary("skipped", "updated", "stale", "invalid", "error")
	memoryErrorClasses      = vocabulary("none", "cancelled", "deadline", "invalid_output", "dependency")
	flashSaleOperations     = vocabulary("lua_admission", "lua_release", "transaction", "transaction_check", "consume", "fulfil", "final_guard", "recovery", "expiry", "release", "release_retry")
	flashSaleAgeStages      = vocabulary("consume", "recovery", "release")
	flashSaleOutcomes       = vocabulary("accepted", "replay", "not_started", "ended", "exhausted", "already_reserved", "unavailable", "commit", "rollback", "unknown", "success", "retry", "retry_exhausted", "rejected", "terminal", "released", "invalid", "cancelled", "deadline", "dependency", "empty")
	mysqlOperations         = vocabulary("open", "ready", "ping", "migrate", "inspect_migrations", "repair_migration", "transaction")
	mysqlOperationOutcomes  = vocabulary("success", "error", "cancelled", "deadline", "commit_unknown")
	mysqlRetryReasons       = vocabulary("deadlock", "lock_wait_timeout")
	mysqlRetryOutcomes      = vocabulary("scheduled", "exhausted", "cancelled")
	mysqlCutoverOperations  = vocabulary("inspect", "copy", "resume", "verify")
	mysqlCutoverOutcomes    = vocabulary("success", "error", "cancelled", "deadline", "rejected")
	lightragFenceStates     = vocabulary("absent", "stale", "rebuilding", "failed", "verified", "invalid")
	lightragFenceOperations = vocabulary("migration_rebuild", "bootstrap_empty", "restore_verify", "revalidate")
	lightragFenceOutcomes   = vocabulary("verified", "rejected", "error")
	lightragFenceReasons    = vocabulary(
		"restore_pending_verification", "contract_changed", "generation_changed", "legacy_manifest_changed", "invalidated",
		"in_progress", "operation_failed", "abandoned_rebuilding", "verified",
		"marker_missing", "marker_unreadable", "marker_invalid", "state_not_verified", "generation_mismatch", "contract_mismatch",
		"report_unreadable", "report_digest_mismatch", "report_invalid", "report_identity_mismatch",
	)
	lightragRebuildTargets = vocabulary("entities", "relationships", "chunks")
)

var lightragFenceStateValues = []string{"absent", "stale", "rebuilding", "failed", "verified", "invalid", "unknown"}

var mysqlPoolMetricNames = []string{
	"xiaolanhe_mysql_pool_max_open_connections",
	"xiaolanhe_mysql_pool_open_connections",
	"xiaolanhe_mysql_pool_in_use_connections",
	"xiaolanhe_mysql_pool_idle_connections",
	"xiaolanhe_mysql_pool_wait_count_total",
	"xiaolanhe_mysql_pool_wait_duration_seconds_total",
	"xiaolanhe_mysql_pool_max_idle_closed_total",
	"xiaolanhe_mysql_pool_max_idle_time_closed_total",
	"xiaolanhe_mysql_pool_max_lifetime_closed_total",
}
