package input

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
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
