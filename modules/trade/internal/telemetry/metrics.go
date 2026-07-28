package telemetry

import (
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Commands         = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_commands_total", Help: "Trade commands by bounded command and result."}, []string{"command", "result"})
	Submissions      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_exchange_submissions_total", Help: "Exchange submissions by result."}, []string{"result"})
	Fills            = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_fills_total", Help: "Private or reconciled fills by result."}, []string{"source", "result"})
	UnknownOrders    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "moox_trade_unknown_orders", Help: "Orders awaiting exchange truth."})
	TargetExecutions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_trade_target_executions_total", Help: "Target execution transitions by status."}, []string{"status"})
	OperationLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "moox_trade_operation_duration_seconds", Help: "Trade operation latency.", Buckets: prometheus.DefBuckets}, []string{"operation"})

	privateMu        sync.RWMutex
	privateConnected = map[string]bool{}
	privateExpected  = map[string]bool{}
)

func init() {
	prometheus.MustRegister(Commands, Submissions, Fills, UnknownOrders, TargetExecutions, OperationLatency)
}

func RecordModuleStage(stage, result string, watermark time.Time) {
	_ = report.ObserveModuleRun("trade", stage, result, "trade-target", time.Now())
	if !watermark.IsZero() {
		_ = report.ObserveModuleInputWatermark("trade", stage, "trade-target", watermark)
		if result == "success" {
			_ = report.ObserveModuleWatermark("trade", stage, "trade-target", watermark)
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
