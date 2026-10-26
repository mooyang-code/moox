package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
)

// strategyParamStaleMetric 是策略模块导出的参数投递滞后指标（标签 space_id、instance_id）：实例在运行、策略有可变成员，
// 并且最近连续多期的结果都没有把参数送达因子侧时为 1（设计 6.5、S7）。
const strategyParamStaleMetric = "moox_strategy_param_delivery_stale"

// buildFactorParamHealth 消费策略模块导出的参数投递滞后指标：投递一直失败或被拒绝时，可变成员在沿用旧参数，
// 继续沿用、不自动回退默认值，只告警让人看。
func (b Builder) buildFactorParamHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	stale, seen, err := b.paramDeliverySeries(ctx, spaceID, now)
	if err != nil {
		return nil, err
	}
	spaces := make([]string, 0, len(seen))
	for space := range seen {
		spaces = append(spaces, space)
	}
	sort.Strings(spaces)
	var out []BusinessStatus
	for _, space := range spaces {
		if list := stale[space]; len(list) > 0 {
			sort.Strings(list)
			out = append(out, BusinessStatus{SpaceID: space, Kind: "factor_params", Module: "delivery", Status: "down",
				Reason: "策略连续多期没有把参数送达因子侧（可变成员在沿用旧参数）：" + strings.Join(list, "；"), LastCheckedAt: now})
			continue
		}
		out = append(out, BusinessStatus{SpaceID: space, Kind: "factor_params", Module: "delivery", Status: "healthy", Reason: "策略参数投递正常", LastCheckedAt: now})
	}
	return out, nil
}

// paramDeliverySeries 读取参数投递滞后指标的全部序列，按空间收集告警的实例；seen 是出现过序列的空间。
func (b Builder) paramDeliverySeries(ctx context.Context, spaceID string, now time.Time) (map[string][]string, map[string]bool, error) {
	// 目录一次最多返回 500 条：按 series_id 翻页读完整个指标，实例很多时排序靠后的异常实例也要参与检查。
	var series []monmetrics.MetricSeries
	for after := ""; ; {
		page, err := b.Metrics.Catalog().FindMetricSeriesAfterAt(ctx, strategyParamStaleMetric, after, 500, now)
		if err != nil {
			return nil, nil, err
		}
		series = append(series, page...)
		if len(page) < 500 {
			break
		}
		after = page[len(page)-1].SeriesID
	}
	alerts, seen := map[string][]string{}, map[string]bool{}
	for _, item := range series {
		if item.IsStale {
			continue
		}
		labels, err := datasetLabels(item.LabelsJSON)
		if err != nil {
			return nil, nil, fmt.Errorf("%s labels for %s: %w", strategyParamStaleMetric, item.SeriesID, err)
		}
		space := strings.TrimSpace(labels["space_id"])
		if space == "" || (spaceID != "" && space != spaceID) {
			continue
		}
		latest, err := b.Metrics.Latest(ctx, item.SeriesID)
		if err != nil {
			return nil, nil, err
		}
		seen[space] = true
		if latest.Value >= 1 {
			alerts[space] = append(alerts[space], labels["instance_id"])
		}
	}
	return alerts, seen, nil
}
