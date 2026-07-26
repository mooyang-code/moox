package metrics

import (
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	ingestTotal       = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_metrics_ingest_total", Help: "Metric snapshot ingestion outcomes."}, []string{"result"})
	ingestLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_ingest_last_success_timestamp_seconds", Help: "Last successful metric snapshot ingestion."})
	ingestLatency     = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "moox_monitor_metrics_ingest_latency_seconds", Help: "Delay between snapshot occurrence and ingestion."})
	consumerPending   = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_consumer_pending", Help: "Fetched metric deliveries awaiting handling."})
)

func init() {
	prometheus.MustRegister(ingestTotal, ingestLastSuccess, ingestLatency, consumerPending)
}

func recordIngest(result string, observed time.Time) {
	now := time.Now().UTC()
	ingestTotal.WithLabelValues(result).Inc()
	_ = report.ObserveModuleRun("monitor", "ingest", result, "monitor-metrics", now)
	if !observed.IsZero() {
		_ = report.ObserveModuleInputWatermark("monitor", "ingest", "monitor-metrics", observed)
	}
	if result == "success" {
		ingestLastSuccess.Set(float64(now.Unix()))
		if !observed.IsZero() && !observed.After(now) {
			ingestLatency.Observe(now.Sub(observed).Seconds())
		}
		if !observed.IsZero() {
			_ = report.ObserveModuleWatermark("monitor", "ingest", "monitor-metrics", observed)
		}
	}
}

func RecordIngest(result string, observed time.Time) {
	recordIngest(result, observed)
}
