package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
	UpdateAppliedPlan(ctx context.Context, id int64, plan string) error
}

// ClearRegisteredSnapshots 把最近一份已生效计划里每个数据集的 hash 清空，返回这些“数据集|频率”（排序）。
// Storage 里登记的序列快照被清空后（例如重置全部 Storage 数据），采集函数的周期提交会因“快照缺失”被拒绝，
// 而对账只在数据集标的集合 hash 变化时才重新登记：hash 清空后，下一轮对账会把全部数据集当作变化，
// 生成新计划、重新登记快照并选新的生效周期。已退役的数据集（Gone）不动。
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
	keys := make([]string, 0, len(content.Datasets))
	for key, change := range content.Datasets {
		if change == nil || change.Gone {
			continue
		}
		change.Hash = ""
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, ErrReregisterNoPlan
	}
	sort.Strings(keys)
	if err := plans.UpdateAppliedPlan(ctx, applied.ID, mustJSON(content)); err != nil {
		return nil, fmt.Errorf("update applied fetch plan: %w", err)
	}
	return keys, nil
}
