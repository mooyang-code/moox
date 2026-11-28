package kline

import "github.com/prometheus/client_golang/prometheus"

var (
	inventoryRefresh   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_task_result_inventory_refresh_total", Help: "Collector task-result inventory refresh outcomes."}, []string{"result"})
	inventoryLastOK    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_last_success_timestamp_seconds", Help: "Last successful Collector task-result inventory refresh."})
	inventoryAge       = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_cache_age_seconds", Help: "Age of the most recent complete Collector task-result inventory snapshot."})
	inventoryAvailable = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_task_result_inventory_cache_available", Help: "Whether the complete Collector task-result inventory is within its freshness TTL."})
)

func init() {
	prometheus.MustRegister(inventoryRefresh, inventoryLastOK, inventoryAge, inventoryAvailable)
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
