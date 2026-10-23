package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
)

// factorOfflineFreezeWindow 是“引擎离线冻结参数”的告警窗口：最近这段时间内出现过就告警。
const factorOfflineFreezeWindow = 30 * time.Minute

// buildFactorParamHealth 消费因子管理服务导出的参数指标（因子动态调参执行计划 2.10）：
//   - 成员连续沿用参数超过阈值（默认 10 期）：参数写入方没有提交新参数，因子在沿用旧值。继续沿用、不自动回退默认值，只告警让人看。
//   - 引擎离线冻结：FactorMgr 不可达时引擎在本地冻结参数，补报时管理端以自己的记录为准；最近 30 分钟内出现过就告警。
func (b Builder) buildFactorParamHealth(ctx context.Context, spaceID string, now time.Time) ([]BusinessStatus, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	carried, carriedSeen, err := b.factorParamSeries(ctx, spaceID, now, "moox_factor_param_carry_alert", func(value float64, _ map[string]string) (string, bool) {
		return "", value >= 1
	}, func(labels map[string]string) string { return labels["set_id"] + "/" + labels["member"] })
	if err != nil {
		return nil, err
	}
	offline, offlineSeen, err := b.factorParamSeries(ctx, spaceID, now, "moox_factor_param_offline_freeze_timestamp_seconds", func(value float64, _ map[string]string) (string, bool) {
		if value <= 0 || now.Sub(unixTime(value)) > factorOfflineFreezeWindow {
			return "", false
		}
		return "（最近 " + unixTime(value).Format(time.RFC3339) + "）", true
	}, func(labels map[string]string) string { return labels["set_id"] })
	if err != nil {
		return nil, err
	}
	var out []BusinessStatus
	emit := func(seen map[string]bool, alerts map[string][]string, module, down, healthy string) {
		spaces := make([]string, 0, len(seen))
		for space := range seen {
			spaces = append(spaces, space)
		}
		sort.Strings(spaces)
		for _, space := range spaces {
			if list := alerts[space]; len(list) > 0 {
				sort.Strings(list)
				out = append(out, BusinessStatus{SpaceID: space, Kind: "factor_params", Module: module, Status: "down", Reason: down + strings.Join(list, "；"), LastCheckedAt: now})
				continue
			}
			out = append(out, BusinessStatus{SpaceID: space, Kind: "factor_params", Module: module, Status: "healthy", Reason: healthy, LastCheckedAt: now})
		}
	}
	emit(carriedSeen, carried, "carry", "成员连续沿用参数超过阈值（参数写入方没有提交新参数，因子在沿用旧值）：", "因子参数没有长期沿用")
	emit(offlineSeen, offline, "offline_freeze", "引擎在 FactorMgr 不可达时离线冻结了参数（补报时以管理端为准）：", "没有离线冻结参数")
	return out, nil
}

// factorParamSeries 读取一个指标的全部序列，按空间收集满足 alert 的序列描述；seen 是出现过序列的空间。
func (b Builder) factorParamSeries(ctx context.Context, spaceID string, now time.Time, metric string, alert func(float64, map[string]string) (string, bool), describe func(map[string]string) string) (map[string][]string, map[string]bool, error) {
	// 目录一次最多返回 500 条：按 series_id 翻页读完整个指标，成员很多时排序靠后的异常成员也要参与检查。
	var series []monmetrics.MetricSeries
	for after := ""; ; {
		page, err := b.Metrics.Catalog().FindMetricSeriesAfterAt(ctx, metric, after, 500, now)
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
			return nil, nil, fmt.Errorf("%s labels for %s: %w", metric, item.SeriesID, err)
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
		if suffix, hit := alert(latest.Value, labels); hit {
			alerts[space] = append(alerts[space], describe(labels)+suffix)
		}
	}
	return alerts, seen, nil
}
