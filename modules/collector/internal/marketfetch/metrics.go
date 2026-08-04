package marketfetch

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	assignmentRequired    *prometheus.GaugeVec
	assignmentActive      *prometheus.GaugeVec
	assignmentLastSuccess *prometheus.GaugeVec
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
		assignmentErrors:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_fetch_assignment_errors_total", Help: "Timer assignment reconciliation errors."}, []string{"space_id", "reason"}),
	}
	metrics.assignmentRequired = registerGaugeVec(reg, metrics.assignmentRequired)
	metrics.assignmentActive = registerGaugeVec(reg, metrics.assignmentActive)
	metrics.assignmentLastSuccess = registerGaugeVec(reg, metrics.assignmentLastSuccess)
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
	if reconciledAt > 0 {
		m.assignmentLastSuccess.WithLabelValues(spaceID).Set(float64(reconciledAt))
	}
}

func (m *Metrics) ObserveAssignmentError(spaceID, reason string) {
	if m == nil {
		return
	}
	m.assignmentErrors.WithLabelValues(spaceID, reason).Inc()
}

// Observe and SetRetryPending remain no-ops for the retired completion-based
// realtime scheduler. They keep the manual catchup package boundary source
// compatible without registering legacy high-cardinality metrics.
func (m *Metrics) Observe(string, any)                         {}
func (m *Metrics) SetRetryPending(string, string, string, int) {}
