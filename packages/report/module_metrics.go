package report

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const MaxModuleMetricSeries = 256

var allowedModules = stringSet("collector", "cloudnode", "factor", "strategy", "archive", "trade", "monitor", "gateway", "eventbus")
var allowedStages = stringSet(
	"collect", "dispatch", "calculate", "evaluate", "target_commit", "materialize",
	"rebalance", "reconcile", "ingest", "route_refresh", "publish",
)
var allowedResults = stringSet("success", "error", "rejected")

type ModuleMetrics struct {
	module           string
	allowedPipelines map[string]bool
	runs             *prometheus.CounterVec
	lastSuccess      *prometheus.GaugeVec
	lastError        *prometheus.GaugeVec
	watermark        *prometheus.GaugeVec
	inputWatermark   *prometheus.GaugeVec
	mu               sync.Mutex
	series           map[string]bool
	watermarks       map[string]float64
	inputWatermarks  map[string]float64
	maxSeries        int
}

var defaultModuleMetrics = struct {
	sync.Mutex
	items map[string]*ModuleMetrics
}{items: map[string]*ModuleMetrics{}}

var moduleMetricErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "moox_module_metrics_errors_total",
	Help: "Rejected module metric observations, grouped by operation.",
}, []string{"module", "operation"})
var moduleMetricLastError = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "moox_module_metrics_last_error_timestamp_seconds",
	Help: "Unix timestamp of the latest rejected module metric observation.",
}, []string{"module"})

func init() {
	prometheus.MustRegister(moduleMetricErrors, moduleMetricLastError)
}

func recordModuleMetricError(module, operation string, err error) error {
	if err != nil && allowedModules[module] {
		moduleMetricErrors.WithLabelValues(module, operation).Inc()
		moduleMetricLastError.WithLabelValues(module).Set(float64(time.Now().UTC().Unix()))
	}
	return err
}

func DefaultModuleMetrics(module string) (*ModuleMetrics, error) {
	defaultModuleMetrics.Lock()
	defer defaultModuleMetrics.Unlock()
	if existing := defaultModuleMetrics.items[module]; existing != nil {
		return existing, nil
	}
	cfg, err := ValidatePipelineEnvironment()
	if err != nil {
		return nil, err
	}
	if len(cfg.Pipelines) == 0 {
		return nil, fmt.Errorf("pipeline config is required for module metrics")
	}
	metrics, err := NewModuleMetrics(prometheus.DefaultRegisterer, module, cfg.IDsForModule(module))
	if err != nil {
		return nil, err
	}
	defaultModuleMetrics.items[module] = metrics
	return metrics, nil
}

func ObserveModuleRun(module, stage, result, pipeline string, at time.Time) error {
	metrics, err := DefaultModuleMetrics(module)
	if err != nil {
		return recordModuleMetricError(module, "run", err)
	}
	return recordModuleMetricError(module, "run", metrics.RecordRun(stage, result, pipeline, at))
}

func ObserveModuleWatermark(module, stage, pipeline string, at time.Time) error {
	metrics, err := DefaultModuleMetrics(module)
	if err != nil {
		return recordModuleMetricError(module, "watermark", err)
	}
	return recordModuleMetricError(module, "watermark", metrics.AdvanceWatermark(stage, pipeline, at))
}

// ObserveModuleInputWatermark records the latest business timestamp accepted
// by a pipeline. It is intentionally separate from last-success and output
// watermark metrics so Doctor never infers input progress from execution time.
func ObserveModuleInputWatermark(module, stage, pipeline string, at time.Time) error {
	metrics, err := DefaultModuleMetrics(module)
	if err != nil {
		return recordModuleMetricError(module, "input_watermark", err)
	}
	return recordModuleMetricError(module, "input_watermark", metrics.AdvanceInputWatermark(stage, pipeline, at))
}

func NewModuleMetrics(registerer prometheus.Registerer, module string, pipelines []string) (*ModuleMetrics, error) {
	if !allowedModules[module] {
		return nil, fmt.Errorf("unknown metrics module %q", module)
	}
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	allowed := make(map[string]bool, len(pipelines))
	for _, pipeline := range pipelines {
		pipeline = strings.TrimSpace(pipeline)
		if err := validateMetricLabel("pipeline", pipeline); err != nil {
			return nil, err
		}
		if allowed[pipeline] {
			return nil, fmt.Errorf("duplicate metrics pipeline %q", pipeline)
		}
		allowed[pipeline] = true
	}
	m := &ModuleMetrics{
		module: module, allowedPipelines: allowed, series: map[string]bool{}, watermarks: map[string]float64{}, inputWatermarks: map[string]float64{}, maxSeries: MaxModuleMetricSeries,
		runs:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_module_runs_total", Help: "Completed module stage runs."}, []string{"module", "stage", "result", "pipeline"}),
		lastSuccess:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_module_last_success_timestamp_seconds", Help: "Last successful module stage completion."}, []string{"module", "stage", "pipeline"}),
		lastError:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_module_last_error_timestamp_seconds", Help: "Last failed module stage completion."}, []string{"module", "stage", "pipeline"}),
		watermark:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_business_watermark_timestamp_seconds", Help: "Monotonic authoritative business output watermark."}, []string{"module", "stage", "pipeline"}),
		inputWatermark: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_module_input_watermark_timestamp_seconds", Help: "Monotonic business timestamp accepted as pipeline input."}, []string{"module", "stage", "pipeline"}),
	}
	for _, collector := range []prometheus.Collector{m.runs, m.lastSuccess, m.lastError, m.watermark, m.inputWatermark} {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register module metrics: %w", err)
		}
	}
	return m, nil
}

func (m *ModuleMetrics) RecordRun(stage, result, pipeline string, at time.Time) error {
	if err := m.validate(stage, result, pipeline); err != nil {
		return err
	}
	if err := m.claim("runs", stage, result, pipeline); err != nil {
		return err
	}
	m.runs.WithLabelValues(m.module, stage, result, pipeline).Inc()
	if at.IsZero() {
		return nil
	}
	metric := m.lastSuccess
	kind := "last_success"
	if result != "success" {
		metric = m.lastError
		kind = "last_error"
	}
	if err := m.claim(kind, stage, "", pipeline); err != nil {
		return err
	}
	metric.WithLabelValues(m.module, stage, pipeline).Set(float64(at.UTC().Unix()))
	return nil
}

func (m *ModuleMetrics) AdvanceWatermark(stage, pipeline string, value time.Time) error {
	if err := m.validate(stage, "success", pipeline); err != nil {
		return err
	}
	if value.IsZero() {
		return fmt.Errorf("watermark is required")
	}
	seconds := float64(value.UTC().Unix())
	key := stage + "\x00" + pipeline
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.watermarks[key]; ok && seconds < previous {
		return fmt.Errorf("watermark regression for %s/%s: %v < %v", stage, pipeline, seconds, previous)
	}
	if err := m.claimLocked("watermark", stage, "", pipeline); err != nil {
		return err
	}
	m.watermarks[key] = seconds
	m.watermark.WithLabelValues(m.module, stage, pipeline).Set(seconds)
	return nil
}

func (m *ModuleMetrics) AdvanceInputWatermark(stage, pipeline string, value time.Time) error {
	if err := m.validate(stage, "success", pipeline); err != nil {
		return err
	}
	if value.IsZero() {
		return fmt.Errorf("input watermark is required")
	}
	seconds := float64(value.UTC().Unix())
	key := stage + "\x00" + pipeline
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.inputWatermarks[key]; ok && seconds < previous {
		return fmt.Errorf("input watermark regression for %s/%s: %v < %v", stage, pipeline, seconds, previous)
	}
	if err := m.claimLocked("input_watermark", stage, "", pipeline); err != nil {
		return err
	}
	m.inputWatermarks[key] = seconds
	m.inputWatermark.WithLabelValues(m.module, stage, pipeline).Set(seconds)
	return nil
}

func (m *ModuleMetrics) validate(stage, result, pipeline string) error {
	if m == nil {
		return fmt.Errorf("module metrics are nil")
	}
	if !allowedStages[stage] {
		return fmt.Errorf("unknown metrics stage %q", stage)
	}
	if !allowedResults[result] {
		return fmt.Errorf("unknown metrics result %q", result)
	}
	if !m.allowedPipelines[pipeline] {
		return fmt.Errorf("unknown metrics pipeline %q", pipeline)
	}
	return nil
}

func (m *ModuleMetrics) claim(kind, stage, result, pipeline string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claimLocked(kind, stage, result, pipeline)
}

func (m *ModuleMetrics) claimLocked(kind, stage, result, pipeline string) error {
	key := strings.Join([]string{kind, m.module, stage, result, pipeline}, "\x00")
	if m.series[key] {
		return nil
	}
	if len(m.series) >= m.maxSeries {
		return fmt.Errorf("module metric series limit exceeded: %d", m.maxSeries)
	}
	m.series[key] = true
	return nil
}

func validateMetricLabel(name, value string) error {
	if value == "" || len(value) > 64 {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return fmt.Errorf("invalid %s %q", name, value)
		}
	}
	return nil
}

func stringSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
