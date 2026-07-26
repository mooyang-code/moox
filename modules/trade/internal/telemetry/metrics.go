package telemetry

import (
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Commands                  = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_commands_total", Help: "Trade commands by bounded command and result."}, []string{"command", "result"})
	Submissions               = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_exchange_submissions_total", Help: "Exchange submissions by result."}, []string{"result"})
	Fills                     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_fills_total", Help: "Private or reconciled fills by result."}, []string{"source", "result"})
	UnknownOrders             = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_trade_unknown_orders", Help: "Orders awaiting exchange truth."})
	ReconciliationDifferences = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_reconciliation_differences_total", Help: "Reconciliation differences by kind."}, []string{"kind"})
	Rebalances                = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_rebalances_total", Help: "Rebalance runs by result."}, []string{"result"})
	OperationLatency          = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_trade_operation_duration_seconds", Help: "Trade operation latency.", Buckets: prometheus.DefBuckets}, []string{"operation"})

	privateMu        sync.RWMutex
	privateConnected = map[string]bool{}
	privateExpected  = map[string]bool{}
)

func init() {
	prometheus.MustRegister(Commands, Submissions, Fills, UnknownOrders, ReconciliationDifferences, Rebalances, OperationLatency)
}

func RecordModuleStage(stage, result string, watermark time.Time) {
	_ = report.ObserveModuleRun("trade", stage, result, "trade-rebalance", time.Now())
	if !watermark.IsZero() {
		_ = report.ObserveModuleInputWatermark("trade", stage, "trade-rebalance", watermark)
		if result == "success" {
			_ = report.ObserveModuleWatermark("trade", stage, "trade-rebalance", watermark)
		}
	}
}

func SetPrivateExpected(keys map[string]bool) {
	privateMu.Lock()
	privateExpected = keys
	privateMu.Unlock()
}
func SetPrivateConnected(key string, ready bool) {
	privateMu.Lock()
	privateConnected[key] = ready
	privateMu.Unlock()
}
func PrivateConnectedCount() int {
	privateMu.RLock()
	defer privateMu.RUnlock()
	n := 0
	for _, ready := range privateConnected {
		if ready {
			n++
		}
	}
	return n
}
func PrivateStreamsReady() bool {
	privateMu.RLock()
	defer privateMu.RUnlock()
	for key := range privateExpected {
		if !privateConnected[key] {
			return false
		}
	}
	return true
}
