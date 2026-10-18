package marketfetch

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

type reregisterStore struct {
	open, applied *store.FetchPlanRecord
}

func (s *reregisterStore) Open(context.Context, string) (*store.FetchPlanRecord, error) {
	return s.open, nil
}
func (s *reregisterStore) LatestApplied(context.Context, string) (*store.FetchPlanRecord, error) {
	return s.applied, nil
}
func (s *reregisterStore) UpdateAppliedPlan(_ context.Context, _ int64, plan string) error {
	s.applied.Plan = plan
	return nil
}

// 数据集 hash 清空后，对账把全部数据集当作变化（重新登记快照、选新生效周期），而函数保持不变（不重建函数）；
// 清空之前同样的期望状态没有变化。
func TestClearRegisteredSnapshotsMakesTheNextPlanReregisterEveryDataset(t *testing.T) {
	options := PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}}, DefaultLatenessS: 15, Fetch: PlanFetch{Concurrency: 5}}
	desired, err := BuildDesiredState(options, []TaskGroup{{
		Provider: "binance", SourceID: "binance", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m",
		Subjects: []string{"BTC-USDT", "ETH-USDT"},
	}}, nil)
	require.NoError(t, err)
	choose := func(string) (int64, error) { return 1_000, nil }
	first, _, err := BuildPlanContent(nil, nil, desired, choose)
	require.NoError(t, err)
	plans := &reregisterStore{applied: &store.FetchPlanRecord{ID: 7, Status: store.FetchPlanApplied, Plan: mustJSON(first)}}

	previousFunctions, previousDatasets := first.Applied()
	_, changed, err := BuildPlanContent(previousFunctions, previousDatasets, desired, choose)
	require.NoError(t, err)
	require.False(t, changed, "清空之前期望状态没有变化")

	keys, err := ClearRegisteredSnapshots(context.Background(), plans, "crypto")
	require.NoError(t, err)
	require.Equal(t, []string{"ds|1m"}, keys)

	cleared, err := decodeFetchPlanContent(plans.applied.Plan)
	require.NoError(t, err)
	previousFunctions, previousDatasets = cleared.Applied()
	next, changed, err := BuildPlanContent(previousFunctions, previousDatasets, desired, choose)
	require.NoError(t, err)
	require.True(t, changed)
	dataset := next.Datasets["ds|1m"]
	require.True(t, dataset.HashChanged && dataset.Changed, "重新登记快照并选新的生效周期")
	require.Len(t, dataset.Series, 2)
	require.NotEmpty(t, previousFunctions, "函数沿用已生效的计划，不会被当作新建")
	for name, function := range next.Functions {
		require.NotNil(t, function.Old, "函数 %s 不是新建的", name)
	}
}

func TestClearRegisteredSnapshotsRefusesWithAnOpenPlanOrNoAppliedPlan(t *testing.T) {
	ctx := context.Background()
	_, err := ClearRegisteredSnapshots(ctx, &reregisterStore{open: &store.FetchPlanRecord{ID: 2}, applied: &store.FetchPlanRecord{ID: 1, Plan: `{"datasets":{"a|1m":{"hash":"x"}}}`}}, "crypto")
	require.ErrorIs(t, err, ErrReregisterPlanOpen)
	_, err = ClearRegisteredSnapshots(ctx, &reregisterStore{}, "crypto")
	require.ErrorIs(t, err, ErrReregisterNoPlan)
	_, err = ClearRegisteredSnapshots(ctx, &reregisterStore{applied: &store.FetchPlanRecord{ID: 1, Plan: `{"datasets":{"a|1m":{"hash":"x","gone":true}}}`}}, "crypto")
	require.ErrorIs(t, err, ErrReregisterNoPlan, "只剩已退役的数据集时没有可重新登记的")
}
