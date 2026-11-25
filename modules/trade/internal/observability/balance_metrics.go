package observability

import (
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	defaultBalanceMetricsOnce sync.Once
	defaultBalanceMetrics     *BalanceMetrics
	defaultBalanceMetricsErr  error
)

// BalanceMetrics 记余额同步：运行次数计数供本机排查，各账户的最近状态用来判断关注项 balance_sync_failing。
type BalanceMetrics struct {
	runs     *prometheus.CounterVec
	mu       sync.Mutex
	accounts map[string]balanceState
}

type balanceState struct {
	lastSuccess         float64
	maxDifference       float64
	consecutiveFailures float64
}

func DefaultBalanceMetrics() (*BalanceMetrics, error) {
	defaultBalanceMetricsOnce.Do(func() {
		defaultBalanceMetrics, defaultBalanceMetricsErr = NewBalanceMetrics(prometheus.DefaultRegisterer)
	})
	return defaultBalanceMetrics, defaultBalanceMetricsErr
}

func NewBalanceMetrics(registerer prometheus.Registerer) (*BalanceMetrics, error) {
	metrics := &BalanceMetrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_trade_balance_sync_runs_total",
			Help: "Trade balance synchronization runs grouped by result.",
		}, []string{"result"}),
		accounts: make(map[string]balanceState),
	}
	if err := registerer.Register(metrics.runs); err != nil {
		return nil, err
	}
	return metrics, nil
}

func (m *BalanceMetrics) Observe(
	tradingAccountID string,
	now time.Time,
	maxDifference float64,
	err error,
) {
	if m == nil || tradingAccountID == "" {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	timestamp := float64(now.UTC().Unix())
	m.runs.WithLabelValues(result).Inc()
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.accounts[tradingAccountID]
	if err == nil {
		state.lastSuccess = timestamp
		state.maxDifference = max(maxDifference, 0)
		state.consecutiveFailures = 0
	} else {
		state.consecutiveFailures++
	}
	m.accounts[tradingAccountID] = state
}

func (m *BalanceMetrics) Remove(tradingAccountID string) {
	if m == nil || tradingAccountID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.accounts, tradingAccountID)
}

// 余额同步的告警线：连续失败 3 次，或最近一次成功已超过 15 分钟，或本地与交易所余额差异超过上限。
const (
	balanceFailureLimit = 3
	balanceSuccessStale = 15 * time.Minute
)

// Attention 按当前时间判断各交易账户的余额同步，返回 kind 为 kind 的关注项（采集指标时现算：“超过 15 分钟没成功”要随时间翻成 1）。
// detail 只写账户与最近成功的时刻，不写失败次数等每轮都变的量。还没成功过、失败不到 3 次的账户先不报。
func (m *BalanceMetrics) Attention(kind string, differenceLimit float64, now time.Time) map[string][]string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []string
	for account, state := range m.accounts {
		lastSuccess := time.Unix(int64(state.lastSuccess), 0).UTC()
		since := "从未成功"
		if state.lastSuccess > 0 {
			since = "最近成功 " + report.AttentionTime(lastSuccess)
		}
		switch {
		case state.consecutiveFailures >= balanceFailureLimit:
			items = append(items, fmt.Sprintf("%s 连续 %d 次以上同步失败（%s）", account, balanceFailureLimit, since))
		case state.lastSuccess > 0 && now.Sub(lastSuccess) > balanceSuccessStale:
			items = append(items, fmt.Sprintf("%s 超过 15 分钟没有同步成功（%s）", account, since))
		case state.lastSuccess > 0 && state.maxDifference > differenceLimit:
			items = append(items, fmt.Sprintf("%s 本地与交易所余额差异超过 %.2f%%", account, differenceLimit*100))
		}
	}
	if len(items) == 0 {
		return nil
	}
	return map[string][]string{kind: items}
}
