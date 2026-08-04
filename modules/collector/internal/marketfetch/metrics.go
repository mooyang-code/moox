package marketfetch

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	assignmentRequired    *prometheus.GaugeVec
	assignmentActive      *prometheus.GaugeVec
	assignmentLastSuccess *prometheus.GaugeVec
	assignmentHealthy     *prometheus.GaugeVec
	timerAvailable        *prometheus.GaugeVec
	assignmentErrors      *prometheus.CounterVec
}

// SetDatasetRunObserver is retained as a no-op for the manual/catchup helper
// path. Timer-triggered realtime freshness comes from Storage, not completion
// events.
func (m *Metrics) SetDatasetRunObserver(any) {}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		assignmentRequired:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_required", Help: "Required Timer SCF assignments."}, []string{"space_id", "dataset_id", "frequency"}),
		assignmentActive:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_active", Help: "Active Timer SCF assignments."}, []string{"space_id", "dataset_id", "frequency"}),
		assignmentLastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", Help: "Last successful Timer assignment reconciliation timestamp."}, []string{"space_id"}),
		assignmentHealthy:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_healthy", Help: "Whether the latest Timer assignment reconciliation completed (1 healthy, 0 failed)."}, []string{"space_id"}),
		timerAvailable:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_available", Help: "Tencent Timer trigger availability for Collector nodes (1 available, 0 unavailable, -1 unknown)."}, []string{"space_id", "node_id", "enabled"}),
		assignmentErrors:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_fetch_assignment_errors_total", Help: "Timer assignment reconciliation errors."}, []string{"space_id", "reason"}),
	}
	metrics.assignmentRequired = registerGaugeVec(reg, metrics.assignmentRequired)
	metrics.assignmentActive = registerGaugeVec(reg, metrics.assignmentActive)
	metrics.assignmentLastSuccess = registerGaugeVec(reg, metrics.assignmentLastSuccess)
	metrics.assignmentHealthy = registerGaugeVec(reg, metrics.assignmentHealthy)
	metrics.timerAvailable = registerGaugeVec(reg, metrics.timerAvailable)
	metrics.assignmentErrors = registerCounterVec(reg, metrics.assignmentErrors)
	return metrics
}

func registerCounterVec(reg prometheus.Registerer, collector *prometheus.CounterVec) *prometheus.CounterVec {
	if err := reg.Register(collector); err != nil {
		if existing, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if typed, ok := existing.ExistingCollector.(*prometheus.CounterVec); ok {
				return typed
			}
		}
	}
	return collector
}

func registerGaugeVec(reg prometheus.Registerer, collector *prometheus.GaugeVec) *prometheus.GaugeVec {
	if err := reg.Register(collector); err != nil {
		if existing, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if typed, ok := existing.ExistingCollector.(*prometheus.GaugeVec); ok {
				return typed
			}
		}
	}
	return collector
}

// ObserveAssignment records the desired and currently assigned Timer shards.
func (m *Metrics) ObserveAssignment(spaceID, datasetID, frequency string, required, active int, reconciledAt int64) {
	m.ObserveAssignmentDesired(spaceID, datasetID, frequency, required, active)
	m.ObserveAssignmentSuccess(spaceID, reconciledAt)
}

// ObserveAssignmentDesired records the state requested from CloudNode. It is
// deliberately separate from success: SubmitRuntimeConfigs is asynchronous,
// so accepting a job must not advance the last-success timestamp.
func (m *Metrics) ObserveAssignmentDesired(spaceID, datasetID, frequency string, required, active int) {
	if m == nil {
		return
	}
	if required < 0 {
		required = 0
	}
	if active < 0 {
		active = 0
	}
	m.assignmentRequired.WithLabelValues(spaceID, datasetID, frequency).Set(float64(required))
	m.assignmentActive.WithLabelValues(spaceID, datasetID, frequency).Set(float64(active))
}

// ResetAssignmentScope removes labels for rules which disappeared from the
// current reconciliation result. GaugeVec keeps a child until it is deleted;
// leaving those children around would make Monitor alert for disabled rules.
func (m *Metrics) ResetAssignmentScope(spaceID string) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	labels := prometheus.Labels{"space_id": spaceID}
	m.assignmentRequired.DeletePartialMatch(labels)
	m.assignmentActive.DeletePartialMatch(labels)
}

func (m *Metrics) ResetTimerScope(spaceID string) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	m.timerAvailable.DeletePartialMatch(prometheus.Labels{"space_id": spaceID})
}

func (m *Metrics) ObserveAssignmentSuccess(spaceID string, reconciledAt int64) {
	if m == nil || reconciledAt <= 0 {
		return
	}
	m.assignmentLastSuccess.WithLabelValues(spaceID).Set(float64(reconciledAt))
	m.assignmentHealthy.WithLabelValues(spaceID).Set(1)
}

func (m *Metrics) ObserveAssignmentFailure(spaceID, reason string) {
	if m == nil {
		return
	}
	m.assignmentHealthy.WithLabelValues(spaceID).Set(0)
	m.ObserveAssignmentError(spaceID, reason)
}

func (m *Metrics) ObserveAssignmentError(spaceID, reason string) {
	if m == nil {
		return
	}
	m.assignmentErrors.WithLabelValues(spaceID, reason).Inc()
}

// ObserveTimerState mirrors the last CloudNode trigger readback. The
// Collector is the only component that already lists the timer fleet, so it
// forwards this small coordination fact through the normal metrics reporter
// instead of making Monitor call Tencent or CloudNode directly.
func (m *Metrics) ObserveTimerState(spaceID, nodeID, enabled string, value float64) {
	if m == nil || strings.TrimSpace(nodeID) == "" {
		return
	}
	if value < -1 {
		value = -1
	}
	if value > 1 {
		value = 1
	}
	m.timerAvailable.WithLabelValues(spaceID, nodeID, enabled).Set(value)
}

// Observe and SetRetryPending remain no-ops for the retired completion-based
// realtime scheduler. They keep the manual catchup package boundary source
// compatible without registering legacy high-cardinality metrics.
func (m *Metrics) Observe(string, any)                         {}
func (m *Metrics) SetRetryPending(string, string, string, int) {}
