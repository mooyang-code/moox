package telemetry

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	Fills                 = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_fills_total", Help: "Private or reconciled fills by result."}, []string{"source", "result"})
	TargetExecutions      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_executions_total", Help: "Target execution transitions by status."}, []string{"status"})
	Attention             = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_trade_attention_active", Help: "Trade conditions that need a person, with account and reason in the detail label."}, []string{"kind", "detail"})
	TargetDeliveryActions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_delivery_actions_total", Help: "Target delivery decisions and transport action outcomes."}, []string{"decision", "error_code", "action_result"})
	// 下单量监控：标签只用账户，不带标的和目标 ID，序列数固定。
	Orders            = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_orders_total", Help: "Order submissions by trading account and result."}, []string{"account", "result"})
	OrderCancels      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_cancels_total", Help: "Order cancel requests accepted by the venue."}, []string{"account"})
	OrderNotional     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_order_notional_total", Help: "Submitted order notional at the reference price."}, []string{"account", "asset"})
	Reversals         = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_reversals_total", Help: "Target children reversing the previous target's fill on the same lane within five minutes."}, []string{"logical_account"})
	TargetChildOrders = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_trade_target_child_orders", Help: "Child orders submitted for the current target."}, []string{"logical_account"})
	RateWeightUsed    = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_trade_rate_weight_used", Help: "Exchange request weight used in the current minute."}, []string{"host"})
	RatePauses        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_rate_pauses_total", Help: "Rate limit pauses by host and reason."}, []string{"host", "reason"})
)

// 下单结果。
const (
	OrderSubmitted        = "submitted"
	OrderRejected         = "rejected"
	OrderUnknown          = "unknown"
	OrderNotAccepted      = "not_accepted"
	OrderDiscarded        = "discarded"
	OrderDuplicateBlocked = "duplicate_blocked"
	OrderReversalBlocked  = "reversal_blocked"
)

func init() {
	prometheus.MustRegister(Fills, TargetExecutions, TargetDeliveryActions, Attention,
		Orders, OrderCancels, OrderNotional, Reversals, TargetChildOrders, RateWeightUsed, RatePauses)
}

// ExecutionMetrics 把收敛事件记成指标。每个组合账户只保留当前目标的子单数。
type ExecutionMetrics struct {
	mu      sync.Mutex
	targets map[string]string
}

func (m *ExecutionMetrics) Blocked(tradingAccountID, result string) {
	Orders.WithLabelValues(tradingAccountID, result).Inc()
}

func (m *ExecutionMetrics) ChildPlaced(logicalAccountID, targetID string, crossTargetReversal bool) {
	m.mu.Lock()
	if m.targets == nil {
		m.targets = make(map[string]string)
	}
	if m.targets[logicalAccountID] != targetID {
		m.targets[logicalAccountID] = targetID
		TargetChildOrders.WithLabelValues(logicalAccountID).Set(0)
	}
	m.mu.Unlock()
	TargetChildOrders.WithLabelValues(logicalAccountID).Inc()
	if crossTargetReversal {
		Reversals.WithLabelValues(logicalAccountID).Inc()
	}
}
