package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Commands         = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_commands_total", Help: "Trade commands by bounded command and result."}, []string{"command", "result"})
	Submissions      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_exchange_submissions_total", Help: "Exchange submissions by result."}, []string{"result"})
	Fills            = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_fills_total", Help: "Private or reconciled fills by result."}, []string{"source", "result"})
	UnknownOrders    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_trade_unknown_orders", Help: "Orders awaiting exchange truth."})
	TargetExecutions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_executions_total", Help: "Target execution transitions by status."}, []string{"status"})
	OperationLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_trade_operation_duration_seconds", Help: "Trade operation latency.", Buckets: prometheus.DefBuckets}, []string{"operation"})
)

func init() {
	prometheus.MustRegister(Commands, Submissions, Fills, UnknownOrders, TargetExecutions, OperationLatency)
}
