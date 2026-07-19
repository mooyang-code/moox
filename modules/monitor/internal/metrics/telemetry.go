package metrics

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

type PipelineVerdict struct{ Status, Reason string }
type PipelineSignals struct {
	EnabledWorkloads       int
	InputWatermark         time.Time
	OutputWatermark        time.Time
	PreviousInputWatermark time.Time
	LagTolerance           time.Duration
	LegalEmptyOutput       bool
	CrossesStorageDeferred bool
}

func EvaluatePipelineSignals(signals PipelineSignals, now time.Time) PipelineVerdict {
	if signals.CrossesStorageDeferred {
		return PipelineVerdict{Status: "SKIPPED", Reason: "storage_observability_deferred"}
	}
	if signals.EnabledWorkloads == 0 {
		return PipelineVerdict{Status: "SKIPPED", Reason: "no_enabled_workload"}
	}
	if signals.InputWatermark.IsZero() || !signals.InputWatermark.After(signals.PreviousInputWatermark) {
		return PipelineVerdict{Status: "UNKNOWN", Reason: "input_not_advancing"}
	}
	if signals.LegalEmptyOutput {
		return PipelineVerdict{Status: "PASS", Reason: "legal_empty_output"}
	}
	if signals.LagTolerance <= 0 {
		return PipelineVerdict{Status: "UNKNOWN", Reason: "invalid_lag_tolerance"}
	}
	if signals.OutputWatermark.IsZero() || signals.InputWatermark.Sub(signals.OutputWatermark) > signals.LagTolerance || now.Sub(signals.OutputWatermark) > signals.LagTolerance {
		return PipelineVerdict{Status: "FAIL", Reason: fmt.Sprintf("output_stalled_over_%s", signals.LagTolerance)}
	}
	return PipelineVerdict{Status: "PASS", Reason: "within_lag_tolerance"}
}

var (
	ingestTotal       = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_monitor_metrics_ingest_total", Help: "Metric snapshot ingestion outcomes."}, []string{"result"})
	ingestLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_ingest_last_success_timestamp_seconds", Help: "Last successful metric snapshot ingestion."})
	ingestLatency     = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "moox_monitor_metrics_ingest_latency_seconds", Help: "Delay between snapshot occurrence and ingestion."})
	consumerPending   = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_monitor_metrics_consumer_pending", Help: "Fetched metric deliveries awaiting handling."})
	dlqTotal          = prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_monitor_metrics_dlq_total", Help: "Metric snapshots sent to the DLQ."})
)

func init() {
	prometheus.MustRegister(ingestTotal, ingestLastSuccess, ingestLatency, consumerPending, dlqTotal)
}

func recordIngest(result string, observed time.Time) {
	now := time.Now().UTC()
	ingestTotal.WithLabelValues(result).Inc()
	_ = report.ObserveModuleRun("monitor", "ingest", result, "monitor-metrics", now)
	if result == "success" {
		ingestLastSuccess.Set(float64(now.Unix()))
		if !observed.IsZero() && !observed.After(now) {
			ingestLatency.Observe(now.Sub(observed).Seconds())
		}
		_ = report.ObserveModuleWatermark("monitor", "ingest", "monitor-metrics", observed)
	}
}
