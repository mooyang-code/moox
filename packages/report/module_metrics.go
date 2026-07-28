package report

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const MaxModuleMetricSeries = 256

var allowedModules = stringSet(
	"admin", "archive", "cloudnode", "collector", "eventbus", "factor",
	"gateway", "hostagent", "monitor", "scf", "storage", "strategy", "trade",
)
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
	errors           *prometheus.CounterVec
	lastMetricError  prometheus.Gauge
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
		return err
	}
	return metrics.ObserveRun(stage, result, pipeline, at)
}

func ObserveModuleWatermark(module, stage, pipeline string, at time.Time) error {
	metrics, err := DefaultModuleMetrics(module)
	if err != nil {
		return err
	}
	return metrics.AdvanceWatermark(stage, pipeline, at)
}

// ObserveModuleInputWatermark records the latest business timestamp accepted
// by a pipeline. It is intentionally separate from last-success and output
// watermark metrics so Doctor never infers input progress from execution time.
func ObserveModuleInputWatermark(module, stage, pipeline string, at time.Time) error {
	metrics, err := DefaultModuleMetrics(module)
	if err != nil {
		return err
	}
	return metrics.AdvanceInputWatermark(stage, pipeline, at)
}

func NewModuleMetrics(registerer prometheus.Registerer, module string, pipelines []string) (*ModuleMetrics, error) {
	if err := validateModuleName(module); err != nil {
		return nil, err
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
		runs:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_" + module + "_runs_total", Help: "Completed module stage runs."}, []string{"stage", "result", "pipeline"}),
		lastSuccess:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_" + module + "_last_success_timestamp_seconds", Help: "Last successful module stage completion."}, []string{"stage", "pipeline"}),
		lastError:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_" + module + "_last_error_timestamp_seconds", Help: "Last failed module stage completion."}, []string{"stage", "pipeline"}),
		watermark:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_" + module + "_business_watermark_timestamp_seconds", Help: "Monotonic authoritative business output watermark."}, []string{"stage", "pipeline"}),
		inputWatermark: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_" + module + "_input_watermark_timestamp_seconds", Help: "Monotonic business timestamp accepted as pipeline input."}, []string{"stage", "pipeline"}),
		errors:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_" + module + "_metrics_errors_total", Help: "Rejected module metric observations."}, []string{"operation"}),
		lastMetricError: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_" + module + "_metrics_last_error_timestamp_seconds",
			Help: "Unix timestamp of the latest rejected module metric observation.",
		}),
	}
	registered := make([]prometheus.Collector, 0, 7)
	for _, collector := range []prometheus.Collector{m.runs, m.lastSuccess, m.lastError, m.watermark, m.inputWatermark, m.errors, m.lastMetricError} {
		if err := registerer.Register(collector); err != nil {
			for _, item := range registered {
				registerer.Unregister(item)
			}
			return nil, fmt.Errorf("register module metrics: %w", err)
		}
		registered = append(registered, collector)
	}
	return m, nil
}

func (m *ModuleMetrics) ObserveRun(stage, result, pipeline string, at time.Time) error {
	if err := m.validate(stage, result, pipeline); err != nil {
		return m.recordError("run", err)
	}
	if err := m.claim("runs", stage, result, pipeline); err != nil {
		return m.recordError("run", err)
	}
	m.runs.WithLabelValues(stage, result, pipeline).Inc()
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
		return m.recordError("run", err)
	}
	metric.WithLabelValues(stage, pipeline).Set(float64(at.UTC().Unix()))
	return nil
}

func (m *ModuleMetrics) AdvanceWatermark(stage, pipeline string, value time.Time) error {
	if err := m.validate(stage, "success", pipeline); err != nil {
		return m.recordError("watermark", err)
	}
	if value.IsZero() {
		return m.recordError("watermark", fmt.Errorf("watermark is required"))
	}
	seconds := float64(value.UTC().Unix())
	key := stage + "\x00" + pipeline
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.watermarks[key]; ok && seconds < previous {
		return m.recordError("watermark", fmt.Errorf("watermark regression for %s/%s: %v < %v", stage, pipeline, seconds, previous))
	}
	if err := m.claimLocked("watermark", stage, "", pipeline); err != nil {
		return m.recordErrorLocked("watermark", err)
	}
	m.watermarks[key] = seconds
	m.watermark.WithLabelValues(stage, pipeline).Set(seconds)
	return nil
}

func (m *ModuleMetrics) AdvanceInputWatermark(stage, pipeline string, value time.Time) error {
	if err := m.validate(stage, "success", pipeline); err != nil {
		return m.recordError("input_watermark", err)
	}
	if value.IsZero() {
		return m.recordError("input_watermark", fmt.Errorf("input watermark is required"))
	}
	seconds := float64(value.UTC().Unix())
	key := stage + "\x00" + pipeline
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.inputWatermarks[key]; ok && seconds < previous {
		return m.recordError("input_watermark", fmt.Errorf("input watermark regression for %s/%s: %v < %v", stage, pipeline, seconds, previous))
	}
	if err := m.claimLocked("input_watermark", stage, "", pipeline); err != nil {
		return m.recordErrorLocked("input_watermark", err)
	}
	m.inputWatermarks[key] = seconds
	m.inputWatermark.WithLabelValues(stage, pipeline).Set(seconds)
	return nil
}

func (m *ModuleMetrics) recordError(operation string, err error) error {
	if err == nil || m == nil {
		return err
	}
	m.errors.WithLabelValues(operation).Inc()
	m.lastMetricError.Set(float64(time.Now().UTC().Unix()))
	return err
}

func (m *ModuleMetrics) recordErrorLocked(operation string, err error) error {
	m.errors.WithLabelValues(operation).Inc()
	m.lastMetricError.Set(float64(time.Now().UTC().Unix()))
	return err
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

func validateModuleName(module string) error {
	if err := validateMetricLabel("module", module); err != nil {
		return err
	}
	if strings.Contains(module, "-") {
		return fmt.Errorf("invalid module %q", module)
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
