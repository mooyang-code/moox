package observability

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ViewMetrics contains only low-cardinality projection and delivery metrics.
type ViewMetrics struct {
	deriveTotal     *prometheus.CounterVec
	batchDuration   *prometheus.HistogramVec
	deriveInFlight  prometheus.Gauge
	deliveryTotal   *prometheus.CounterVec
	redeliveryTotal prometheus.Counter
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
	}
	for _, collector := range []prometheus.Collector{
		metrics.deriveTotal, metrics.batchDuration, metrics.deriveInFlight, metrics.deliveryTotal, metrics.redeliveryTotal,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
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
	case "ack", "nak", "in_progress":
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
