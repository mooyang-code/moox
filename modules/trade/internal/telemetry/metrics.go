package telemetry

import "github.com/prometheus/client_golang/prometheus"

var (
	Fills                 = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_fills_total", Help: "Private or reconciled fills by result."}, []string{"source", "result"})
	TargetExecutions      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_executions_total", Help: "Target execution transitions by status."}, []string{"status"})
	Attention             = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_trade_attention_active", Help: "Trade conditions that need a person, with account and reason in the detail label."}, []string{"kind", "detail"})
	TargetDeliveryActions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_delivery_actions_total", Help: "Target delivery decisions and transport action outcomes."}, []string{"decision", "error_code", "action_result"})
)

func init() {
	prometheus.MustRegister(Fills, TargetExecutions, TargetDeliveryActions, Attention)
}
