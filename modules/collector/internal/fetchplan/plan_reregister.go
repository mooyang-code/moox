package fetchplan

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// ErrReregisterPlanOpen 表示空间还有未生效的采集计划：它生效之前不能改写已生效的计划。
var ErrReregisterPlanOpen = errors.New("space has an unapplied fetch plan")

// ErrReregisterNoPlan 表示空间还没有已生效的采集计划，没有需要重新登记的数据集。
var ErrReregisterNoPlan = errors.New("space has no applied fetch plan")

// PlanReregisterStore 是 ClearRegisteredSnapshots 需要的计划存储。
type PlanReregisterStore interface {
	Open(ctx context.Context, spaceID string) (*store.FetchPlanRecord, error)
	LatestApplied(ctx context.Context, spaceID string) (*store.FetchPlanRecord, error)
	// ReregisterAppliedPlan 改写已生效计划，并在同一个事务里删掉该空间的全部时间线行；该空间有未生效计划时返回 store.ErrFetchPlanOpen。
	ReregisterAppliedPlan(ctx context.Context, id int64, plan string) error
}

// ClearRegisteredSnapshots 把最近一份已生效计划里每个在采数据集头份的 hash 清空，返回这些“数据集|频率”（排序）。
// Storage 里登记的序列快照被清空后（例如重置全部 Storage 数据），采集函数的周期提交会因“快照缺失”被拒绝，
// 而决策表只在 hash 与头份不同时才登记：hash 清空后，下一轮对账把这些数据集当作变化，头份在未来的原地替换、按原 From 登记，
// 其余在新的生效点加一份并登记。为此还要：
//
//	每个在采数据集的份裁成只剩头份，函数 Target 里同样只留头份（旧份对应的登记已经不存在，不裁会被当成未过窗口的旧份而一直等待）；
//	停止份连同数据集一起去掉（函数因此没有数据集的，Target 置空，下一份计划里它不再出现，由读回停掉计划外的定时器）；
//	TimelineFrom 清零：同一个事务里删掉该空间的全部标的时间线，此前的时间线连同数据一起作废，
//	检查与自动补采的起点变成重新登记后的新生效周期。
func ClearRegisteredSnapshots(ctx context.Context, plans PlanReregisterStore, spaceID string) ([]string, error) {
	open, err := plans.Open(ctx, spaceID)
	if err != nil {
		return nil, fmt.Errorf("read open fetch plan: %w", err)
	}
	if open != nil {
		return nil, ErrReregisterPlanOpen
	}
	applied, err := plans.LatestApplied(ctx, spaceID)
	if err != nil {
		return nil, fmt.Errorf("read applied fetch plan: %w", err)
	}
	if applied == nil {
		return nil, ErrReregisterNoPlan
	}
	content, err := decodeFetchPlanContent(applied.Plan)
	if err != nil {
		return nil, err
	}
	heads := map[string]int64{}
	for key, dataset := range content.Datasets {
		head := dataset.Head()
		if head == nil || head.Stop {
			delete(content.Datasets, key)
			continue
		}
		head.Hash, dataset.Hash = "", ""
		dataset.Copies, dataset.TimelineFrom = []DatasetCopy{*head}, 0
		heads[key] = head.From
	}
	for _, function := range content.Functions {
		if function.Target == nil {
			continue
		}
		var kept []marketfetch.PlanDataset
		for _, dataset := range function.Target.Datasets {
			from, ok := heads[marketfetch.PlanDatasetKeyOf(dataset)]
			if copy := copyAt(&dataset, from); ok && copy != nil && len(copy.Subjects) > 0 {
				dataset.Copies = []marketfetch.PlanCopy{*copy}
				kept = append(kept, dataset)
			}
		}
		function.Target.Datasets = kept
		if len(kept) == 0 {
			function.Target, function.Cron = nil, ""
		} else if function.IsSCF() {
			function.Target.PruneExceptions()
			function.Cron = planCron(function.Target)
		}
	}
	keys := content.ClearedDatasets()
	if len(keys) == 0 {
		return nil, ErrReregisterNoPlan
	}
	if err := plans.ReregisterAppliedPlan(ctx, applied.ID, mustJSON(content)); err != nil {
		// 上面查过没有未生效计划，但对账器可能在这之后刚建了一份：事务里的复查会拒绝，按同样的原因返回。
		if errors.Is(err, store.ErrFetchPlanOpen) {
			return nil, ErrReregisterPlanOpen
		}
		return nil, fmt.Errorf("update applied fetch plan: %w", err)
	}
	return keys, nil
}

// ClearedDatasets 返回这份计划里头份 hash 被 ClearRegisteredSnapshots 清空、等待重新登记快照的在采数据集（排序）。
// 非空说明该空间 Storage 里登记的快照（连同数据）已被整体清空、还没有新计划接手；状态页据此提示重新登记迟迟没有新计划。
// 真实的标的集合 hash 不会为空（没有标的的数据集不进计划），所以空 hash 只来自 ClearRegisteredSnapshots。
func (c *FetchPlanContent) ClearedDatasets() []string {
	var keys []string
	for key, dataset := range c.Datasets {
		if head := dataset.Head(); head != nil && !head.Stop && head.Hash == "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
