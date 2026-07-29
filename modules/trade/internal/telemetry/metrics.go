package telemetry

import "github.com/prometheus/client_golang/prometheus"

var (
	Fills            = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_fills_total", Help: "Private or reconciled fills by result."}, []string{"source", "result"})
	TargetExecutions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_executions_total", Help: "Target execution transitions by status."}, []string{"status"})
)

func init() {
	prometheus.MustRegister(Fills, TargetExecutions)
}
