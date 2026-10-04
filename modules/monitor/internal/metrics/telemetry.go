package metrics

import (
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	ingestTotal        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_metrics_ingest_total", Help: "Metric snapshot ingestion outcomes."}, []string{"result"})
	ingestLastSuccess  = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_ingest_last_success_timestamp_seconds", Help: "Last successful metric snapshot ingestion."})
	ingestLatency      = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "moox_monitor_metrics_ingest_latency_seconds", Help: "Delay between snapshot occurrence and ingestion."})
	consumerPending    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_consumer_pending", Help: "Fetched metric deliveries awaiting handling."})
	inventoryRefresh   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_task_result_inventory_refresh_total", Help: "Collector task-result inventory refresh outcomes."}, []string{"result"})
	inventoryLastOK    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_last_success_timestamp_seconds", Help: "Last successful Collector task-result inventory refresh."})
	inventoryAge       = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_cache_age_seconds", Help: "Age of the most recent complete Collector task-result inventory snapshot."})
	inventoryAvailable = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_cache_available", Help: "Whether the complete Collector task-result inventory is within its freshness TTL."})
)

func init() {
	prometheus.MustRegister(ingestTotal, ingestLastSuccess, ingestLatency, consumerPending, inventoryRefresh, inventoryLastOK, inventoryAge, inventoryAvailable)
}

func recordTaskResultInventoryRefresh(result string, state TaskResultInventoryCacheState) {
	inventoryRefresh.WithLabelValues(result).Inc()
	if !state.LastSuccess.IsZero() {
		inventoryLastOK.Set(float64(state.LastSuccess.Unix()))
		inventoryAge.Set(max(state.Age.Seconds(), 0))
	} else {
		inventoryLastOK.Set(0)
		inventoryAge.Set(0)
	}
	if state.Available {
		inventoryAvailable.Set(1)
	} else {
		inventoryAvailable.Set(0)
	}
}

func observeTaskResultInventoryCache(state TaskResultInventoryCacheState) {
	if !state.LastSuccess.IsZero() {
		inventoryAge.Set(max(state.Age.Seconds(), 0))
	} else {
		inventoryAge.Set(0)
	}
	if state.Available {
		inventoryAvailable.Set(1)
	} else {
		inventoryAvailable.Set(0)
	}
}

func recordIngest(moduleMetrics *report.ModuleMetrics, result string, observed time.Time) {
	now := time.Now().UTC()
	ingestTotal.WithLabelValues(result).Inc()
	if moduleMetrics != nil {
		_ = moduleMetrics.ObserveRun("ingest", result, "monitor-metrics", now)
	}
	if moduleMetrics != nil && !observed.IsZero() {
		_ = moduleMetrics.AdvanceInputWatermark("ingest", "monitor-metrics", observed)
	}
	if result == "success" {
		ingestLastSuccess.Set(float64(now.Unix()))
		if !observed.IsZero() && !observed.After(now) {
			ingestLatency.Observe(now.Sub(observed).Seconds())
		}
		if moduleMetrics != nil && !observed.IsZero() {
			_ = moduleMetrics.AdvanceWatermark("ingest", "monitor-metrics", observed)
		}
	}
}

func RecordIngest(moduleMetrics *report.ModuleMetrics, result string, observed time.Time) {
	recordIngest(moduleMetrics, result, observed)
}
