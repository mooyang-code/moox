package observability

import (
	"errors"
	"fmt"
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

	consumerLagMessages         prometheus.Gauge
	consumerBound               prometheus.Gauge
	oldestPendingEventAge       prometheus.GaugeFunc
	deliveryDuration            prometheus.Histogram
	ackErrorsTotal              prometheus.Counter
	inProgressErrorsTotal       prometheus.Counter
	retryExhaustedTotal         prometheus.Counter
	laneActive                  prometheus.Gauge
	outboxPendingEntries        prometheus.Gauge
	outboxOldestAge             prometheus.Gauge
	outboxPublishErrorsTotal    prometheus.Counter
	outboxDuplicatePublish      prometheus.Counter
	periodWaitingDatasets       *prometheus.GaugeVec
	readyPublishRetry           *prometheus.CounterVec
	consumerLagSnapshot         atomic.Int64
	consumerBoundSnapshot       atomic.Bool
	outboxObservedSnapshot      atomic.Bool
	outboxDynamicAge            atomic.Bool
	outboxOldestEventAt         atomic.Int64
	laneActiveSnapshot          atomic.Int64
	outboxPendingSnapshot       atomic.Int64
	outboxOldestAgeSnapshot     atomic.Int64
	oldestPendingAgeSnapshot    atomic.Int64
	ackErrorsSnapshot           atomic.Int64
	inProgressErrorsSnapshot    atomic.Int64
	retryExhaustedSnapshot      atomic.Int64
	outboxPublishErrorsSnapshot atomic.Int64
	outboxDuplicateSnapshot     atomic.Int64
	pendingMu                   sync.Mutex
	pendingDeliveries           map[*jetstream.Delivery]time.Time
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
		readyPublishRetry: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "ready_publish_retry_total",
			Help: "View source/result ready event publishes that need retry.",
		}, []string{"view", "event"}),
		pendingDeliveries: make(map[*jetstream.Delivery]time.Time),
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
	if metrics.readyPublishRetry, err = registerOrReuse(registerer, metrics.readyPublishRetry); err != nil {
		return nil, err
	}
	return metrics, nil
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
	}
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
