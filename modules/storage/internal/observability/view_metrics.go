package observability

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
)

// ViewMetrics contains only low-cardinality projection and delivery metrics.
type ViewMetrics struct {
	deriveTotal     *prometheus.CounterVec
	batchDuration   *prometheus.HistogramVec
	deriveInFlight  prometheus.Gauge
	deliveryTotal   *prometheus.CounterVec
	redeliveryTotal prometheus.Counter

	consumerLagMessages         prometheus.Gauge
	oldestPendingEventAge       prometheus.Gauge
	deliveryDuration            prometheus.Histogram
	ackErrorsTotal              prometheus.Counter
	inProgressErrorsTotal       prometheus.Counter
	laneActive                  prometheus.Gauge
	outboxPendingEntries        prometheus.Gauge
	outboxOldestAge             prometheus.Gauge
	outboxPublishErrorsTotal    prometheus.Counter
	outboxDuplicatePublish      prometheus.Counter
	consumerLagSnapshot         atomic.Int64
	laneActiveSnapshot          atomic.Int64
	outboxPendingSnapshot       atomic.Int64
	outboxOldestAgeSnapshot     atomic.Int64
	oldestPendingAgeSnapshot    atomic.Int64
	ackErrorsSnapshot           atomic.Int64
	inProgressErrorsSnapshot    atomic.Int64
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
	LaneActive                  int64
	OutboxPendingEntries        int64
	OutboxOldestAge             time.Duration
	OldestPendingAge            time.Duration
	AckErrorsTotal              int64
	InProgressErrorsTotal       int64
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
		oldestPendingEventAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "moox", Subsystem: "storage_view", Name: "oldest_pending_event_age_seconds",
			Help: "Age of the oldest pending Storage view delivery.",
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
		pendingDeliveries: make(map[*jetstream.Delivery]time.Time),
	}
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
	return metrics, nil
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
	if delivery.Message != nil {
		if occurred := delivery.Message.GetOccurredAt(); occurred != nil && occurred.CheckValid() == nil {
			eventAt = occurred.AsTime()
		}
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
	oldest := time.Time{}
	for _, eventAt := range m.pendingDeliveries {
		if oldest.IsZero() || eventAt.Before(oldest) {
			oldest = eventAt
		}
	}
	age := time.Duration(0)
	if !oldest.IsZero() && now.After(oldest) {
		age = now.Sub(oldest)
	}
	m.oldestPendingEventAge.Set(age.Seconds())
	m.oldestPendingAgeSnapshot.Store(age.Nanoseconds())
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

func (m *ViewMetrics) SetOutboxSnapshot(pending int, oldestAge time.Duration) {
	if m == nil {
		return
	}
	if pending < 0 {
		pending = 0
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	m.outboxPendingEntries.Set(float64(pending))
	m.outboxOldestAge.Set(oldestAge.Seconds())
	m.outboxPendingSnapshot.Store(int64(pending))
	m.outboxOldestAgeSnapshot.Store(oldestAge.Nanoseconds())
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
	return ViewMetricsSnapshot{
		ConsumerLagMessages:         m.consumerLagSnapshot.Load(),
		LaneActive:                  m.laneActiveSnapshot.Load(),
		OutboxPendingEntries:        m.outboxPendingSnapshot.Load(),
		OutboxOldestAge:             time.Duration(m.outboxOldestAgeSnapshot.Load()),
		OldestPendingAge:            time.Duration(m.oldestPendingAgeSnapshot.Load()),
		AckErrorsTotal:              m.ackErrorsSnapshot.Load(),
		InProgressErrorsTotal:       m.inProgressErrorsSnapshot.Load(),
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
