package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// counterByLabel 汇总某个指标族里带 result=<value> 标签的计数之和。
func counterByLabel(t *testing.T, registry *prometheus.Registry, name, result string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	total := 0.0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if hasLabel(metric.GetLabel(), "result", result) {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}

func hasLabel(labels []*dto.LabelPair, name, value string) bool {
	for _, label := range labels {
		if label.GetName() == name && label.GetValue() == value {
			return true
		}
	}
	return false
}

// 数据积累期（历史不足、上游因子被空洞保护跳过）：数据集记 partial，不计模块失败；超过积累期上限仍然不足就是真问题，
// 回到 incomplete 并计模块失败；出现 ok 后重新计时；与积累无关的跳过原因不受影响。
func TestObserverTreatsHistoryAccumulationAsPartialWithinTheLimit(t *testing.T) {
	repo := openStore(t)
	seedEnabled(t, repo, "i1", nil, resolvedJSON)
	registry := prometheus.NewRegistry()
	observer, err := newInstanceObserver(repo, registry, t.Logf)
	require.NoError(t, err)
	require.NoError(t, observer.refresh(context.Background()))
	instance, err := repo.GetInstance(context.Background(), "i1")
	require.NoError(t, err)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	observer.now = func() time.Time { return now }
	barEnd := now

	observer.ObservePeriod(instance, "1h", barEnd, store.StatusSkipped, "history_insufficient")
	observer.ObservePeriod(instance, "1h", barEnd.Add(time.Hour), store.StatusSkipped, "factor_skipped")
	require.EqualValues(t, 2, counterByLabel(t, registry, "moox_strategy_dataset_runs_total", "partial"))
	require.Zero(t, counterByLabel(t, registry, "moox_strategy_dataset_runs_total", "incomplete"))
	require.Zero(t, counterByLabel(t, registry, "moox_strategy_runs_total", "error"), "积累中不计模块失败")

	now = now.Add(minAccumulationLimit)
	observer.ObservePeriod(instance, "1h", barEnd.Add(2*time.Hour), store.StatusSkipped, "history_insufficient")
	require.EqualValues(t, 3, counterByLabel(t, registry, "moox_strategy_dataset_runs_total", "partial"), "上限之内（72 小时，含一小时余量）")

	now = now.Add(2 * time.Hour)
	observer.ObservePeriod(instance, "1h", barEnd.Add(3*time.Hour), store.StatusSkipped, "history_insufficient")
	require.EqualValues(t, 1, counterByLabel(t, registry, "moox_strategy_dataset_runs_total", "incomplete"), "超过上限不再是积累")
	require.EqualValues(t, 1, counterByLabel(t, registry, "moox_strategy_runs_total", "error"), "回到模块失败")

	observer.ObservePeriod(instance, "1h", barEnd.Add(4*time.Hour), store.StatusOK, "")
	require.Empty(t, observer.accumulatingSince, "ok 之后清除计时")
	observer.ObservePeriod(instance, "1h", barEnd.Add(5*time.Hour), store.StatusSkipped, "history_insufficient")
	require.EqualValues(t, 4, counterByLabel(t, registry, "moox_strategy_dataset_runs_total", "partial"), "重新计时")

	observer.ObservePeriod(instance, "1h", barEnd.Add(6*time.Hour), store.StatusSkipped, "factor_missing")
	require.EqualValues(t, 2, counterByLabel(t, registry, "moox_strategy_dataset_runs_total", "incomplete"), "与积累无关的跳过原因照旧")
}
