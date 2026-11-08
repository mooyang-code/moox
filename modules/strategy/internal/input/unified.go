package input

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// unifiedMarketSubjects 读统一数据集（方式 B）每个市场的标的与活跃状态：数据集挂的标签按标签自己的市场类型归到市场，
// 标签的活跃成员就是这个市场的标的，同时要求标的在数据集里是活跃绑定。这样永续下架而现货还在时，标的只出现在现货里，
// 永续不可用、现货照常可用；只在永续上市的币，不算现货缺失。返回的标的不带 series_tag（读行时不按序列过滤，读全量后按行的 series_tag 拆市场）。
func (l Loader) unifiedMarketSubjects(ctx context.Context, spaceID, datasetID string, markets []string) (map[string][]Subject, error) {
	tagIDs, err := datasetTagsOf(ctx, l.Client, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	bindings, err := l.Client.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	bound := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		bound[binding.SubjectID] = binding.Active
	}
	members := make(map[string]map[string]struct{}, len(markets))
	for _, tagID := range tagIDs {
		tag, err := l.Client.GetTag(ctx, spaceID, tagID)
		if err != nil {
			return nil, err
		}
		if tag.MarketType != dsl.MarketSpot && tag.MarketType != dsl.MarketSwap {
			return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("统一数据集 %s 的标签 %s 的市场类型是 %q，只能是 spot 或 swap", datasetID, tagID, tag.MarketType)}
		}
		ids, err := l.Client.ListTagMembers(ctx, spaceID, tagID)
		if err != nil {
			return nil, err
		}
		if members[tag.MarketType] == nil {
			members[tag.MarketType] = make(map[string]struct{}, len(ids))
		}
		for _, id := range ids {
			members[tag.MarketType][id] = struct{}{}
		}
	}
	if len(tagIDs) == 0 {
		return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("统一数据集 %s 没有标的范围标签，无法按市场划分标的", datasetID)}
	}
	out := make(map[string][]Subject, len(markets))
	for _, market := range markets {
		ids := make([]string, 0, len(members[market]))
		for id := range members[market] {
			if bound[id] {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		subjects := make([]Subject, 0, len(ids))
		for _, id := range ids {
			subjects = append(subjects, Subject{SubjectID: strings.TrimSpace(id), Active: true})
		}
		out[market] = subjects
	}
	return out, nil
}

// rowsOfMarket 只保留 series_tag 对应某个市场的行。
func rowsOfMarket(rows []Row, market string) []Row {
	var out []Row
	for _, row := range rows {
		if seriesset.SeriesTagMarket(row.SeriesTag) == market {
			out = append(out, row)
		}
	}
	return out
}

// checkUnifiedAgeCoverage 在启用统一数据集上的实例时，按市场核对 min_age_bars 需要的历史：以最新一根为 T，读 T−(N−1) 根附近的年龄探针窗口，
// 策略涉及的每个市场自己的序列都至少要有一行，否则这个市场的标的会一直被当作新上市剔除。
func checkUnifiedAgeCoverage(ctx context.Context, client Client, spaceID string, view ViewInfo, resolved Resolved, now time.Time) error {
	bounds, err := CoverageBounds(view, resolved, now)
	if err != nil {
		return coverageUnusable(view.ViewID, err, fmt.Sprintf("无法确认 universe.min_age_bars=%d 所需的历史", resolved.MinAgeBars))
	}
	from, to, err := AgeWindow(resolved.Calendar, resolved.Bar, bounds.To, resolved.MinAgeBars)
	if err != nil {
		return err
	}
	dataset := resolved.TaskOf(resolved.Markets()[0]).DatasetID
	bindings, err := client.ListDatasetSubjects(ctx, spaceID, dataset)
	if err != nil {
		return err
	}
	var subjects []Subject
	for _, binding := range bindings {
		if binding.Active {
			subjects = append(subjects, Subject{SubjectID: binding.SubjectID})
		}
	}
	rows, _, err := client.QueryRows(ctx, spaceID, Query{ViewID: view.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: subjects, Start: from, End: to, Columns: []string{ageProbeColumn}, ExpectedIndexID: view.ActiveIndexID})
	if err != nil {
		return fmt.Errorf("按市场核对 universe.min_age_bars 所需的历史：%w", err)
	}
	for _, market := range resolved.Markets() {
		if len(rowsOfMarket(rows, market)) == 0 {
			return fmt.Errorf("统一数据集的 View %s 里%s市场的序列在 %s 至 %s 内没有数据，无法满足 universe.min_age_bars=%d：该市场的标的会一直被当作新上市剔除；请等该市场积累够历史（或先补导入）再启用",
				view.ViewID, marketLabel(market), PeriodLabel(resolved.Calendar, resolved.Bar, from), PeriodLabel(resolved.Calendar, resolved.Bar, to.Add(-time.Nanosecond)), resolved.MinAgeBars)
		}
	}
	return nil
}

func marketLabel(market string) string {
	if market == "swap" {
		return "永续"
	}
	return "现货"
}
