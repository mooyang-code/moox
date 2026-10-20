package metrics

import (
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	ingestTotal        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_metrics_ingest_total", Help: "Metric snapshot ingestion outcomes; reason is set only for rejected snapshots."}, []string{"result", "reason"})
	samplesFiltered    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_metrics_samples_filtered_total", Help: "Samples dropped because their metric family is not in the health allow-list."}, []string{"service"})
	seriesDropped      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_metrics_series_dropped_total", Help: "New series dropped because the new-series limit was reached."}, []string{"service"})
	seriesLimitCurrent = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_monitor_metrics_series_limit_current", Help: "Series created in the window, as seen by the last ingest."}, []string{"window"})
	seriesLimitMax     = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_monitor_metrics_series_limit_max", Help: "Configured new-series limit of the window; absent when unlimited."}, []string{"window"})
	ingestLastSuccess  = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_ingest_last_success_timestamp_seconds", Help: "Last successful metric snapshot ingestion."})
	ingestLatency      = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "moox_monitor_metrics_ingest_latency_seconds", Help: "Delay between snapshot occurrence and ingestion."})
	consumerPending    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_consumer_pending", Help: "Fetched metric deliveries awaiting handling."})
	inventoryRefresh   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_task_result_inventory_refresh_total", Help: "Collector task-result inventory refresh outcomes."}, []string{"result"})
	inventoryLastOK    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_last_success_timestamp_seconds", Help: "Last successful Collector task-result inventory refresh."})
	inventoryAge       = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_cache_age_seconds", Help: "Age of the most recent complete Collector task-result inventory snapshot."})
	inventoryAvailable = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_cache_available", Help: "Whether the complete Collector task-result inventory is within its freshness TTL."})
)

func init() {
	prometheus.MustRegister(ingestTotal, samplesFiltered, seriesDropped, seriesLimitCurrent, seriesLimitMax, ingestLastSuccess, ingestLatency, consumerPending, inventoryRefresh, inventoryLastOK, inventoryAge, inventoryAvailable)
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
	recordIngestReason(moduleMetrics, result, "", observed)
}

func recordIngestReason(moduleMetrics *report.ModuleMetrics, result, reason string, observed time.Time) {
	now := time.Now().UTC()
	ingestTotal.WithLabelValues(result, reason).Inc()
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

// RecordIngestRejected 记录一份被拒收的快照：按原因计数，并留下该服务最近一次被拒的原因与时间供诊断。
func (r *MetricMessageStore) RecordIngestRejected(moduleMetrics *report.ModuleMetrics, service, reason string) {
	recordIngestReason(moduleMetrics, "rejected", reason, time.Time{})
	if r != nil {
		r.diag.recordRejected(service, reason, time.Now().UTC())
	}
}

// RecordSamplesFiltered 记录按白名单被丢弃的样本数。生产者改了指标名而白名单没跟上时，靠它看出来。
func RecordSamplesFiltered(service string, count int) {
	if count > 0 {
		samplesFiltered.WithLabelValues(service).Add(float64(count))
	}
}

// publishSeriesLimit 把配置的限额写成 gauge；限额为 0（不限）的窗口不输出。
func publishSeriesLimit(limits SeriesLimits) {
	seriesLimitMax.Reset()
	if limits.MaxNewPerHour > 0 {
		seriesLimitMax.WithLabelValues("hour").Set(float64(limits.MaxNewPerHour))
	}
	if limits.MaxNewPerDay > 0 {
		seriesLimitMax.WithLabelValues("day").Set(float64(limits.MaxNewPerDay))
	}
}
