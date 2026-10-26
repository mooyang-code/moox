package observability

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	PeriodDuration *prometheus.HistogramVec
	PeriodLag      *prometheus.GaugeVec
	PeriodTotal    *prometheus.CounterVec
	Failures       *prometheus.CounterVec
	RunnerBacklog  *prometheus.GaugeVec
	CompileErrors  *prometheus.CounterVec
	LastPeriodTime *prometheus.GaugeVec
	// K 线空洞账本：待恢复条目数、累计跳过与累计放弃（交给人工）的条目数、账本是否写盘失败。
	GapPending        prometheus.Gauge
	GapSkipped        prometheus.Counter
	GapGaveUp         prometheus.Counter
	GapLedgerDegraded prometheus.Gauge
	// SeriesChangeGaps 是本地时序存储的变更日志同步位点断档的次数（需要对账）。
	SeriesChangeGaps prometheus.Counter
	// 参数冻结（因子动态调参执行计划 2.10）：按来源统计的冻结次数、成员连续沿用的期数、离线冻结次数与补报冲突次数。
	ParamFreezes          *prometheus.CounterVec
	ParamCarriedPeriods   *prometheus.GaugeVec
	ParamOfflineFreezes   *prometheus.CounterVec
	ParamOfflineConflicts *prometheus.CounterVec
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
		RunnerBacklog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_runner_backlog", Help: "Number of queued periods for each factor task runner.",
		}, []string{"set"}),
		CompileErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_compile_errors_total", Help: "引擎侧编译因子定义失败的次数（mgr 与引擎的 expr 版本不一致时出现）。",
		}, []string{"task_id", "factor_id"}),
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
		SeriesChangeGaps: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "factor_series_change_gaps_total", Help: "Number of times a local series source lost its change-log cursor and needs reconciliation.",
		}),
		ParamFreezes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_param_freezes_total", Help: "每个成员每期冻结参数的次数，按来源（requested、carried、carried_offline、default）。",
		}, []string{"task_id", "source"}),
		ParamCarriedPeriods: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_param_carried_periods", Help: "成员连续沿用（没有新提交）参数的期数；提交生效后归零。",
		}, []string{"task_id", "member"}),
		ParamOfflineFreezes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_param_offline_freezes_total", Help: "FactorMgr 不可达时引擎在本地冻结参数的成员期数。",
		}, []string{"task_id"}),
		ParamOfflineConflicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_param_offline_conflicts_total", Help: "补报离线冻结时管理端已有不同记录的成员期数（以管理端为准，需要手工重算）。",
		}, []string{"task_id"}),
	}
	if err := registerer.Register(metrics.PeriodDuration); err != nil {
		return nil, err
	}
	collectors := []prometheus.Collector{
		metrics.PeriodLag, metrics.PeriodTotal, metrics.Failures, metrics.RunnerBacklog, metrics.CompileErrors, metrics.LastPeriodTime,
		metrics.GapPending, metrics.GapSkipped, metrics.GapGaveUp, metrics.GapLedgerDegraded, metrics.SeriesChangeGaps,
		metrics.ParamFreezes, metrics.ParamCarriedPeriods, metrics.ParamOfflineFreezes, metrics.ParamOfflineConflicts,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}
