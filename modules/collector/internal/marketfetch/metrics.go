package marketfetch

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	assignmentRequired     *prometheus.GaugeVec
	assignmentActive       *prometheus.GaugeVec
	assignmentLastSuccess  *prometheus.GaugeVec
	assignmentHealthy      *prometheus.GaugeVec
	assignmentFailure      *prometheus.GaugeVec
	assignmentPending      *prometheus.GaugeVec
	assignmentPendingSince *prometheus.GaugeVec
	timerAvailable         *prometheus.GaugeVec
	timerCapacityTotal     *prometheus.GaugeVec
	timerCapacityRequired  *prometheus.GaugeVec
	timerCapacityActive    *prometheus.GaugeVec
	timerCapacityHeadroom  *prometheus.GaugeVec
	assignmentErrors       *prometheus.CounterVec
	periodPending          *prometheus.GaugeVec
	periodReportRetry      *prometheus.CounterVec
}

// SetDatasetRunObserver is retained as a no-op for the manual/catchup helper
// path. Timer-triggered realtime freshness comes from Storage, not completion
// events.
func (m *Metrics) SetDatasetRunObserver(any) {}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		assignmentRequired:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_required", Help: "Required Timer SCF assignments."}, []string{"space_id", "dataset_id", "frequency"}),
		assignmentActive:       prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_active", Help: "Active Timer SCF assignments."}, []string{"space_id", "dataset_id", "frequency"}),
		assignmentLastSuccess:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", Help: "Last successful Timer assignment reconciliation timestamp."}, []string{"space_id"}),
		assignmentHealthy:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_healthy", Help: "Whether the latest Timer assignment reconciliation completed (1 healthy, 0 failed)."}, []string{"space_id"}),
		assignmentFailure:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_failure", Help: "Current Timer coordination failure reason (1 current, 0 inactive)."}, []string{"space_id", "reason"}),
		assignmentPending:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_pending", Help: "Whether a Timer assignment reconciliation batch is pending or running (1 pending, 0 idle)."}, []string{"space_id"}),
		assignmentPendingSince: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_pending_since_timestamp_seconds", Help: "Unix timestamp when the current Timer assignment reconciliation batch started."}, []string{"space_id"}),
		timerAvailable:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_available", Help: "Tencent Timer trigger availability for Collector nodes (1 available, 0 unavailable, -1 unknown)."}, []string{"space_id", "node_id", "enabled"}),
		timerCapacityTotal:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_total", Help: "Total Timer SCF nodes currently visible to Collector."}, []string{"space_id"}),
		timerCapacityRequired:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_required", Help: "Timer SCF nodes required by the current dataset/frequency shard plan."}, []string{"space_id"}),
		timerCapacityActive:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_active", Help: "Timer SCF nodes included in the current active shard assignment."}, []string{"space_id"}),
		timerCapacityHeadroom:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_headroom", Help: "Timer SCF capacity headroom: total nodes minus required nodes; negative means capacity is insufficient."}, []string{"space_id"}),
		assignmentErrors:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_fetch_assignment_errors_total", Help: "Timer assignment reconciliation errors."}, []string{"space_id", "reason"}),
		periodPending:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_period_pending_total", Help: "Collector periods waiting to be reported."}, []string{"dataset", "frequency"}),
		periodReportRetry:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_period_report_retry_total", Help: "Collector period report attempts that need retry."}, []string{"dataset", "frequency"}),
	}
	metrics.assignmentRequired = registerGaugeVec(reg, metrics.assignmentRequired)
	metrics.assignmentActive = registerGaugeVec(reg, metrics.assignmentActive)
	metrics.assignmentLastSuccess = registerGaugeVec(reg, metrics.assignmentLastSuccess)
	metrics.assignmentHealthy = registerGaugeVec(reg, metrics.assignmentHealthy)
	metrics.assignmentFailure = registerGaugeVec(reg, metrics.assignmentFailure)
	metrics.assignmentPending = registerGaugeVec(reg, metrics.assignmentPending)
	metrics.assignmentPendingSince = registerGaugeVec(reg, metrics.assignmentPendingSince)
	metrics.timerAvailable = registerGaugeVec(reg, metrics.timerAvailable)
	metrics.timerCapacityTotal = registerGaugeVec(reg, metrics.timerCapacityTotal)
	metrics.timerCapacityRequired = registerGaugeVec(reg, metrics.timerCapacityRequired)
	metrics.timerCapacityActive = registerGaugeVec(reg, metrics.timerCapacityActive)
	metrics.timerCapacityHeadroom = registerGaugeVec(reg, metrics.timerCapacityHeadroom)
	metrics.assignmentErrors = registerCounterVec(reg, metrics.assignmentErrors)
	metrics.periodPending = registerGaugeVec(reg, metrics.periodPending)
	metrics.periodReportRetry = registerCounterVec(reg, metrics.periodReportRetry)
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

// ObserveAssignmentRequired updates the current desired shard count without
// discarding the last confirmed active assignment during a reconciliation.
func (m *Metrics) ObserveAssignmentRequired(spaceID, datasetID, frequency string, required int) {
	if m == nil {
		return
	}
	if required < 0 {
		required = 0
	}
	m.assignmentRequired.WithLabelValues(spaceID, datasetID, frequency).Set(float64(required))
}

// ResetAssignmentRequirements removes desired scopes that disappeared while
// retaining the last active assignment until a new plan has been built.
func (m *Metrics) ResetAssignmentRequirements(spaceID string) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	m.assignmentRequired.DeletePartialMatch(prometheus.Labels{"space_id": spaceID})
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
	m.ClearAssignmentFailure(spaceID)
}

// ObserveAssignmentPending exposes the asynchronous CloudNode batch window so
// Monitor can distinguish a short reconciliation from a failed coordination.
func (m *Metrics) ObserveAssignmentPending(spaceID string, pending bool, since time.Time) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	if !pending {
		m.assignmentPending.WithLabelValues(spaceID).Set(0)
		m.assignmentPendingSince.WithLabelValues(spaceID).Set(0)
		return
	}
	m.assignmentPending.WithLabelValues(spaceID).Set(1)
	if !since.IsZero() {
		m.assignmentPendingSince.WithLabelValues(spaceID).Set(float64(since.UTC().Unix()))
	}
}

func (m *Metrics) ObserveAssignmentFailure(spaceID, reason string) {
	if m == nil {
		return
	}
	m.assignmentHealthy.WithLabelValues(spaceID).Set(0)
	reason = normalizeAssignmentFailureReason(reason)
	m.ClearAssignmentFailure(spaceID)
	m.assignmentFailure.WithLabelValues(spaceID, reason).Set(1)
	m.ObserveAssignmentError(spaceID, reason)
}

// ClearAssignmentFailure clears every bounded failure label. Setting the
// values to zero (rather than deleting them) lets Monitor stop using the last
// scraped failure immediately.
func (m *Metrics) ClearAssignmentFailure(spaceID string) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	for _, reason := range assignmentFailureReasons {
		m.assignmentFailure.WithLabelValues(spaceID, reason).Set(0)
	}
}

func (m *Metrics) ObserveAssignmentError(spaceID, reason string) {
	if m == nil {
		return
	}
	m.assignmentErrors.WithLabelValues(spaceID, normalizeAssignmentFailureReason(reason)).Inc()
}

var assignmentFailureReasons = []string{
	"capacity", "rules", "symbols", "dns", "cloudnode", "environment", "task_instances", "submit_timeout",
}

func normalizeAssignmentFailureReason(reason string) string {
	reason = strings.TrimSpace(reason)
	for _, allowed := range assignmentFailureReasons {
		if reason == allowed {
			return reason
		}
	}
	return "cloudnode"
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

// ObserveTimerCapacity publishes the fleet size and the current shard-plan
// demand together. Assignment gauges are split by dataset/frequency for
// diagnosis; these space-level values make an imminent or actual capacity
// shortfall directly alertable without having Monitor reconstruct the plan.
func (m *Metrics) ObserveTimerCapacity(spaceID string, total, required, active int) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	if total < 0 {
		total = 0
	}
	if required < 0 {
		required = 0
	}
	if active < 0 {
		active = 0
	}
	m.timerCapacityTotal.WithLabelValues(spaceID).Set(float64(total))
	m.timerCapacityRequired.WithLabelValues(spaceID).Set(float64(required))
	m.timerCapacityActive.WithLabelValues(spaceID).Set(float64(active))
	m.timerCapacityHeadroom.WithLabelValues(spaceID).Set(float64(total - required))
}

// ObservePeriodPending exposes the number of report-pending periods for one
// Dataset/frequency pair. Labels intentionally omit subject and period to keep
// the metric useful for a single-user deployment without high cardinality.
func (m *Metrics) ObservePeriodPending(dataset, frequency string, pending int) {
	if m == nil {
		return
	}
	if pending < 0 {
		pending = 0
	}
	m.periodPending.WithLabelValues(dataset, frequency).Set(float64(pending))
}

func (m *Metrics) ObservePeriodReportRetry(dataset, frequency string) {
	if m == nil {
		return
	}
	m.periodReportRetry.WithLabelValues(dataset, frequency).Inc()
}

// Observe and SetRetryPending remain no-ops for the retired completion-based
// realtime scheduler. They keep the manual catchup package boundary source
// compatible without registering legacy high-cardinality metrics.
func (m *Metrics) Observe(string, any)                         {}
func (m *Metrics) SetRetryPending(string, string, string, int) {}
