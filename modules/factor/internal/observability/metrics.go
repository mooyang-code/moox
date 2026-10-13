package observability

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	PeriodDuration *prometheus.HistogramVec
	PeriodLag      *prometheus.GaugeVec
	PeriodTotal    *prometheus.CounterVec
	Failures       *prometheus.CounterVec
	LaneBacklog    *prometheus.GaugeVec
	PythonBusy     prometheus.Gauge
	LastPeriodTime *prometheus.GaugeVec
	// K 线空洞账本：待恢复条目数、累计跳过与累计放弃（交给人工）的条目数、账本是否写盘失败。
	GapPending        prometheus.Gauge
	GapSkipped        prometheus.Counter
	GapGaveUp         prometheus.Counter
	GapLedgerDegraded prometheus.Gauge
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	metrics := &Metrics{
		PeriodDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "factor_period_duration_seconds", Help: "Duration of factor period pipeline stages.",
			Buckets: prometheus.DefBuckets,
		}, []string{"set", "stage"}),
		PeriodLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_period_lag_seconds", Help: "Lag between the target period and its completion.",
		}, []string{"set"}),
		PeriodTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_period_total", Help: "Number of completed factor periods.",
		}, []string{"set", "status"}),
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_failures_total", Help: "Number of factor calculation failures.",
		}, []string{"set", "factor", "reason"}),
		LaneBacklog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_lane_backlog", Help: "Number of queued periods for each factor set lane.",
		}, []string{"set"}),
		PythonBusy: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "factor_python_busy", Help: "Number of busy Python workers.",
		}),
		LastPeriodTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_last_period_time", Help: "Unix timestamp of the most recently computed period.",
		}, []string{"set"}),
		GapPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "factor_gap_pending", Help: "Number of window-gap entries waiting to be repaired.",
		}),
		GapSkipped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "factor_gap_skipped_total", Help: "Number of (subject, period, factor) entries skipped because of a window gap.",
		}),
		GapGaveUp: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "factor_gap_gave_up_total", Help: "Number of window-gap entries given up and left for manual repair.",
		}),
		GapLedgerDegraded: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "factor_gap_ledger_degraded", Help: "1 when the last window-gap ledger write failed.",
		}),
	}
	if err := registerer.Register(metrics.PeriodDuration); err != nil {
		return nil, err
	}
	collectors := []prometheus.Collector{
		metrics.PeriodLag, metrics.PeriodTotal, metrics.Failures, metrics.LaneBacklog, metrics.PythonBusy, metrics.LastPeriodTime,
		metrics.GapPending, metrics.GapSkipped, metrics.GapGaveUp, metrics.GapLedgerDegraded,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}
