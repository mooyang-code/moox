package observability

import (
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PeriodMetrics contains the small set of low-cardinality metrics specific to
// View-driven factor periods. Subject, period and row keys are intentionally
// excluded from labels.
type PeriodMetrics struct {
	periodRunning  *prometheus.GaugeVec
	periodDegraded *prometheus.CounterVec
	manifestClear  *prometheus.CounterVec
	sourceReadyLag *prometheus.GaugeVec
}

func NewPeriodMetrics(reg prometheus.Registerer) (*PeriodMetrics, error) {
	if reg == nil {
		return nil, fmt.Errorf("factor period metrics registerer is nil")
	}
	m := &PeriodMetrics{
		periodRunning: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "factor", Name: "period_running",
			Help: "Factor periods currently executing.",
		}, []string{"source_view", "frequency"}),
		periodDegraded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "factor", Name: "period_degraded_total",
			Help: "Factor periods completed with degraded inputs or outputs.",
		}, []string{"source_view", "frequency"}),
		manifestClear: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "factor", Name: "manifest_clear_total",
			Help: "Factor output manifest clear operations.",
		}, []string{"binding"}),
		sourceReadyLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "factor", Name: "source_ready_lag_seconds",
			Help: "Age of the latest source View ready event when factor execution starts.",
		}, []string{"source_view", "frequency"}),
	}
	var err error
	if m.periodRunning, err = registerOrReusePeriod(reg, m.periodRunning); err != nil {
		return nil, err
	}
	if m.periodDegraded, err = registerOrReusePeriod(reg, m.periodDegraded); err != nil {
		return nil, err
	}
	if m.manifestClear, err = registerOrReusePeriod(reg, m.manifestClear); err != nil {
		return nil, err
	}
	if m.sourceReadyLag, err = registerOrReusePeriod(reg, m.sourceReadyLag); err != nil {
		return nil, err
	}
	return m, nil
}

func registerOrReusePeriod[T prometheus.Collector](reg prometheus.Registerer, collector T) (T, error) {
	err := reg.Register(collector)
	if err == nil {
		return collector, nil
	}
	if existing, ok := err.(prometheus.AlreadyRegisteredError); ok {
		if typed, ok := existing.ExistingCollector.(T); ok {
			return typed, nil
		}
	}
	return collector, err
}

func (m *PeriodMetrics) Begin(sourceView, frequency string) {
	if m == nil {
		return
	}
	m.periodRunning.WithLabelValues(strings.TrimSpace(sourceView), strings.TrimSpace(frequency)).Inc()
}

func (m *PeriodMetrics) End(sourceView, frequency string) {
	if m == nil {
		return
	}
	m.periodRunning.WithLabelValues(strings.TrimSpace(sourceView), strings.TrimSpace(frequency)).Dec()
}

func (m *PeriodMetrics) ObserveDegraded(sourceView, frequency string) {
	if m == nil {
		return
	}
	m.periodDegraded.WithLabelValues(strings.TrimSpace(sourceView), strings.TrimSpace(frequency)).Inc()
}

func (m *PeriodMetrics) ObserveManifestClear(binding string) {
	if m == nil {
		return
	}
	m.manifestClear.WithLabelValues(strings.TrimSpace(binding)).Inc()
}

func (m *PeriodMetrics) ObserveSourceReady(sourceView, frequency string, readyAt time.Time) {
	if m == nil || readyAt.IsZero() {
		return
	}
	lag := time.Since(readyAt.UTC()).Seconds()
	if lag < 0 {
		lag = 0
	}
	m.sourceReadyLag.WithLabelValues(strings.TrimSpace(sourceView), strings.TrimSpace(frequency)).Set(lag)
}
