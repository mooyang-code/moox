package telemetry

import "github.com/prometheus/client_golang/prometheus"

var (
	Fills                 = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_fills_total", Help: "Private or reconciled fills by result."}, []string{"source", "result"})
	TargetExecutions      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_executions_total", Help: "Target execution transitions by status."}, []string{"status"})
	TargetDeliveryActions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_delivery_actions_total", Help: "Target delivery decisions and transport action outcomes."}, []string{"decision", "error_code", "action_result"})
)

func init() {
	prometheus.MustRegister(Fills, TargetExecutions, TargetDeliveryActions)
}
