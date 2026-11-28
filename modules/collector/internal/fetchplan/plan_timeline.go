package fetchplan

import (
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// 标的时间线（t_collector_series_timeline）只在三处写入，都与采集计划记录在同一个事务里：
//
//	计划建立时：不用登记的行——停止行、hash 没变的原地替换与加一份（planCreatedTimeline）；
//	Storage 确认登记后：要登记的数据集从 P 起的新序列（seriesTimelineEntry）；
//	重新登记时：整个空间的行全部删掉（ClearRegisteredSnapshots）。
//
// 写不写行只看内容：新内容与时间线在这个生效点上的有效内容（最后一行）相比，序列 hash（停止也算一种内容）没变，
// 就不写（只改分配的原地替换与加一份因此不写）。写了行的数据集，计划把 TimelineFrom 记成这一行的生效点。
//
// 单调性：同一数据集同一频率的新行生效点从不早于已有的最后一行。原地替换写在头份的 From，决策表要求它不早于 TimelineFrom；
// 加一份写在 Q = max(P_new, TimelineFrom)。同一生效点只覆盖最后一行（store 先删后插），BuildTimeline 的重放
// 不会出现结束早于开始的段；这一点与每轮的提前量取多少无关。
// 未生效计划里数据集的内容一旦发出过登记请求就不再改，只能撤回（撤回的数据集还没写过行）或推进，所以登记后就写、不等生效。

// seriesTimelineEntry 是数据集在时间线上从 P 起的一行：停止份是空序列，其余是 dataset.Series。
func seriesTimelineEntry(spaceID string, dataset *PlanDataset) store.SeriesTimelineEntry {
	entry := store.SeriesTimelineEntry{SpaceID: spaceID, DatasetID: dataset.DatasetID, Frequency: dataset.Frequency, EffectiveFrom: dataset.P}
	if head := dataset.Head(); head != nil && head.Stop {
		return entry
	}
	entry.SeriesHash, entry.Series = dataset.Hash, append([]seriesset.Series(nil), dataset.Series...)
	entry.LatenessS, entry.OutputFields = dataset.LatenessS, append([]string(nil), dataset.OutputFields...)
	return entry
}

// planCreatedTimeline 返回这份计划一建立就成立的时间线行（不用登记的），按“数据集|频率”排序。
func (c *FetchPlanContent) planCreatedTimeline(spaceID string) []store.SeriesTimelineEntry {
	var entries []store.SeriesTimelineEntry
	for _, key := range c.sortedDatasetKeys() {
		if dataset := c.Datasets[key]; dataset.Timeline && !dataset.Register {
			entries = append(entries, seriesTimelineEntry(spaceID, dataset))
		}
	}
	return entries
}
