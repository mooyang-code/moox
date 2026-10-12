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
	storeBytes               *prometheus.GaugeVec
	storeRows                *prometheus.GaugeVec
	storeRowsCapped          *prometheus.GaugeVec
	storeStatsHealthy        *prometheus.GaugeVec
	storeStatsLastSuccess    *prometheus.GaugeVec
	maintenanceDeleted       *prometheus.GaugeVec
	maintenanceDuration      *prometheus.GaugeVec
	maintenancePasses        *prometheus.CounterVec
	maintenanceLastSuccess   *prometheus.GaugeVec
	assignmentLastSuccess    *prometheus.GaugeVec
	assignmentHealthy        *prometheus.GaugeVec
	assignmentFailure        *prometheus.GaugeVec
	assignmentPending        *prometheus.GaugeVec
	assignmentPendingSince   *prometheus.GaugeVec
	assignmentErrors         *prometheus.CounterVec
	periodPending            *prometheus.GaugeVec
	periodPendingMu          sync.Mutex
	periodPendingFrequencies map[string]map[string]struct{}
	periodReportRetry        *prometheus.CounterVec
	feedResults              *prometheus.CounterVec
	feedObservations         *prometheus.CounterVec
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
		storeBytes:               prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_database_bytes", Help: "Collector SQLite database and WAL file bytes."}, []string{"kind"}),
		storeRows:                prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_rows", Help: "Space-scoped Collector table rows; lower bound when store_rows_capped is one."}, []string{"space_id", "table"}),
		storeRowsCapped:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_rows_capped", Help: "Whether the row observation exceeded its bounded scan limit."}, []string{"space_id", "table"}),
		storeStatsHealthy:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_stats_healthy", Help: "Whether the latest complete store statistics observation succeeded."}, []string{"space_id"}),
		storeStatsLastSuccess:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_store_stats_last_success_timestamp_seconds", Help: "Last complete store statistics observation timestamp."}, []string{"space_id"}),
		maintenanceDeleted:       prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_maintenance_deleted_rows", Help: "Committed physical deletes in the latest maintenance pass, with readiness, snapshot, Storage state and Timer manifest rows counted separately."}, []string{"space_id", "table"}),
		maintenanceDuration:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_maintenance_duration_seconds", Help: "Elapsed duration of the latest process-level maintenance pass."}, nil),
		maintenancePasses:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_maintenance_passes_total", Help: "Process-level maintenance passes by bounded outcome."}, []string{"status"}),
		maintenanceLastSuccess:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_maintenance_last_success_timestamp_seconds", Help: "Last fully successful process-level maintenance pass timestamp."}, nil),
		assignmentLastSuccess:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_assignment_last_success_timestamp_seconds", Help: "Last successful Timer assignment reconciliation timestamp."}, []string{"space_id"}),
		assignmentHealthy:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_healthy", Help: "Whether the latest Timer assignment reconciliation completed (1 healthy, 0 failed)."}, []string{"space_id"}),
		assignmentFailure:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_failure", Help: "Current Timer coordination failure reason (1 current, 0 inactive)."}, []string{"space_id", "reason"}),
		assignmentPending:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_pending", Help: "Whether a Timer assignment reconciliation batch is pending or running (1 pending, 0 idle)."}, []string{"space_id"}),
		assignmentPendingSince:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_coordination_pending_since_timestamp_seconds", Help: "Unix timestamp when the current Timer assignment reconciliation batch started."}, []string{"space_id"}),
		assignmentErrors:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_fetch_assignment_errors_total", Help: "Timer assignment reconciliation errors."}, []string{"space_id", "reason"}),
		periodPending:            prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_period_pending_total", Help: "Collector periods waiting to be reported."}, []string{"space_id", "frequency"}),
		periodPendingFrequencies: make(map[string]map[string]struct{}),
		periodReportRetry:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_period_report_retry_total", Help: "Collector period report attempts that need retry."}, []string{"space_id", "frequency"}),
		feedResults:              prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_feed_results_total", Help: "Bounded market feed outcomes; subject, function, IP, and candidate chain are intentionally excluded."}, []string{"market_id", "route_id", "provider_id", "feed_kind", "group_id", "batch_kind", "result"}),
		feedObservations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_feed_observations_total", Help: "Bounded market feed observations with source, route, and egress dimensions."}, []string{
			"market_id", "instrument_type", "provider_id", "source_id", "frequency", "source_kind", "transport",
			"remote_host", "remote_port", "scf_region", "egress_scope", "egress_ip", "connection_attempt",
			"rows", "unit", "fallback_rank", "error_kind", "history_window", "calendar_id", "result",
		}),
	}
	metrics.storeBytes = registerGaugeVec(reg, metrics.storeBytes)
	metrics.storeRows = registerGaugeVec(reg, metrics.storeRows)
	metrics.storeRowsCapped = registerGaugeVec(reg, metrics.storeRowsCapped)
	metrics.storeStatsHealthy = registerGaugeVec(reg, metrics.storeStatsHealthy)
	metrics.storeStatsLastSuccess = registerGaugeVec(reg, metrics.storeStatsLastSuccess)
	metrics.maintenanceDeleted = registerGaugeVec(reg, metrics.maintenanceDeleted)
	metrics.maintenanceDuration = registerGaugeVec(reg, metrics.maintenanceDuration)
	metrics.maintenancePasses = registerCounterVec(reg, metrics.maintenancePasses)
	metrics.maintenanceLastSuccess = registerGaugeVec(reg, metrics.maintenanceLastSuccess)
	metrics.assignmentLastSuccess = registerGaugeVec(reg, metrics.assignmentLastSuccess)
	metrics.assignmentHealthy = registerGaugeVec(reg, metrics.assignmentHealthy)
	metrics.assignmentFailure = registerGaugeVec(reg, metrics.assignmentFailure)
	metrics.assignmentPending = registerGaugeVec(reg, metrics.assignmentPending)
	metrics.assignmentPendingSince = registerGaugeVec(reg, metrics.assignmentPendingSince)
	metrics.assignmentErrors = registerCounterVec(reg, metrics.assignmentErrors)
	metrics.periodPending = registerGaugeVec(reg, metrics.periodPending)
	metrics.periodReportRetry = registerCounterVec(reg, metrics.periodReportRetry)
	metrics.feedResults = registerCounterVec(reg, metrics.feedResults)
	metrics.feedObservations = registerCounterVec(reg, metrics.feedObservations)
	return metrics
}

type operationalStatsSource interface {
	OperationalStats(context.Context, string, int) (store.OperationalStats, error)
}

var operationalTableKinds = []string{"instances", "write_targets"}
var maintenanceTableKinds = []string{"period_readiness"}

// RefreshOperationalStats publishes only a complete observation. On failure it
// retains last-good values and marks them stale rather than inventing zeros.
func (m *Metrics) RefreshOperationalStats(ctx context.Context, source operationalStatsSource, spaceID string, now time.Time) error {
	if m == nil {
		return nil
	}
	stats, err := source.OperationalStats(ctx, spaceID, store.MaxOperationalStatsRows)
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
func (m *Metrics) Observe(string, any) {}

func (m *Metrics) ObserveAssignmentError(spaceID, reason string) {
	if m == nil {
		return
	}
	m.assignmentErrors.WithLabelValues(spaceID, normalizeAssignmentFailureReason(reason)).Inc()
}
