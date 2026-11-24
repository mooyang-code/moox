package input

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// unifiedMarketSubjects 读统一数据集（方式 B）每个市场的标的与活跃状态：数据集挂的标签按标签自己的市场类型归到市场，
// 标签的活跃成员就是这个市场的标的，同时要求标的在数据集里是活跃或停牌的绑定。这样永续下架而现货还在时，标的只出现在现货里，
// 永续不可用、现货照常可用；只在永续上市的币，不算现货缺失。每个市场再按成员自己的交易状态决定：trading 为 Active，
// suspended 为 Suspended（已持有的保持持仓，见 frame.go），delisted 不产出。现货、永续同时停牌时绑定只是停牌，标的仍要逐市场判断，
// 不能整只丢掉。返回的标的不带 series_tag（读行时不按序列过滤，读全量后按行的 series_tag 拆市场）。
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
		bound[binding.SubjectID] = binding.Active || binding.Suspended
	}
	// 市场 → 标的 → 是否停牌。同一市场挂了多个标签时，任一标签上可交易就算可交易（与 Storage 按绑定聚合的口径一致）。
	members := make(map[string]map[string]bool, len(markets))
	for _, tagID := range tagIDs {
		tag, err := l.Client.GetTag(ctx, spaceID, tagID)
		if err != nil {
			return nil, err
		}
		if tag.MarketType != dsl.MarketSpot && tag.MarketType != dsl.MarketSwap {
			return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("统一数据集 %s 的标签 %s 的市场类型是 %q，只能是 spot 或 swap", datasetID, tagID, tag.MarketType)}
		}
		tagMembers, err := l.Client.ListTagMembers(ctx, spaceID, tagID)
		if err != nil {
			return nil, err
		}
		if members[tag.MarketType] == nil {
			members[tag.MarketType] = make(map[string]bool, len(tagMembers))
		}
		for _, member := range tagMembers {
			id := strings.TrimSpace(member.SubjectID)
			switch member.TradingStatus {
			case TradingStatusDelisted:
				continue
			case TradingStatusSuspended:
				if _, seen := members[tag.MarketType][id]; !seen {
					members[tag.MarketType][id] = true
				}
			default:
				members[tag.MarketType][id] = false
			}
		}
	}
	if len(tagIDs) == 0 {
		return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("统一数据集 %s 没有标的范围标签，无法按市场划分标的", datasetID)}
	}
	out := make(map[string][]Subject, len(markets))
	for _, market := range markets {
		subjects := make([]Subject, 0, len(members[market]))
		for _, id := range sortedKeys(members[market]) {
			if !bound[id] {
				continue
			}
			suspended := members[market][id]
			subjects = append(subjects, Subject{SubjectID: id, Active: !suspended, Suspended: suspended})
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
		return coverageUnusable(view.ViewID, err, fmt.Sprintf("无法确认%s所需的历史", resolved.LongestAgeLabel()))
	}
	from, to, err := AgeWindow(resolved.Calendar, resolved.Bar, bounds.To, resolved.LongestAge())
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
		return fmt.Errorf("按市场核对%s所需的历史：%w", resolved.LongestAgeLabel(), err)
	}
	for _, market := range resolved.Markets() {
		if len(rowsOfMarket(rows, market)) == 0 {
			return fmt.Errorf("统一数据集的 View %s 里%s市场的序列在 %s 至 %s 内没有数据，无法满足%s：该市场的标的会一直被当作新上市剔除；请等该市场积累够历史（或先补导入）再启用，或减小括号里的字段",
				view.ViewID, marketLabel(market), PeriodLabel(resolved.Calendar, resolved.Bar, from), PeriodLabel(resolved.Calendar, resolved.Bar, to.Add(-time.Nanosecond)), resolved.LongestAgeLabel())
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
