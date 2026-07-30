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

const (
	ModuleMetricRuns              = "runs_total"
	ModuleMetricLastSuccess       = "last_success_timestamp_seconds"
	ModuleMetricLastError         = "last_error_timestamp_seconds"
	ModuleMetricBusinessWatermark = "business_watermark_timestamp_seconds"
	ModuleMetricInputWatermark    = "input_watermark_timestamp_seconds"
	ModuleMetricErrors            = "metrics_errors_total"
	ModuleMetricLastMetricsError  = "metrics_last_error_timestamp_seconds"
)

// ModuleMetricName returns the canonical metric family name for one module.
func ModuleMetricName(module, metric string) string {
	return "moox_" + module + "_" + metric
}

type ModuleMetrics struct {
	module              string
	allowedHealthChecks map[string]bool
	runs                *prometheus.CounterVec
	lastSuccess         *prometheus.GaugeVec
	lastError           *prometheus.GaugeVec
	watermark           *prometheus.GaugeVec
	inputWatermark      *prometheus.GaugeVec
	errors              *prometheus.CounterVec
	lastMetricError     prometheus.Gauge
	mu                  sync.Mutex
	series              map[string]bool
	watermarks          map[string]float64
	inputWatermarks     map[string]float64
	maxSeries           int
}

func NewModuleMetrics(registerer prometheus.Registerer, module string, healthChecks []string) (*ModuleMetrics, error) {
	if err := validateModuleName(module); err != nil {
		return nil, err
	}
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	allowed := make(map[string]bool, len(healthChecks))
	for _, healthCheck := range healthChecks {
		healthCheck = strings.TrimSpace(healthCheck)
		if err := validateMetricLabel("health_check", healthCheck); err != nil {
			return nil, err
		}
		if allowed[healthCheck] {
			return nil, fmt.Errorf("duplicate metrics health check %q", healthCheck)
		}
		allowed[healthCheck] = true
	}
	m := &ModuleMetrics{
		module: module, allowedHealthChecks: allowed, series: map[string]bool{}, watermarks: map[string]float64{}, inputWatermarks: map[string]float64{}, maxSeries: MaxModuleMetricSeries,
		runs:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: ModuleMetricName(module, ModuleMetricRuns), Help: "Completed module stage runs."}, []string{"stage", "result", "health_check"}),
		lastSuccess:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: ModuleMetricName(module, ModuleMetricLastSuccess), Help: "Last successful module stage completion."}, []string{"stage", "health_check"}),
		lastError:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: ModuleMetricName(module, ModuleMetricLastError), Help: "Last failed module stage completion."}, []string{"stage", "health_check"}),
		watermark:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: ModuleMetricName(module, ModuleMetricBusinessWatermark), Help: "Monotonic authoritative business output watermark."}, []string{"stage", "health_check"}),
		inputWatermark: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: ModuleMetricName(module, ModuleMetricInputWatermark), Help: "Monotonic business timestamp accepted as module input."}, []string{"stage", "health_check"}),
		errors:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: ModuleMetricName(module, ModuleMetricErrors), Help: "Rejected module metric observations."}, []string{"operation"}),
		lastMetricError: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: ModuleMetricName(module, ModuleMetricLastMetricsError),
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

func (m *ModuleMetrics) ObserveRun(stage, result, healthCheck string, at time.Time) error {
	if err := m.validate(stage, result, healthCheck); err != nil {
		return m.recordError("run", err)
	}
	if err := m.claim("runs", stage, result, healthCheck); err != nil {
		return m.recordError("run", err)
	}
	m.runs.WithLabelValues(stage, result, healthCheck).Inc()
	if at.IsZero() {
		return nil
	}
	metric := m.lastSuccess
	kind := "last_success"
	if result != "success" {
		metric = m.lastError
		kind = "last_error"
	}
	if err := m.claim(kind, stage, "", healthCheck); err != nil {
		return m.recordError("run", err)
	}
	metric.WithLabelValues(stage, healthCheck).Set(float64(at.UTC().Unix()))
	return nil
}

func (m *ModuleMetrics) AdvanceWatermark(stage, healthCheck string, value time.Time) error {
	if err := m.validate(stage, "success", healthCheck); err != nil {
		return m.recordError("watermark", err)
	}
	if value.IsZero() {
		return m.recordError("watermark", fmt.Errorf("watermark is required"))
	}
	seconds := float64(value.UTC().Unix())
	key := stage + "\x00" + healthCheck
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.watermarks[key]; ok && seconds < previous {
		return m.recordError("watermark", fmt.Errorf("watermark regression for %s/%s: %v < %v", stage, healthCheck, seconds, previous))
	}
	if err := m.claimLocked("watermark", stage, "", healthCheck); err != nil {
		return m.recordErrorLocked("watermark", err)
	}
	m.watermarks[key] = seconds
	m.watermark.WithLabelValues(stage, healthCheck).Set(seconds)
	return nil
}

func (m *ModuleMetrics) AdvanceInputWatermark(stage, healthCheck string, value time.Time) error {
	if err := m.validate(stage, "success", healthCheck); err != nil {
		return m.recordError("input_watermark", err)
	}
	if value.IsZero() {
		return m.recordError("input_watermark", fmt.Errorf("input watermark is required"))
	}
	seconds := float64(value.UTC().Unix())
	key := stage + "\x00" + healthCheck
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.inputWatermarks[key]; ok && seconds < previous {
		return m.recordError("input_watermark", fmt.Errorf("input watermark regression for %s/%s: %v < %v", stage, healthCheck, seconds, previous))
	}
	if err := m.claimLocked("input_watermark", stage, "", healthCheck); err != nil {
		return m.recordErrorLocked("input_watermark", err)
	}
	m.inputWatermarks[key] = seconds
	m.inputWatermark.WithLabelValues(stage, healthCheck).Set(seconds)
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

func (m *ModuleMetrics) validate(stage, result, healthCheck string) error {
	if m == nil {
		return fmt.Errorf("module metrics are nil")
	}
	if !allowedStages[stage] {
		return fmt.Errorf("unknown metrics stage %q", stage)
	}
	if !allowedResults[result] {
		return fmt.Errorf("unknown metrics result %q", result)
	}
	if !m.allowedHealthChecks[healthCheck] {
		return fmt.Errorf("unknown metrics health check %q", healthCheck)
	}
	return nil
}

func (m *ModuleMetrics) claim(kind, stage, result, healthCheck string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claimLocked(kind, stage, result, healthCheck)
}

func (m *ModuleMetrics) claimLocked(kind, stage, result, healthCheck string) error {
	key := strings.Join([]string{kind, m.module, stage, result, healthCheck}, "\x00")
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
