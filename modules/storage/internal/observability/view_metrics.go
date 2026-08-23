package observability

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
)

// ViewMetrics contains only low-cardinality projection and delivery metrics.
type ViewMetrics struct {
	deriveTotal     *prometheus.CounterVec
	batchDuration   *prometheus.HistogramVec
	deriveInFlight  prometheus.Gauge
	deliveryTotal   *prometheus.CounterVec
	redeliveryTotal prometheus.Counter

	consumerLagMessages          prometheus.Gauge
	consumerBound                prometheus.Gauge
	consumerPartitionLag         *prometheus.GaugeVec
	consumerPartitionBound       *prometheus.GaugeVec
	oldestPendingEventAge        prometheus.GaugeFunc
	deliveryDuration             prometheus.Histogram
	ackErrorsTotal               prometheus.Counter
	inProgressErrorsTotal        prometheus.Counter
	retryExhaustedTotal          prometheus.Counter
	laneActive                   prometheus.Gauge
	outboxPendingEntries         prometheus.Gauge
	outboxOldestAge              prometheus.Gauge
	outboxPublishErrorsTotal     prometheus.Counter
	outboxDuplicatePublish       prometheus.Counter
	periodWaitingDatasets        *prometheus.GaugeVec
	viewOutputWatermark          *prometheus.GaugeVec
	readyPublishRetry            *prometheus.CounterVec
	restoreDuration              prometheus.Gauge
	restoreReady                 prometheus.Gauge
	restoreFailures              prometheus.Counter
	rebuildAuditPending          prometheus.Gauge
	rebuildAuditFailures         prometheus.Counter
	rebuildAuditDropped          prometheus.Counter
	consumerLagSnapshot          atomic.Int64
	consumerBoundSnapshot        atomic.Bool
	partitionMu                  sync.RWMutex
	partitionStates              map[string]ConsumerPartitionSnapshot
	viewWatermarkMu              sync.Mutex
	viewWatermarks               map[string]int64
	outboxObservedSnapshot       atomic.Bool
	outboxDynamicAge             atomic.Bool
	outboxOldestEventAt          atomic.Int64
	laneActiveSnapshot           atomic.Int64
	outboxPendingSnapshot        atomic.Int64
	outboxOldestAgeSnapshot      atomic.Int64
	oldestPendingAgeSnapshot     atomic.Int64
	ackErrorsSnapshot            atomic.Int64
	inProgressErrorsSnapshot     atomic.Int64
	retryExhaustedSnapshot       atomic.Int64
	outboxPublishErrorsSnapshot  atomic.Int64
	outboxDuplicateSnapshot      atomic.Int64
	restoreDurationSnapshot      atomic.Int64
	restoreReadySnapshot         atomic.Bool
	restoreFailuresSnapshot      atomic.Int64
	rebuildAuditPendingSnapshot  atomic.Int64
	rebuildAuditFailuresSnapshot atomic.Int64
	rebuildAuditDroppedSnapshot  atomic.Int64
	pendingMu                    sync.Mutex
	pendingDeliveries            map[*jetstream.Delivery]time.Time
}

// ViewMetricsSnapshot is the aggregate runtime state exported by the view and
// outbox instrumentation. It intentionally has no subject, symbol, or message
// identity fields.
type ViewMetricsSnapshot struct {
	ConsumerLagMessages         int64
	ConsumerBound               bool
	LaneActive                  int64
	OutboxPendingEntries        int64
	OutboxObserved              bool
	OutboxOldestAge             time.Duration
	OldestPendingAge            time.Duration
	AckErrorsTotal              int64
	InProgressErrorsTotal       int64
	RetryExhaustedTotal         int64
	OutboxPublishErrorsTotal    int64
	OutboxDuplicatePublishTotal int64
	RestoreDuration             time.Duration
	RestoreReady                bool
	RestoreFailures             int64
	RebuildAuditPending         int64
	RebuildAuditFailures        int64
	RebuildAuditDropped         int64
}

// ConsumerPartitionSnapshot is a low-cardinality status projection for one
// Storage View durable. It is intentionally keyed only by configured
// partition id, never by dataset or subject.
type ConsumerPartitionSnapshot struct {
	PartitionID      string
	Durable          string
	Bound            bool
	LagMessages      int64
	Pending          uint64
	AckPending       uint64
	OldestPendingAge time.Duration
	LastDeliveryAt   time.Time
	backlogSince     time.Time
}

func NewViewMetrics(registerer prometheus.Registerer) (*ViewMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("storage view metrics registerer is nil")
	}
	metrics := &ViewMetrics{
		deriveTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "derive_events_total",
			Help: "Terminal result of Storage view derivation events.",
		}, []string{"kind", "result"}),
		batchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "derive_batch_duration_seconds",
			Help: "Storage view derivation batch latency.",
		}, []string{"engine", "result"}),
		deriveInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "derive_events_in_flight",
			Help: "Storage events waiting for all derived rows to complete.",
		}),
		deliveryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "delivery_actions_total",
			Help: "JetStream delivery terminal and heartbeat actions.",
		}, []string{"action", "result"}),
		redeliveryTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "redeliveries_total",
			Help: "Storage view deliveries observed more than once.",
		}),
		consumerLagMessages: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "consumer_lag_messages",
			Help: "Storage view deliveries fetched but not finished.",
		}),
		consumerBound: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "consumer_bound",
			Help: "Whether the Storage view durable consumer is currently bound.",
		}),
		consumerPartitionLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "consumer_partition_lag_messages",
			Help: "Storage view deliveries fetched but not finished, by configured partition.",
		}, []string{"partition", "durable"}),
		consumerPartitionBound: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "consumer_partition_bound",
			Help: "Whether a configured Storage view durable is currently bound.",
		}, []string{"partition", "durable"}),
		deliveryDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "delivery_duration_seconds",
			Help: "Storage view delivery processing duration.",
		}),
		ackErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "ack_errors_total",
			Help: "Storage view ACK and TERM errors.",
		}),
		inProgressErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "in_progress_errors_total",
			Help: "Storage view InProgress errors.",
		}),
		retryExhaustedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "retry_exhausted_total",
			Help: "Storage view deliveries that reached the client-side retry limit.",
		}),
		laneActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "lane_active",
			Help: "Number of active aggregate Storage view subject lanes.",
		}),
		outboxPendingEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "pending_entries",
			Help: "Number of pending Storage outbox entries.",
		}),
		outboxOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "oldest_age_seconds",
			Help: "Age of the oldest pending Storage outbox entry.",
		}),
		outboxPublishErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "publish_errors_total",
			Help: "Storage outbox publish errors.",
		}),
		outboxDuplicatePublish: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_outbox", Name: "duplicate_publish_total",
			Help: "Storage outbox publishes acknowledged as duplicates.",
		}),
		periodWaitingDatasets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "period_waiting_datasets",
			Help: "Number of datasets still missing before a View source period can be published.",
		}, []string{"view", "frequency"}),
		viewOutputWatermark: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "output_watermark_timestamp_seconds",
			Help: "Latest business timestamp successfully committed to an active Storage View.",
		}, []string{"space_id", "view_id", "freq"}),
		readyPublishRetry: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "ready_publish_retry_total",
			Help: "View source/result ready event publishes that need retry.",
		}, []string{"view", "event"}),
		restoreDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "restore_duration_seconds",
			Help: "Duration of the latest active View restore pass.",
		}),
		restoreReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "restore_ready",
			Help: "Whether the latest active View restore pass completed successfully.",
		}),
		restoreFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "restore_failures_total",
			Help: "Number of failed active View restore passes.",
		}),
		rebuildAuditPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "rebuild_audit_pending",
			Help: "Number of View rebuild audit records waiting for metadata persistence.",
		}),
		rebuildAuditFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "rebuild_audit_write_failures_total",
			Help: "View rebuild audit persistence failures.",
		}),
		rebuildAuditDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "rebuild_audit_dropped_total",
			Help: "View rebuild audit retry records dropped after reaching the queue limit.",
		}),
		pendingDeliveries: make(map[*jetstream.Delivery]time.Time),
		partitionStates:   make(map[string]ConsumerPartitionSnapshot),
		viewWatermarks:    make(map[string]int64),
	}
	metrics.oldestPendingEventAge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "moox", Subsystem: "storage_view", Name: "oldest_pending_event_age_seconds",
		Help: "Age of the oldest pending Storage view delivery.",
	}, func() float64 { return metrics.currentOldestPendingAge(time.Now().UTC()).Seconds() })
	var err error
	if metrics.deriveTotal, err = registerOrReuse(registerer, metrics.deriveTotal); err != nil {
		return nil, err
	}
	if metrics.batchDuration, err = registerOrReuse(registerer, metrics.batchDuration); err != nil {
		return nil, err
	}
	if metrics.deriveInFlight, err = registerOrReuse(registerer, metrics.deriveInFlight); err != nil {
		return nil, err
	}
	if metrics.deliveryTotal, err = registerOrReuse(registerer, metrics.deliveryTotal); err != nil {
		return nil, err
	}
	if metrics.redeliveryTotal, err = registerOrReuse(registerer, metrics.redeliveryTotal); err != nil {
		return nil, err
	}
	if metrics.consumerLagMessages, err = registerOrReuse(registerer, metrics.consumerLagMessages); err != nil {
		return nil, err
	}
	if metrics.consumerBound, err = registerOrReuse(registerer, metrics.consumerBound); err != nil {
		return nil, err
	}
	if metrics.consumerPartitionLag, err = registerOrReuse(registerer, metrics.consumerPartitionLag); err != nil {
		return nil, err
	}
	if metrics.consumerPartitionBound, err = registerOrReuse(registerer, metrics.consumerPartitionBound); err != nil {
		return nil, err
	}
	if metrics.oldestPendingEventAge, err = registerOrReuse(registerer, metrics.oldestPendingEventAge); err != nil {
		return nil, err
	}
	if metrics.deliveryDuration, err = registerOrReuse(registerer, metrics.deliveryDuration); err != nil {
		return nil, err
	}
	if metrics.ackErrorsTotal, err = registerOrReuse(registerer, metrics.ackErrorsTotal); err != nil {
		return nil, err
	}
	if metrics.inProgressErrorsTotal, err = registerOrReuse(registerer, metrics.inProgressErrorsTotal); err != nil {
		return nil, err
	}
	if metrics.retryExhaustedTotal, err = registerOrReuse(registerer, metrics.retryExhaustedTotal); err != nil {
		return nil, err
	}
	if metrics.laneActive, err = registerOrReuse(registerer, metrics.laneActive); err != nil {
		return nil, err
	}
	if metrics.outboxPendingEntries, err = registerOrReuse(registerer, metrics.outboxPendingEntries); err != nil {
		return nil, err
	}
	if metrics.outboxOldestAge, err = registerOrReuse(registerer, metrics.outboxOldestAge); err != nil {
		return nil, err
	}
	if metrics.outboxPublishErrorsTotal, err = registerOrReuse(registerer, metrics.outboxPublishErrorsTotal); err != nil {
		return nil, err
	}
	if metrics.outboxDuplicatePublish, err = registerOrReuse(registerer, metrics.outboxDuplicatePublish); err != nil {
		return nil, err
	}
	if metrics.periodWaitingDatasets, err = registerOrReuse(registerer, metrics.periodWaitingDatasets); err != nil {
		return nil, err
	}
	if metrics.viewOutputWatermark, err = registerOrReuse(registerer, metrics.viewOutputWatermark); err != nil {
		return nil, err
	}
	if metrics.readyPublishRetry, err = registerOrReuse(registerer, metrics.readyPublishRetry); err != nil {
		return nil, err
	}
	if metrics.restoreDuration, err = registerOrReuse(registerer, metrics.restoreDuration); err != nil {
		return nil, err
	}
	if metrics.restoreReady, err = registerOrReuse(registerer, metrics.restoreReady); err != nil {
		return nil, err
	}
	if metrics.restoreFailures, err = registerOrReuse(registerer, metrics.restoreFailures); err != nil {
		return nil, err
	}
	if metrics.rebuildAuditPending, err = registerOrReuse(registerer, metrics.rebuildAuditPending); err != nil {
		return nil, err
	}
	if metrics.rebuildAuditFailures, err = registerOrReuse(registerer, metrics.rebuildAuditFailures); err != nil {
		return nil, err
	}
	if metrics.rebuildAuditDropped, err = registerOrReuse(registerer, metrics.rebuildAuditDropped); err != nil {
		return nil, err
	}
	return metrics, nil
}

// ObserveViewOutputWatermark advances the committed watermark for one active
// View. View IDs are deliberately used as the dataset identity here so the
// monitor can create an independent freshness check instead of folding this
// signal into the Primary dataset watermark.
func (m *ViewMetrics) ObserveViewOutputWatermark(spaceID, viewID, frequency string, watermark time.Time) {
	if m == nil || strings.TrimSpace(spaceID) == "" || strings.TrimSpace(viewID) == "" || watermark.IsZero() {
		return
	}
	frequency = strings.TrimSpace(frequency)
	if frequency == "" {
		return
	}
	spaceID = strings.TrimSpace(spaceID)
	viewID = strings.TrimSpace(viewID)
	watermarkUnix := watermark.UTC().Unix()
	key := strings.Join([]string{spaceID, viewID, frequency}, "\x00")
	m.viewWatermarkMu.Lock()
	defer m.viewWatermarkMu.Unlock()
	if previous, ok := m.viewWatermarks[key]; ok && watermarkUnix <= previous {
		return
	}
	m.viewWatermarks[key] = watermarkUnix
	m.viewOutputWatermark.WithLabelValues(spaceID, viewID, frequency).Set(float64(watermarkUnix))
}

// ObservePeriodWaiting records the current missing-dataset count for a View
// period. View and frequency are the only labels so this remains bounded.
func (m *ViewMetrics) ObservePeriodWaiting(view, frequency string, waiting int) {
	if m == nil {
		return
	}
	if waiting < 0 {
		waiting = 0
	}
	m.periodWaitingDatasets.WithLabelValues(view, frequency).Set(float64(waiting))
}

func (m *ViewMetrics) ObserveReadyPublishRetry(view, event string) {
	if m == nil {
		return
	}
	m.readyPublishRetry.WithLabelValues(view, event).Inc()
}

// ObserveRestore records the latest startup restore result without adding
// View- or dataset-level labels. A failed pass leaves readiness false while
// the liveness endpoint remains available for diagnostics.
func (m *ViewMetrics) ObserveRestore(success bool, elapsed time.Duration) {
	if m == nil {
		return
	}
	if elapsed < 0 {
		elapsed = 0
	}
	m.restoreDurationSnapshot.Store(elapsed.Nanoseconds())
	m.restoreDuration.Set(elapsed.Seconds())
	if success {
		m.restoreReadySnapshot.Store(true)
		m.restoreReady.Set(1)
		return
	}
	m.restoreReadySnapshot.Store(false)
	m.restoreReady.Set(0)
	m.restoreFailuresSnapshot.Add(1)
	m.restoreFailures.Inc()
}

func (m *ViewMetrics) SetConsumerBound(bound bool) {
	if m == nil {
		return
	}
	m.consumerBoundSnapshot.Store(bound)
	if bound {
		m.consumerBound.Set(1)
	} else {
		m.consumerBound.Set(0)
	}
}

func (m *ViewMetrics) SetConsumerPartitionBound(partition, durable string, bound bool) {
	if m == nil || strings.TrimSpace(partition) == "" {
		return
	}
	partition = strings.TrimSpace(partition)
	durable = strings.TrimSpace(durable)
	m.partitionMu.Lock()
	state := m.partitionStates[partition]
	state.PartitionID = partition
	state.Durable = durable
	state.Bound = bound
	m.partitionStates[partition] = state
	m.partitionMu.Unlock()
	value := float64(0)
	if bound {
		value = 1
	}
	m.consumerPartitionBound.WithLabelValues(partition, durable).Set(value)
}

func (m *ViewMetrics) AddConsumerPartitionLag(partition, durable string, delta int64) {
	if m == nil || strings.TrimSpace(partition) == "" {
		return
	}
	partition = strings.TrimSpace(partition)
	durable = strings.TrimSpace(durable)
	m.partitionMu.Lock()
	state := m.partitionStates[partition]
	state.PartitionID = partition
	state.Durable = durable
	state.LagMessages += delta
	if state.LagMessages < 0 {
		state.LagMessages = 0
	}
	m.partitionStates[partition] = state
	lag := state.LagMessages
	m.partitionMu.Unlock()
	m.consumerPartitionLag.WithLabelValues(partition, durable).Set(float64(lag))
}

func (m *ViewMetrics) SetConsumerPartitionBacklog(partition, durable string, pending, ackPending uint64) {
	if m == nil || strings.TrimSpace(partition) == "" {
		return
	}
	partition = strings.TrimSpace(partition)
	durable = strings.TrimSpace(durable)
	m.partitionMu.Lock()
	state := m.partitionStates[partition]
	state.PartitionID = partition
	state.Durable = durable
	state.Pending = pending
	state.AckPending = ackPending
	if pending+ackPending > 0 && state.backlogSince.IsZero() {
		state.backlogSince = time.Now().UTC()
	}
	if pending+ackPending == 0 {
		state.backlogSince = time.Time{}
		state.OldestPendingAge = 0
	}
	m.partitionStates[partition] = state
	m.partitionMu.Unlock()
}

func (m *ViewMetrics) ObserveConsumerPartitionBacklog(partition string, pending, ackPending uint64) {
	if m == nil || strings.TrimSpace(partition) == "" {
		return
	}
	partition = strings.TrimSpace(partition)
	m.partitionMu.Lock()
	state := m.partitionStates[partition]
	state.PartitionID = partition
	state.Pending = pending
	state.AckPending = ackPending
	now := time.Now().UTC()
	state.LastDeliveryAt = now
	if pending+ackPending > 0 {
		if state.backlogSince.IsZero() {
			state.backlogSince = now
		}
		state.OldestPendingAge = now.Sub(state.backlogSince)
	} else {
		state.backlogSince = time.Time{}
		state.OldestPendingAge = 0
	}
	m.partitionStates[partition] = state
	m.partitionMu.Unlock()
}

func (m *ViewMetrics) ConsumerPartitionsSnapshot() []ConsumerPartitionSnapshot {
	if m == nil {
		return nil
	}
	m.partitionMu.RLock()
	result := make([]ConsumerPartitionSnapshot, 0, len(m.partitionStates))
	now := time.Now().UTC()
	for _, state := range m.partitionStates {
		if !state.backlogSince.IsZero() && now.After(state.backlogSince) {
			state.OldestPendingAge = now.Sub(state.backlogSince)
		}
		state.backlogSince = time.Time{}
		result = append(result, state)
	}
	m.partitionMu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].PartitionID < result[j].PartitionID })
	return result
}

func registerOrReuse[T prometheus.Collector](registerer prometheus.Registerer, collector T) (T, error) {
	if err := registerer.Register(collector); err == nil {
		return collector, nil
	} else {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(T); ok {
				return existing, nil
			}
		}
		return collector, err
	}
}

func mustDefaultViewMetrics() *ViewMetrics {
	metrics, err := NewViewMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		panic(err)
	}
	return metrics
}

var DefaultViewMetrics = mustDefaultViewMetrics()

func (m *ViewMetrics) IncDeriveInFlight() {
	if m != nil {
		m.deriveInFlight.Inc()
	}
}

func (m *ViewMetrics) DecDeriveInFlight() {
	if m != nil {
		m.deriveInFlight.Dec()
	}
}

func (m *ViewMetrics) ObserveDerive(kind, result string) {
	if m != nil {
		m.deriveTotal.WithLabelValues(deriveKind(kind), outcome(result)).Inc()
	}
}

func (m *ViewMetrics) ObserveBatch(engine, result string, elapsed time.Duration) {
	if m != nil {
		m.batchDuration.WithLabelValues(viewEngine(engine), outcome(result)).Observe(elapsed.Seconds())
	}
}

func (m *ViewMetrics) ObserveDelivery(action, result string) {
	if m != nil {
		m.deliveryTotal.WithLabelValues(deliveryAction(action), outcome(result)).Inc()
	}
}

func (m *ViewMetrics) IncRedelivery() {
	if m != nil {
		m.redeliveryTotal.Inc()
	}
}

func (m *ViewMetrics) AddConsumerLagMessages(delta int64) {
	if m == nil {
		return
	}
	value := m.consumerLagSnapshot.Add(delta)
	if value < 0 {
		m.consumerLagSnapshot.Store(0)
		value = 0
	}
	m.consumerLagMessages.Set(float64(value))
}

func (m *ViewMetrics) ObserveLaneSubmit() {
	if m == nil {
		return
	}
	m.consumerLagMessages.Set(float64(m.consumerLagSnapshot.Load()))
}

func (m *ViewMetrics) ObservePendingDelivery(delivery *jetstream.Delivery, now time.Time) {
	if m == nil || delivery == nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	eventAt := now
	message := new(eventpb.EventMessage)
	if err := proto.Unmarshal(delivery.RawData, message); err == nil && message.GetOccurredAt() != nil && message.GetOccurredAt().CheckValid() == nil {
		eventAt = message.GetOccurredAt().AsTime()
	}
	m.pendingMu.Lock()
	m.pendingDeliveries[delivery] = eventAt
	m.updateOldestPendingLocked(now)
	m.pendingMu.Unlock()
}

func (m *ViewMetrics) CompletePendingDelivery(delivery *jetstream.Delivery, now time.Time) {
	if m == nil || delivery == nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m.pendingMu.Lock()
	delete(m.pendingDeliveries, delivery)
	m.updateOldestPendingLocked(now)
	m.pendingMu.Unlock()
}

func (m *ViewMetrics) updateOldestPendingLocked(now time.Time) {
	age := m.oldestPendingAgeLocked(now)
	m.oldestPendingAgeSnapshot.Store(age.Nanoseconds())
}

func (m *ViewMetrics) currentOldestPendingAge(now time.Time) time.Duration {
	if m == nil {
		return 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	age := m.oldestPendingAgeLocked(now)
	m.oldestPendingAgeSnapshot.Store(age.Nanoseconds())
	return age
}

func (m *ViewMetrics) oldestPendingAgeLocked(now time.Time) time.Duration {
	oldest := time.Time{}
	for _, eventAt := range m.pendingDeliveries {
		if oldest.IsZero() || eventAt.Before(oldest) {
			oldest = eventAt
		}
	}
	if oldest.IsZero() || !now.After(oldest) {
		return 0
	}
	return now.Sub(oldest)
}

func (m *ViewMetrics) IncLaneActive() {
	if m != nil {
		m.laneActive.Set(float64(m.laneActiveSnapshot.Add(1)))
	}
}

func (m *ViewMetrics) DecLaneActive() {
	if m == nil {
		return
	}
	value := m.laneActiveSnapshot.Add(-1)
	if value < 0 {
		m.laneActiveSnapshot.Store(0)
		value = 0
	}
	m.laneActive.Set(float64(value))
}

func (m *ViewMetrics) ObserveDeliveryDuration(elapsed time.Duration) {
	if m != nil {
		m.deliveryDuration.Observe(elapsed.Seconds())
	}
}

func (m *ViewMetrics) IncAckError() {
	if m != nil {
		m.ackErrorsTotal.Inc()
		m.ackErrorsSnapshot.Add(1)
	}
}

func (m *ViewMetrics) IncInProgressError() {
	if m != nil {
		m.inProgressErrorsTotal.Inc()
		m.inProgressErrorsSnapshot.Add(1)
	}
}

func (m *ViewMetrics) IncRetryExhausted() {
	if m != nil {
		m.retryExhaustedTotal.Inc()
		m.retryExhaustedSnapshot.Add(1)
	}
}

// SetOutboxSnapshotAt records the source timestamp of the oldest pending
// entry. Health checks can then observe an increasing age even if the relay
// stops polling after the entry was first observed.
func (m *ViewMetrics) SetOutboxSnapshotAt(pending int, oldestEventAt time.Time) {
	if m == nil {
		return
	}
	if pending < 0 {
		pending = 0
	}
	if pending == 0 || oldestEventAt.IsZero() {
		oldestEventAt = time.Time{}
	}
	m.outboxObservedSnapshot.Store(true)
	m.outboxDynamicAge.Store(true)
	if oldestEventAt.IsZero() {
		m.outboxOldestEventAt.Store(0)
	} else {
		m.outboxOldestEventAt.Store(oldestEventAt.UTC().UnixNano())
	}
	age := m.currentOutboxOldestAge(time.Now().UTC())
	m.outboxPendingEntries.Set(float64(pending))
	m.outboxOldestAge.Set(age.Seconds())
	m.outboxPendingSnapshot.Store(int64(pending))
	m.outboxOldestAgeSnapshot.Store(age.Nanoseconds())
}

func (m *ViewMetrics) currentOutboxOldestAge(now time.Time) time.Duration {
	if m == nil {
		return 0
	}
	if !m.outboxDynamicAge.Load() {
		return time.Duration(m.outboxOldestAgeSnapshot.Load())
	}
	nanos := m.outboxOldestEventAt.Load()
	if nanos <= 0 {
		return 0
	}
	oldest := time.Unix(0, nanos)
	if !now.After(oldest) {
		return 0
	}
	return now.Sub(oldest)
}

func (m *ViewMetrics) IncOutboxPublishError() {
	if m != nil {
		m.outboxPublishErrorsTotal.Inc()
		m.outboxPublishErrorsSnapshot.Add(1)
	}
}

func (m *ViewMetrics) IncOutboxDuplicatePublish() {
	if m != nil {
		m.outboxDuplicatePublish.Inc()
		m.outboxDuplicateSnapshot.Add(1)
	}
}

func (m *ViewMetrics) Snapshot() ViewMetricsSnapshot {
	if m == nil {
		return ViewMetricsSnapshot{}
	}
	now := time.Now().UTC()
	oldestPendingAge := m.currentOldestPendingAge(now)
	oldestOutboxAge := m.currentOutboxOldestAge(now)
	m.outboxOldestAge.Set(oldestOutboxAge.Seconds())
	m.outboxOldestAgeSnapshot.Store(oldestOutboxAge.Nanoseconds())
	return ViewMetricsSnapshot{
		ConsumerLagMessages:         m.consumerLagSnapshot.Load(),
		ConsumerBound:               m.consumerBoundSnapshot.Load(),
		LaneActive:                  m.laneActiveSnapshot.Load(),
		OutboxPendingEntries:        m.outboxPendingSnapshot.Load(),
		OutboxObserved:              m.outboxObservedSnapshot.Load(),
		OutboxOldestAge:             oldestOutboxAge,
		OldestPendingAge:            oldestPendingAge,
		AckErrorsTotal:              m.ackErrorsSnapshot.Load(),
		InProgressErrorsTotal:       m.inProgressErrorsSnapshot.Load(),
		RetryExhaustedTotal:         m.retryExhaustedSnapshot.Load(),
		OutboxPublishErrorsTotal:    m.outboxPublishErrorsSnapshot.Load(),
		OutboxDuplicatePublishTotal: m.outboxDuplicateSnapshot.Load(),
		RestoreDuration:             time.Duration(m.restoreDurationSnapshot.Load()),
		RestoreReady:                m.restoreReadySnapshot.Load(),
		RestoreFailures:             m.restoreFailuresSnapshot.Load(),
		RebuildAuditPending:         m.rebuildAuditPendingSnapshot.Load(),
		RebuildAuditFailures:        m.rebuildAuditFailuresSnapshot.Load(),
		RebuildAuditDropped:         m.rebuildAuditDroppedSnapshot.Load(),
	}
}

func (m *ViewMetrics) SetRebuildAuditPending(value int64) {
	if m == nil {
		return
	}
	if value < 0 {
		value = 0
	}
	m.rebuildAuditPendingSnapshot.Store(value)
	m.rebuildAuditPending.Set(float64(value))
}

func (m *ViewMetrics) IncRebuildAuditFailure() {
	if m == nil {
		return
	}
	m.rebuildAuditFailuresSnapshot.Add(1)
	m.rebuildAuditFailures.Inc()
}

func (m *ViewMetrics) IncRebuildAuditDropped() {
	if m == nil {
		return
	}
	m.rebuildAuditDroppedSnapshot.Add(1)
	m.rebuildAuditDropped.Inc()
}

func deriveKind(value string) string {
	switch value {
	case "time_series", "record":
		return value
	default:
		return "unknown"
	}
}

func viewEngine(value string) string {
	switch value {
	case "duckdb", "bleve":
		return value
	default:
		return "unknown"
	}
}

func deliveryAction(value string) string {
	switch value {
	case "ack", "nak", "term", "in_progress":
		return value
	default:
		return "unknown"
	}
}

func outcome(value string) string {
	if value == "success" {
		return value
	}
	return "error"
}
