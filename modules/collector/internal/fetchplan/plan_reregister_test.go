package fetchplan

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

type reregisterStore struct {
	open, applied *store.FetchPlanRecord
	// reregisterErr 是 ReregisterAppliedPlan 的返回值，用来模拟事务里的复查。
	reregisterErr error
}

func (s *reregisterStore) Open(context.Context, string) (*store.FetchPlanRecord, error) {
	return s.open, nil
}
func (s *reregisterStore) LatestApplied(context.Context, string) (*store.FetchPlanRecord, error) {
	return s.applied, nil
}
func (s *reregisterStore) ReregisterAppliedPlan(_ context.Context, _ int64, plan string) error {
	if s.reregisterErr != nil {
		return s.reregisterErr
	}
	s.applied.Plan = plan
	return nil
}

// 数据集头份的 hash 清空后，对账把这些数据集当作变化（头份在未来的原地替换、按原 From 登记，其余加一份），函数保持不变（不重建）；
// 带两份的数据集裁成只剩头份，否则旧份会被当成未过窗口而一直等待。清空之前同样的期望状态没有变化。
func TestClearRegisteredSnapshotsMakesTheNextPlanReregisterEveryDataset(t *testing.T) {
	options := PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}}, DefaultLatenessS: 15, Fetch: marketfetch.PlanFetch{Concurrency: 5}}
	group := func(subjects ...string) []TaskGroup {
		return []TaskGroup{{
			Provider: "binance", SourceID: "binance", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m",
			Subjects: subjects,
		}}
	}
	now := time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)
	desired, err := BuildDesiredState(options, group("BTC-USDT", "ETH-USDT"), nil)
	require.NoError(t, err)
	first, _, _, err := BuildPlanContent(nil, nil, desired, timing(now))
	require.NoError(t, err)
	previousFunctions, previousDatasets := first.Applied()
	_, changed, _, err := BuildPlanContent(previousFunctions, previousDatasets, desired, timing(now.Add(time.Hour)))
	require.NoError(t, err)
	require.False(t, changed, "清空之前期望状态没有变化")

	// 一小时后加一个标的：数据集带两份（旧份 08:32、头份 09:32）。
	later, err := BuildDesiredState(options, group("BTC-USDT", "ETH-USDT", "SOL-USDT"), previousFunctions)
	require.NoError(t, err)
	second, _, _, err := BuildPlanContent(previousFunctions, previousDatasets, later, timing(now.Add(time.Hour)))
	require.NoError(t, err)
	require.Len(t, second.Datasets["ds|1m"].Copies, 2)
	head := second.Datasets["ds|1m"].Head().From
	plans := &reregisterStore{applied: &store.FetchPlanRecord{ID: 7, Status: store.FetchPlanApplied, Plan: mustJSON(second)}}

	keys, err := ClearRegisteredSnapshots(context.Background(), plans, "crypto")
	require.NoError(t, err)
	require.Equal(t, []string{"ds|1m"}, keys)
	cleared, err := decodeFetchPlanContent(plans.applied.Plan)
	require.NoError(t, err)
	dataset := cleared.Datasets["ds|1m"]
	require.Len(t, dataset.Copies, 1, "只剩头份")
	require.Equal(t, head, dataset.Copies[0].From)
	require.Empty(t, dataset.Copies[0].Hash)
	require.Zero(t, dataset.TimelineFrom)
	for name, function := range cleared.Functions {
		require.Len(t, findPlanDataset(function.Target, "ds|1m").Copies, 1, "函数 %s 的 Target 只留头份", name)
	}

	previousFunctions, previousDatasets = cleared.Applied()
	desired, err = BuildDesiredState(options, group("BTC-USDT", "ETH-USDT", "SOL-USDT"), previousFunctions)
	require.NoError(t, err)
	// 头份还在未来：原地替换，按原 From 在空的 Storage 里登记。
	replace, changed, _, err := BuildPlanContent(previousFunctions, previousDatasets, desired, timing(time.Unix(head, 0).Add(-3*time.Minute)))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PlanActionReplace, replace.Datasets["ds|1m"].Action)
	require.Equal(t, head, replace.Datasets["ds|1m"].P)
	require.True(t, replace.Datasets["ds|1m"].Register)
	require.True(t, replace.Datasets["ds|1m"].Timeline, "时间线已清空，重新写行")
	// 头份已过：在新的生效点加一份并登记。
	appended, changed, _, err := BuildPlanContent(previousFunctions, previousDatasets, desired, timing(time.Unix(head, 0).Add(time.Hour)))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PlanActionAppend, appended.Datasets["ds|1m"].Action)
	require.True(t, appended.Datasets["ds|1m"].Register)
	require.Len(t, appended.Datasets["ds|1m"].Series, 3)
	for name, function := range appended.Functions {
		require.NotNil(t, function.Old, "函数 %s 不是新建的", name)
		require.NoError(t, function.Target.Validate())
	}
}

func TestClearRegisteredSnapshotsRefusesWithAnOpenPlanOrNoAppliedPlan(t *testing.T) {
	ctx := context.Background()
	_, err := ClearRegisteredSnapshots(ctx, &reregisterStore{open: &store.FetchPlanRecord{ID: 2}, applied: &store.FetchPlanRecord{ID: 1, Plan: `{"datasets":{"a|1m":{"hash":"x","copies":[{"from":60,"hash":"x","count":1}]}}}`}}, "crypto")
	require.ErrorIs(t, err, ErrReregisterPlanOpen)
	_, err = ClearRegisteredSnapshots(ctx, &reregisterStore{}, "crypto")
	require.ErrorIs(t, err, ErrReregisterNoPlan)
	_, err = ClearRegisteredSnapshots(ctx, &reregisterStore{applied: &store.FetchPlanRecord{ID: 1, Plan: `{"datasets":{"a|1m":{"hash":"x","copies":[{"from":60,"hash":"x","count":1,"stop":true}]}}}`}}, "crypto")
	require.ErrorIs(t, err, ErrReregisterNoPlan, "只剩停止的数据集时没有可重新登记的")
}

// 事务外查过没有未生效计划，对账器随后建了一份：事务里的复查拒绝（store.ErrFetchPlanOpen），按“有未生效计划”返回。
func TestClearRegisteredSnapshotsMapsTheInTransactionOpenPlanCheck(t *testing.T) {
	original := `{"datasets":{"a|1m":{"hash":"x","copies":[{"from":60,"hash":"x","count":1}]}}}`
	plans := &reregisterStore{applied: &store.FetchPlanRecord{ID: 1, Plan: original}, reregisterErr: store.ErrFetchPlanOpen}
	_, err := ClearRegisteredSnapshots(context.Background(), plans, "crypto")
	require.ErrorIs(t, err, ErrReregisterPlanOpen)
	require.Equal(t, original, plans.applied.Plan)
}
