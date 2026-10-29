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
	// ParamStale 是开算一期时没能用上最新提交值、改用本地缓存值的次数，按原因（ahead：读到超前的提交；unreachable：管理端不可达）。
	ParamStale *prometheus.CounterVec
	// 自定义算子（wasm UDF）：每批调用耗时、失败次数（按原因）、拷进模块的字节数、各版本模块的状态（1 表示处于该状态）。
	UDFCallSeconds *prometheus.HistogramVec
	UDFFailures    *prometheus.CounterVec
	UDFCopyBytes   *prometheus.CounterVec
	UDFModuleState *prometheus.GaugeVec
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	metrics := &Metrics{
		PeriodDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "factor_period_duration_seconds", Help: "Duration of factor period pipeline stages.",
			Buckets: prometheus.DefBuckets,
		}, []string{"task", "stage"}),
		PeriodLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_period_lag_seconds", Help: "Lag between the target period and its completion.",
		}, []string{"task"}),
		PeriodTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_period_total", Help: "Number of completed factor periods.",
		}, []string{"task", "status"}),
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_failures_total", Help: "Number of factor calculation failures.",
		}, []string{"task", "factor", "reason"}),
		RunnerBacklog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_runner_backlog", Help: "Number of queued periods for each factor task runner.",
		}, []string{"task"}),
		CompileErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_compile_errors_total", Help: "引擎侧编译因子定义失败的次数（mgr 与引擎的 expr 版本不一致时出现）。",
		}, []string{"task_id", "factor_id"}),
		LastPeriodTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_last_period_time", Help: "Unix timestamp of the most recently computed period.",
		}, []string{"task"}),
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
		ParamStale: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_param_stale_total", Help: "开算一期时用本地缓存的提交值而不是最新提交值的次数，按原因（ahead、unreachable）。",
		}, []string{"task_id", "reason"}),
		UDFCallSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "factor_udf_call_seconds", Help: "Duration of one batched custom operator (wasm UDF) call.", Buckets: prometheus.DefBuckets,
		}, []string{"udf"}),
		UDFFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_udf_failures_total", Help: "Number of failed custom operator calls, by reason (timeout, trap, memory, budget, code, instantiate, input, other).",
		}, []string{"udf", "reason"}),
		UDFCopyBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "factor_udf_copy_bytes_total", Help: "Bytes copied into custom operator instances (window inputs and parameters).",
		}, []string{"udf"}),
		UDFModuleState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "factor_udf_module_state", Help: "1 for the state each custom operator module version is in (ready, fetching, fetch_failed, compile_failed).",
		}, []string{"udf", "hash", "state"}),
	}
	if err := registerer.Register(metrics.PeriodDuration); err != nil {
		return nil, err
	}
	collectors := []prometheus.Collector{
		metrics.PeriodLag, metrics.PeriodTotal, metrics.Failures, metrics.RunnerBacklog, metrics.CompileErrors, metrics.LastPeriodTime,
		metrics.GapPending, metrics.GapSkipped, metrics.GapGaveUp, metrics.GapLedgerDegraded, metrics.SeriesChangeGaps,
		metrics.ParamStale, metrics.UDFCallSeconds, metrics.UDFFailures, metrics.UDFCopyBytes, metrics.UDFModuleState,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}
