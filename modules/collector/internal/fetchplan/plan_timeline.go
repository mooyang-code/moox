package fetchplan

import (
	"sort"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// 标的时间线（t_collector_series_timeline）只在三处写入，都与采集计划记录在同一个事务里：
//
//	计划建立时：不再采集的数据集（Gone）写一行空序列，从它的生效周期起停止；只有交易所符号变了的数据集（SymbolsChanged）
//	写一行同样的序列、新的符号，从生效周期起生效（planCreatedTimeline）；
//	Storage 确认登记后：标的集合变了的数据集写一行新序列（连同符号），从生效周期起生效（seriesTimelineEntry）；
//	重新登记时：整个空间的行全部删掉（ClearRegisteredSnapshots）。
//
// 未生效的计划不会被撤销或被新计划取代：一个空间同时只有一份未生效的计划（唯一索引，Create 返回 ErrFetchPlanOpen），
// 对账遇到它只会继续推进（advance），没有删计划记录的路径，重新登记也要求没有未生效的计划。
// 计划里唯一会改的是登记之前因生效周期过期而重选的 P，那时这个数据集还没有写过时间线；只有符号变化的数据集不登记，P 不会重选。
// 所以登记后就写、不等生效，与改造前“已登记或已生效的计划都算”的重放结果一致，也不会留下没生效的行。
// 下一份计划要等这份过了窗口结束（每个变更数据集的 P 加允许迟到）才建，选出的 P 一定更晚，同一生效点不会重复写入。

// seriesTimelineEntry 是序列或符号变化的数据集在时间线上的一行：从 P 起应有 change.Series，交易所符号是 change.Symbols。
func seriesTimelineEntry(spaceID string, change *PlanDatasetChange) store.SeriesTimelineEntry {
	symbols := make(map[string]string, len(change.Symbols))
	for subject, symbol := range change.Symbols {
		symbols[subject] = symbol
	}
	return store.SeriesTimelineEntry{
		SpaceID: spaceID, DatasetID: change.DatasetID, Frequency: change.Frequency, EffectiveFrom: change.P,
		SeriesHash: change.Hash, Series: append([]seriesset.Series(nil), change.Series...), Symbols: symbols,
		LatenessS: change.LatenessS, OutputFields: append([]string(nil), change.OutputFields...),
	}
}

// planCreatedTimeline 返回这份计划一建立就成立的时间线行，按“数据集|频率”排序：不再采集的数据集的停止行（空序列），
// 与只有交易所符号变了的数据集从 P 起的新符号行。
func (c *FetchPlanContent) planCreatedTimeline(spaceID string) []store.SeriesTimelineEntry {
	keys := make([]string, 0, len(c.Datasets))
	for key, change := range c.Datasets {
		if change.Changed && change.P > 0 && (change.Gone || change.SymbolsChanged) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	entries := make([]store.SeriesTimelineEntry, 0, len(keys))
	for _, key := range keys {
		change := c.Datasets[key]
		if change.Gone {
			entries = append(entries, store.SeriesTimelineEntry{SpaceID: spaceID, DatasetID: change.DatasetID, Frequency: change.Frequency, EffectiveFrom: change.P})
			continue
		}
		entries = append(entries, seriesTimelineEntry(spaceID, change))
	}
	return entries
}
