package marketfetch

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	storeBytes                     *prometheus.GaugeVec
	storeRows                      *prometheus.GaugeVec
	storeRowsCapped                *prometheus.GaugeVec
	storeOldest                    *prometheus.GaugeVec
	storeStatsHealthy              *prometheus.GaugeVec
	storeStatsLastSuccess          *prometheus.GaugeVec
	maintenanceDeleted             *prometheus.GaugeVec
	maintenanceDuration            *prometheus.GaugeVec
	maintenancePasses              *prometheus.CounterVec
	maintenanceLastSuccess         *prometheus.GaugeVec
	schedulerLastSuccess           *prometheus.GaugeVec
	assignmentRequired             *prometheus.GaugeVec
	assignmentActive               *prometheus.GaugeVec
	assignmentLastSuccess          *prometheus.GaugeVec
	assignmentHealthy              *prometheus.GaugeVec
	assignmentFailure              *prometheus.GaugeVec
	assignmentPending              *prometheus.GaugeVec
	assignmentPendingSince         *prometheus.GaugeVec
	timerAvailable                 *prometheus.GaugeVec
	timerCapacityTotal             *prometheus.GaugeVec
	timerCapacityRequired          *prometheus.GaugeVec
	timerCapacityActive            *prometheus.GaugeVec
	timerCapacityHeadroom          *prometheus.GaugeVec
	assignmentErrors               *prometheus.CounterVec
	periodPending                  *prometheus.GaugeVec
	periodPendingMu                sync.Mutex
	periodPendingFrequencies       map[string]map[string]struct{}
	periodReportRetry              *prometheus.CounterVec
	periodStorageStatusProbes      *prometheus.CounterVec
	periodStorageReconcileDuration *prometheus.GaugeVec
	periodStorageReconcileHealthy  *prometheus.GaugeVec
	periodFailurePending           *prometheus.GaugeVec
	periodFailureMissed            *prometheus.CounterVec
	periodFailureReportRetry       *prometheus.CounterVec
	feedResults                    *prometheus.CounterVec
	configuredGroups               *prometheus.GaugeVec
	configuredGroupIDs             *prometheus.GaugeVec
	egressFunctions                *prometheus.GaugeVec
	instrumentActive               *prometheus.GaugeVec
	instrumentExchange             *prometheus.GaugeVec
	instrumentLastSnapshot         *prometheus.GaugeVec
	feedObservations               *prometheus.CounterVec
}

type FeedMetric struct {
	MarketID, RouteID, ProviderID, FeedKind, BatchKind, Result string
	InstrumentType, SourceID, Frequency                        string
	SourceKind, Transport, RemoteHost, SCFRegion, EgressScope  string
	EgressIP, Unit, ErrorKind, HistoryWindow, CalendarID       string
	RemotePort, ConnectionAttempt, Rows, FallbackRank          int
	GroupID, GroupCount                                        int
}

// SetDatasetRunObserver is retained as a no-op for the manual/catchup helper
// path. Timer-triggered realtime freshness comes from Storage, not completion
// events.
func (m *Metrics) SetDatasetRunObserver(any) {}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		storeBytes:                     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_database_bytes", Help: "Collector SQLite database and WAL file bytes."}, []string{"kind"}),
		storeRows:                      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_rows", Help: "Space-scoped Collector table rows; lower bound when store_rows_capped is one."}, []string{"space_id", "table"}),
		storeRowsCapped:                prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_rows_capped", Help: "Whether the row observation exceeded its bounded scan limit."}, []string{"space_id", "table"}),
		storeOldest:                    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_oldest_timestamp_seconds", Help: "Oldest pending/dispatched retry creation time, waiting period time, or terminal snapshot cleanup candidate period time; zero when empty."}, []string{"space_id", "kind"}),
		storeStatsHealthy:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_stats_healthy", Help: "Whether the latest complete store statistics observation succeeded."}, []string{"space_id"}),
		storeStatsLastSuccess:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_stats_last_success_timestamp_seconds", Help: "Last complete store statistics observation timestamp."}, []string{"space_id"}),
		maintenanceDeleted:             prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_maintenance_deleted_rows", Help: "Committed physical deletes in the latest maintenance pass, with readiness, snapshot, Storage state and Timer manifest rows counted separately."}, []string{"space_id", "table"}),
		maintenanceDuration:            prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_maintenance_duration_seconds", Help: "Elapsed duration of the latest process-level maintenance pass."}, nil),
		maintenancePasses:              prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_maintenance_passes_total", Help: "Process-level maintenance passes by bounded outcome."}, []string{"status"}),
		maintenanceLastSuccess:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_maintenance_last_success_timestamp_seconds", Help: "Last fully successful process-level maintenance pass timestamp."}, nil),
		schedulerLastSuccess:           prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_scheduler_last_success_timestamp_seconds", Help: "Last successful Scheduler planning pass or persisted SCF dispatch timestamp."}, []string{"space_id", "stage"}),
		assignmentRequired:             prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_required", Help: "Required Timer SCF assignments."}, []string{"space_id", "frequency"}),
		assignmentActive:               prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_active", Help: "Active Timer SCF assignments."}, []string{"space_id", "frequency"}),
		assignmentLastSuccess:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", Help: "Last successful Timer assignment reconciliation timestamp."}, []string{"space_id"}),
		assignmentHealthy:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_healthy", Help: "Whether the latest Timer assignment reconciliation completed (1 healthy, 0 failed)."}, []string{"space_id"}),
		assignmentFailure:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_failure", Help: "Current Timer coordination failure reason (1 current, 0 inactive)."}, []string{"space_id", "reason"}),
		assignmentPending:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_pending", Help: "Whether a Timer assignment reconciliation batch is pending or running (1 pending, 0 idle)."}, []string{"space_id"}),
		assignmentPendingSince:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_pending_since_timestamp_seconds", Help: "Unix timestamp when the current Timer assignment reconciliation batch started."}, []string{"space_id"}),
		timerAvailable:                 prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_available", Help: "Tencent Timer trigger availability for Collector nodes (1 available, 0 unavailable, -1 unknown)."}, []string{"space_id", "node_id", "enabled"}),
		timerCapacityTotal:             prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_total", Help: "Total Timer SCF nodes currently visible to Collector."}, []string{"space_id"}),
		timerCapacityRequired:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_required", Help: "Timer SCF nodes required by the current dataset/frequency shard plan."}, []string{"space_id"}),
		timerCapacityActive:            prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_active", Help: "Timer SCF nodes included in the current active shard assignment."}, []string{"space_id"}),
		timerCapacityHeadroom:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_timer_capacity_headroom", Help: "Timer SCF capacity headroom: total nodes minus required nodes; negative means capacity is insufficient."}, []string{"space_id"}),
		assignmentErrors:               prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_fetch_assignment_errors_total", Help: "Timer assignment reconciliation errors."}, []string{"space_id", "reason"}),
		periodPending:                  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_period_pending_total", Help: "Collector periods waiting to be reported."}, []string{"space_id", "frequency"}),
		periodPendingFrequencies:       make(map[string]map[string]struct{}),
		periodReportRetry:              prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_period_report_retry_total", Help: "Collector period report attempts that need retry."}, []string{"space_id", "frequency"}),
		periodStorageStatusProbes:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_period_storage_status_probes_total", Help: "Storage dataset period status probes by Space and bounded transport outcome."}, []string{"space_id", "result"}),
		periodStorageReconcileDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_period_storage_reconcile_duration_seconds", Help: "Elapsed time of the latest bounded Storage period reconciliation pass by Space."}, []string{"space_id"}),
		periodStorageReconcileHealthy:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_period_storage_reconcile_healthy", Help: "Whether the latest bounded Storage period reconciliation pass completed successfully."}, []string{"space_id"}),
		periodFailurePending:           prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_period_failure_pending", Help: "Permanent period failure receipts awaiting an authoritative Storage disposition."}, []string{"space_id", "frequency"}),
		periodFailureMissed:            prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_period_failure_missed_deadline_total", Help: "Permanent period failure receipts explicitly rejected after the Storage deadline."}, []string{"space_id", "frequency"}),
		periodFailureReportRetry:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_period_failure_report_retries_total", Help: "Permanent period failure report attempts that need retry."}, []string{"space_id", "frequency", "outcome"}),
		feedResults:                    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_feed_results_total", Help: "Bounded market feed outcomes; subject, function, IP, and candidate chain are intentionally excluded."}, []string{"market_id", "route_id", "provider_id", "feed_kind", "group_id", "batch_kind", "result"}),
		configuredGroups:               prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_configured_groups", Help: "Expected and actual Timer group counts."}, []string{"market_id", "route_id", "kind"}),
		configuredGroupIDs:             prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_configured_group_id", Help: "Configured Timer Group identity counts; a value other than one means missing or duplicate identity."}, []string{"market_id", "route_id", "group_id"}),
		egressFunctions:                prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_egress_functions", Help: "Expected, returned, non-empty-IP, and distinct-IP function counts."}, []string{"market_id", "route_id", "kind"}),
		instrumentActive:               prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_instrument_active", Help: "Active instruments in the latest complete snapshot."}, []string{"market_id", "route_id", "provider_id", "result"}),
		instrumentExchange:             prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_instrument_exchange", Help: "Instrument count by bounded exchange in the latest complete snapshot."}, []string{"market_id", "route_id", "provider_id", "exchange"}),
		instrumentLastSnapshot:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_instrument_last_snapshot_timestamp_seconds", Help: "Fetch timestamp of the latest complete instrument snapshot."}, []string{"market_id", "route_id", "provider_id", "result"}),
		feedObservations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_feed_observations_total", Help: "Bounded market feed observations with source, route, and egress dimensions."}, []string{
			"market_id", "instrument_type", "provider_id", "source_id", "frequency", "source_kind", "transport",
			"remote_host", "remote_port", "scf_region", "egress_scope", "egress_ip", "connection_attempt",
			"rows", "unit", "fallback_rank", "error_kind", "history_window", "calendar_id", "result",
		}),
	}
	metrics.storeBytes = registerGaugeVec(reg, metrics.storeBytes)
	metrics.storeRows = registerGaugeVec(reg, metrics.storeRows)
	metrics.storeRowsCapped = registerGaugeVec(reg, metrics.storeRowsCapped)
	metrics.storeOldest = registerGaugeVec(reg, metrics.storeOldest)
	metrics.storeStatsHealthy = registerGaugeVec(reg, metrics.storeStatsHealthy)
	metrics.storeStatsLastSuccess = registerGaugeVec(reg, metrics.storeStatsLastSuccess)
	metrics.maintenanceDeleted = registerGaugeVec(reg, metrics.maintenanceDeleted)
	metrics.maintenanceDuration = registerGaugeVec(reg, metrics.maintenanceDuration)
	metrics.maintenancePasses = registerCounterVec(reg, metrics.maintenancePasses)
	metrics.maintenanceLastSuccess = registerGaugeVec(reg, metrics.maintenanceLastSuccess)
	metrics.schedulerLastSuccess = registerGaugeVec(reg, metrics.schedulerLastSuccess)
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
	metrics.periodStorageStatusProbes = registerCounterVec(reg, metrics.periodStorageStatusProbes)
	metrics.periodStorageReconcileDuration = registerGaugeVec(reg, metrics.periodStorageReconcileDuration)
	metrics.periodStorageReconcileHealthy = registerGaugeVec(reg, metrics.periodStorageReconcileHealthy)
	metrics.periodFailurePending = registerGaugeVec(reg, metrics.periodFailurePending)
	metrics.periodFailureMissed = registerCounterVec(reg, metrics.periodFailureMissed)
	metrics.periodFailureReportRetry = registerCounterVec(reg, metrics.periodFailureReportRetry)
	metrics.feedResults = registerCounterVec(reg, metrics.feedResults)
	metrics.configuredGroups = registerGaugeVec(reg, metrics.configuredGroups)
	metrics.configuredGroupIDs = registerGaugeVec(reg, metrics.configuredGroupIDs)
	metrics.egressFunctions = registerGaugeVec(reg, metrics.egressFunctions)
	metrics.instrumentActive = registerGaugeVec(reg, metrics.instrumentActive)
	metrics.instrumentExchange = registerGaugeVec(reg, metrics.instrumentExchange)
	metrics.instrumentLastSnapshot = registerGaugeVec(reg, metrics.instrumentLastSnapshot)
	metrics.feedObservations = registerCounterVec(reg, metrics.feedObservations)
	return metrics
}

type operationalStatsSource interface {
	OperationalStats(context.Context, string, time.Time, int) (store.OperationalStats, error)
}

var operationalTableKinds = []string{"runs", "instances", "write_targets", "batches", "retry", "period_snapshot"}
var maintenanceTableKinds = []string{"batch_items", "batches", "retry", "write_targets", "instances", "runs", "period_readiness", "period_snapshot", "period_state", "period_manifest"}

// RefreshOperationalStats publishes only a complete observation. On failure it
// retains last-good values and marks them stale rather than inventing zeros.
func (m *Metrics) RefreshOperationalStats(ctx context.Context, source operationalStatsSource, spaceID string, before, now time.Time) error {
	if m == nil {
		return nil
	}
	stats, err := source.OperationalStats(ctx, spaceID, before, store.MaxOperationalStatsRows)
	if err != nil {
		m.storeStatsHealthy.WithLabelValues(spaceID).Set(0)
		return err
	}
	m.storeBytes.WithLabelValues("db").Set(float64(stats.DBBytes))
	m.storeBytes.WithLabelValues("wal").Set(float64(stats.WALBytes))
	for _, kind := range operationalTableKinds {
		row := stats.Rows[kind]
		m.storeRows.WithLabelValues(spaceID, kind).Set(float64(row.Count))
		capped := 0.0
		if row.Capped {
			capped = 1
		}
		m.storeRowsCapped.WithLabelValues(spaceID, kind).Set(capped)
	}
	for kind, oldest := range map[string]time.Time{
		"retry_pending": stats.OldestPendingRetry, "retry_dispatched": stats.OldestDispatchedRetry,
		"period_waiting": stats.OldestWaitingPeriod, "snapshot_cleanup": stats.OldestSnapshotCleanupCandidate,
	} {
		value := 0.0
		if !oldest.IsZero() {
			value = float64(oldest.Unix())
		}
		m.storeOldest.WithLabelValues(spaceID, kind).Set(value)
	}
	m.storeStatsHealthy.WithLabelValues(spaceID).Set(1)
	m.storeStatsLastSuccess.WithLabelValues(spaceID).Set(float64(now.Unix()))
	return nil
}

func (m *Metrics) ObserveMaintenanceDeletes(spaceID string, deleted map[string]int64) {
	if m == nil {
		return
	}
	for _, kind := range maintenanceTableKinds {
		m.maintenanceDeleted.WithLabelValues(spaceID, kind).Set(float64(max(0, deleted[kind])))
	}
}

func (m *Metrics) ObserveMaintenancePass(duration time.Duration, err error) {
	if m == nil {
		return
	}
	m.maintenanceDuration.WithLabelValues().Set(duration.Seconds())
	status := "success"
	if err != nil {
		status = "error"
	} else {
		m.maintenanceLastSuccess.WithLabelValues().Set(float64(time.Now().Unix()))
	}
	m.maintenancePasses.WithLabelValues(status).Inc()
}

func (m *Metrics) ObservePeriodStorageProbe(spaceID, result string) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	switch result {
	case "success", "error", "canceled":
	default:
		result = "error"
	}
	m.periodStorageStatusProbes.WithLabelValues(strings.TrimSpace(spaceID), result).Inc()
}

func (m *Metrics) ObservePeriodStorageReconcile(spaceID string, duration time.Duration, err error) {
	if m == nil || strings.TrimSpace(spaceID) == "" || duration < 0 {
		return
	}
	m.periodStorageReconcileDuration.WithLabelValues(strings.TrimSpace(spaceID)).Set(duration.Seconds())
	healthy := 1.0
	if err != nil {
		healthy = 0
	}
	m.periodStorageReconcileHealthy.WithLabelValues(strings.TrimSpace(spaceID)).Set(healthy)
}

func (m *Metrics) ObserveSchedulerSuccess(spaceID, stage string, now time.Time) {
	if m == nil || (stage != "planning" && stage != "dispatch") || strings.TrimSpace(spaceID) == "" || now.IsZero() {
		return
	}
	m.schedulerLastSuccess.WithLabelValues(spaceID, stage).Set(float64(now.Unix()))
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
func (m *Metrics) ObserveAssignment(spaceID, frequency string, required, active int, reconciledAt int64) {
	m.ObserveAssignmentDesired(spaceID, frequency, required, active)
	m.ObserveAssignmentSuccess(spaceID, reconciledAt)
}

// ObserveAssignmentDesired records the state requested from CloudNode. It is
// deliberately separate from success: SubmitRuntimeConfigs is asynchronous,
// so accepting a job must not advance the last-success timestamp.
func (m *Metrics) ObserveAssignmentDesired(spaceID, frequency string, required, active int) {
	if m == nil {
		return
	}
	frequency = boundedPeriodFrequency(frequency)
	if required < 0 {
		required = 0
	}
	if active < 0 {
		active = 0
	}
	m.assignmentRequired.WithLabelValues(spaceID, frequency).Set(float64(required))
	m.assignmentActive.WithLabelValues(spaceID, frequency).Set(float64(active))
}

// ObserveAssignmentRequired updates the current desired shard count without
// discarding the last confirmed active assignment during a reconciliation.
func (m *Metrics) ObserveAssignmentRequired(spaceID, frequency string, required int) {
	if m == nil {
		return
	}
	frequency = boundedPeriodFrequency(frequency)
	if required < 0 {
		required = 0
	}
	m.assignmentRequired.WithLabelValues(spaceID, frequency).Set(float64(required))
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

// ObservePeriodPendingSnapshot aggregates pending reports without exposing
// task-owned Dataset IDs as Prometheus labels and resets disappeared frequencies.
func (m *Metrics) ObservePeriodPendingSnapshot(spaceID string, pendingByFrequency map[string]int) {
	spaceID = strings.TrimSpace(spaceID)
	if m == nil || spaceID == "" {
		return
	}
	current := make(map[string]int, len(pendingByFrequency))
	for rawFrequency, count := range pendingByFrequency {
		frequency := boundedPeriodFrequency(rawFrequency)
		if frequency == "unknown" {
			continue
		}
		current[frequency] += max(0, count)
	}
	m.periodPendingMu.Lock()
	defer m.periodPendingMu.Unlock()
	previous := m.periodPendingFrequencies[spaceID]
	for frequency := range previous {
		if _, exists := current[frequency]; !exists {
			m.periodPending.WithLabelValues(spaceID, frequency).Set(0)
		}
	}
	next := make(map[string]struct{}, len(current))
	for frequency, count := range current {
		m.periodPending.WithLabelValues(spaceID, frequency).Set(float64(count))
		if count > 0 {
			next[frequency] = struct{}{}
		}
	}
	if len(next) == 0 {
		delete(m.periodPendingFrequencies, spaceID)
	} else {
		m.periodPendingFrequencies[spaceID] = next
	}
}

func (m *Metrics) ObservePeriodReportRetry(spaceID, frequency string) {
	if m == nil {
		return
	}
	frequency = boundedPeriodFrequency(frequency)
	if frequency == "unknown" || strings.TrimSpace(spaceID) == "" {
		return
	}
	m.periodReportRetry.WithLabelValues(strings.TrimSpace(spaceID), frequency).Inc()
}

func (m *Metrics) ObservePeriodFailurePending(spaceID, frequency string, pending int) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	frequency = boundedPeriodFrequency(frequency)
	if frequency == "unknown" {
		return
	}
	if pending < 0 {
		pending = 0
	}
	m.periodFailurePending.WithLabelValues(strings.TrimSpace(spaceID), frequency).Set(float64(pending))
}

func (m *Metrics) ObservePeriodFailureMissedDeadline(spaceID, frequency string) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	frequency = boundedPeriodFrequency(frequency)
	if frequency == "unknown" {
		return
	}
	m.periodFailureMissed.WithLabelValues(strings.TrimSpace(spaceID), frequency).Inc()
}

func (m *Metrics) ObservePeriodFailureReportRetry(spaceID, frequency, outcome string) {
	if m == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	frequency = boundedPeriodFrequency(frequency)
	if frequency == "unknown" {
		return
	}
	outcome = boundedValue(outcome, []string{"timeout", "canceled", "storage_error", "storage_client_error"}, "unknown")
	if outcome == "unknown" {
		return
	}
	m.periodFailureReportRetry.WithLabelValues(strings.TrimSpace(spaceID), frequency, outcome).Inc()
}

func boundedPeriodFrequency(value string) string {
	frequency, err := marketdata.ParseFrequency(strings.TrimSpace(value))
	if err != nil {
		return "unknown"
	}
	return string(frequency)
}

// ObserveFeedResult exposes only dimensions with a bounded operational
// vocabulary. Per-subject routing evidence belongs in structured logs.
func (m *Metrics) ObserveFeedResult(value FeedMetric) {
	if m == nil || value.GroupCount <= 0 || value.GroupID < 0 || value.GroupID >= value.GroupCount {
		return
	}
	marketID, routeID := boundedMarketRoute(value.MarketID, value.RouteID)
	providerID := boundedValue(value.ProviderID, []string{"sina", "tencent", "eastmoney", "baidu", "binance", "none"}, "unknown")
	feedKind := boundedValue(value.FeedKind, []string{"kline", "instrument", "egress"}, "unknown")
	batchKind := boundedValue(value.BatchKind, []string{"realtime", "backfill", "gap_repair", "catchup", "instrument_snapshot"}, "unknown")
	result := boundedValue(value.Result, []string{"success", "fallback", "http_429", "http_5xx", "timeout", "rate_limited", "invalid", "storage_error", "no_candidate"}, "unknown")
	m.feedResults.WithLabelValues(marketID, routeID, providerID, feedKind, strconv.Itoa(value.GroupID), batchKind, result).Inc()
	m.feedObservations.WithLabelValues(
		marketID,
		boundedMetricField(value.InstrumentType, "unknown"),
		providerID,
		boundedMetricField(value.SourceID, "unknown"),
		boundedMetricField(value.Frequency, "unknown"),
		boundedMetricField(value.SourceKind, "provider"),
		boundedMetricField(value.Transport, "unknown"),
		boundedMetricField(value.RemoteHost, "unknown"),
		strconv.Itoa(max(value.RemotePort, 0)),
		boundedMetricField(value.SCFRegion, "unknown"),
		boundedMetricField(value.EgressScope, "unknown"),
		boundedMetricField(value.EgressIP, "unknown"),
		strconv.Itoa(max(value.ConnectionAttempt, 0)),
		strconv.Itoa(max(value.Rows, 0)),
		boundedMetricField(value.Unit, "rows"),
		strconv.Itoa(max(value.FallbackRank, 0)),
		boundedMetricField(value.ErrorKind, "unknown"),
		boundedMetricField(value.HistoryWindow, "live"),
		boundedMetricField(value.CalendarID, "none"),
		result,
	).Inc()
}

func boundedMetricField(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return fallback
	}
	return value
}

func (m *Metrics) ObserveConfiguredGroups(marketID, routeID string, expected, actual int) {
	if m == nil {
		return
	}
	marketID, routeID = boundedMarketRoute(marketID, routeID)
	m.configuredGroups.WithLabelValues(marketID, routeID, "expected").Set(float64(max(expected, 0)))
	m.configuredGroups.WithLabelValues(marketID, routeID, "actual").Set(float64(max(actual, 0)))
}

// ResetConfiguredGroupIDs removes the previous release's identity set before
// publishing the newly confirmed assignment. Keeping this separate from the
// expected/actual count lets Monitor detect a hole or duplicate GroupID.
func (m *Metrics) ResetConfiguredGroupIDs(marketID, routeID string) {
	if m == nil {
		return
	}
	marketID, routeID = boundedMarketRoute(marketID, routeID)
	m.configuredGroupIDs.DeletePartialMatch(prometheus.Labels{"market_id": marketID, "route_id": routeID})
}

// ObserveConfiguredGroupID adds one assignment occurrence. Add, rather than
// Set, is intentional: duplicate GroupIDs must remain visible as value 2.
func (m *Metrics) ObserveConfiguredGroupID(marketID, routeID string, groupID int) {
	if m == nil || groupID < 0 {
		return
	}
	marketID, routeID = boundedMarketRoute(marketID, routeID)
	m.configuredGroupIDs.WithLabelValues(marketID, routeID, strconv.Itoa(groupID)).Add(1)
}

// ObserveEgressDiagnostic records optional probe facts. These values are
// informational and must never be interpreted as a release or activation gate.
func (m *Metrics) ObserveEgressDiagnostic(marketID, routeID string, expected, results, nonEmpty, distinct int) {
	if m == nil {
		return
	}
	marketID, routeID = boundedMarketRoute(marketID, routeID)
	for kind, value := range map[string]int{"expected": expected, "result": results, "non_empty_ip": nonEmpty, "distinct_ip": distinct} {
		m.egressFunctions.WithLabelValues(marketID, routeID, kind).Set(float64(max(value, 0)))
	}
}

func (m *Metrics) ObserveInstrumentSnapshot(marketID, routeID, providerID, result string, active int, exchanges map[string]int, fetchedAt time.Time) {
	if m == nil {
		return
	}
	marketID, routeID = boundedMarketRoute(marketID, routeID)
	providerID = boundedValue(providerID, []string{"sina", "eastmoney", "baidu", "binance", "tencent", "storage_cache", "none"}, "unknown")
	result = boundedValue(result, []string{"success", "fallback", "incomplete", "stale", "invalid"}, "unknown")
	m.instrumentActive.WithLabelValues(marketID, routeID, providerID, result).Set(float64(max(active, 0)))
	if !fetchedAt.IsZero() {
		m.instrumentLastSnapshot.WithLabelValues(marketID, routeID, providerID, result).Set(float64(fetchedAt.UTC().Unix()))
	}
	for exchange, count := range exchanges {
		exchange = boundedValue(exchange, []string{"XSHG", "XSHE", "XBSE", "SPOT", "SWAP"}, "unknown")
		m.instrumentExchange.WithLabelValues(marketID, routeID, providerID, exchange).Set(float64(max(count, 0)))
	}
}

func boundedMarketRoute(marketID, routeID string) (string, string) {
	marketID = boundedValue(marketID, []string{"stockcn", "crypto"}, "unknown")
	routeID = strings.TrimSpace(routeID)
	allowed := routeID == StockCNRouteID || routeID == "stockcn_instrument_v1" || routeID == "binance_spot_instrument_v1" || routeID == "binance_swap_instrument_v1"
	if !allowed {
		for _, product := range []string{"spot", "swap"} {
			prefix := "binance_" + product + "_kline_"
			if strings.HasPrefix(routeID, prefix) {
				frequency := strings.TrimPrefix(routeID, prefix)
				if parsed, err := marketdata.ParseFrequency(frequency); err == nil && string(parsed) == frequency {
					allowed = true
				}
				break
			}
		}
	}
	if !allowed {
		routeID = "unknown"
	}
	return marketID, routeID
}

func boundedValue(value string, allowed []string, fallback string) string {
	value = strings.TrimSpace(value)
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

// Observe and SetRetryPending remain no-ops for the retired completion-based
// realtime scheduler. They keep the manual catchup package boundary source
// compatible without registering legacy high-cardinality metrics.
func (m *Metrics) Observe(string, any)                         {}
func (m *Metrics) SetRetryPending(string, string, string, int) {}
