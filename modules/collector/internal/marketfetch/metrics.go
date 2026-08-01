package marketfetch

import (
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	batchTotal    *prometheus.CounterVec
	batchDuration *prometheus.HistogramVec
	retryPending  *prometheus.GaugeVec
	lastSuccess   *prometheus.GaugeVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		batchTotal:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_collector_market_fetch_batches_total", Help: "Market fetch batches completed."}, []string{"space_id", "dataset_id", "frequency", "outcome"}),
		batchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_collector_market_fetch_batch_duration_seconds", Help: "Market fetch batch duration in seconds."}, []string{"space_id", "dataset_id", "frequency"}),
		retryPending:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_retry_pending", Help: "Approximate market fetch retry backlog."}, []string{"space_id", "dataset_id", "frequency"}),
		lastSuccess:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_fetch_last_success_timestamp_seconds", Help: "Last successful market fetch completion timestamp."}, []string{"space_id", "dataset_id", "frequency"}),
	}
	metrics.batchTotal = registerCounterVec(reg, metrics.batchTotal)
	metrics.batchDuration = registerHistogramVec(reg, metrics.batchDuration)
	metrics.retryPending = registerGaugeVec(reg, metrics.retryPending)
	metrics.lastSuccess = registerGaugeVec(reg, metrics.lastSuccess)
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

func registerHistogramVec(reg prometheus.Registerer, collector *prometheus.HistogramVec) *prometheus.HistogramVec {
	if err := reg.Register(collector); err != nil {
		if existing, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if typed, ok := existing.ExistingCollector.(*prometheus.HistogramVec); ok {
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

func (m *Metrics) Observe(spaceID string, payload *marketfetchpb.MarketFetchBatchCompleted) {
	if m == nil || payload == nil {
		return
	}
	outcome := "failed"
	switch payload.GetStatus() {
	case "succeeded":
		outcome = "success"
	case "partial_failed":
		outcome = "partial_failed"
	case "timed_out":
		outcome = "timeout"
	}
	m.batchTotal.WithLabelValues(spaceID, payload.GetDatasetId(), payload.GetFrequency(), outcome).Inc()
	m.batchDuration.WithLabelValues(spaceID, payload.GetDatasetId(), payload.GetFrequency()).Observe(float64(payload.GetDurationMs()) / 1000)
	if payload.GetStatus() == "succeeded" && payload.GetCompletedAt() != nil && payload.GetCompletedAt().CheckValid() == nil {
		m.lastSuccess.WithLabelValues(spaceID, payload.GetDatasetId(), payload.GetFrequency()).Set(float64(payload.GetCompletedAt().AsTime().Unix()))
	}
}

func (m *Metrics) SetRetryPending(spaceID, datasetID, frequency string, count int) {
	if m == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	m.retryPending.WithLabelValues(spaceID, datasetID, frequency).Set(float64(count))
}
