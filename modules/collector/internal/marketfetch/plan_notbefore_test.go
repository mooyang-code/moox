package marketfetch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPlanNotBeforeRangeIsValidated(t *testing.T) {
	for _, value := range []int{0, MinNotBeforeMS, 8000, MaxNotBeforeMS} {
		plan := testPlan()
		plan.Fetch.NotBeforeMS = value
		require.NoError(t, plan.Validate(), "not_before_ms=%d", value)
	}
	for _, value := range []int{-1, 500, MaxNotBeforeMS + 1} {
		plan := testPlan()
		plan.Fetch.NotBeforeMS = value
		require.ErrorContains(t, plan.Validate(), "not_before_ms", "not_before_ms=%d", value)
	}
}

// 取数开始不早于收盘 + not_before_ms：定时器晚到 1 秒也一样；错峰更晚时以错峰为准。
func TestPlanStartDelayHonoursNotBeforeFromTheClose(t *testing.T) {
	scheduled := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	plan := testPlan()
	plan.Fetch.StaggerMS = 0
	plan.Fetch.NotBeforeMS = 8000
	for _, late := range []time.Duration{0, 400 * time.Millisecond, time.Second} {
		entry := scheduled.Add(late)
		start := entry.Add(plan.StartDelay("fn", entry, scheduled))
		require.Equal(t, scheduled.Add(8*time.Second), start, "late=%s", late)
	}
	// 定时器晚到超过 not_before_ms：不再等待。
	require.Zero(t, plan.StartDelay("fn", scheduled.Add(9*time.Second), scheduled))
	// 错峰比 not_before_ms 更晚：按错峰。
	plan.Fetch.StaggerBaseMS = 9000
	require.Equal(t, 9*time.Second, plan.StartDelay("fn", scheduled.Add(time.Second), scheduled))
	// 合约 20 秒截止：8 秒起取数，周期截止仍远在其后，留出取数与提交的窗口。
	plan.Fetch.StaggerBaseMS = 0
	plan.Datasets[0].LatenessS = 20
	requests, err := plan.BuildRequests("crypto", "fn", "req", scheduled, nil)
	require.NoError(t, err)
	require.NotEmpty(t, requests)
	start := scheduled.Add(time.Second).Add(plan.StartDelay("fn", scheduled.Add(time.Second), scheduled))
	for _, request := range requests {
		if request.Frequency == "1m" {
			require.True(t, request.StorageDeadline.Sub(start) >= 7*time.Second, "截止 %s 开始 %s", request.StorageDeadline, start)
		}
	}
}

// 现货与合约函数拿到各自市场的拉取参数与 SCF 截止；未写覆盖的市场用空间值。
func TestBuildDesiredStateUsesPerMarketFetchAndLateness(t *testing.T) {
	options := testPlanOptions()
	options.MarketFetch = map[string]PlanFetch{"swap": {Concurrency: 5, RPS: 50, Burst: 50, FeedConcurrency: 10, NotBeforeMS: 8000}}
	options.MarketLatenessS = map[string]int{"swap": 20}
	groups := testGroups(10, "1m")
	swap := testGroups(10, "1m")[0]
	swap.MarketType, swap.InstrumentType, swap.DatasetID = "swap", "swap", "ds_swap_1m"
	groups = append(groups, swap)
	state, err := BuildDesiredState(options, groups, nil)
	require.NoError(t, err)
	for _, function := range state.Functions {
		switch function.Plan.Market.MarketType {
		case "swap":
			require.Equal(t, 8000, function.Plan.Fetch.NotBeforeMS)
			require.Equal(t, 20, function.Plan.Datasets[0].LatenessS)
		case "spot":
			require.Zero(t, function.Plan.Fetch.NotBeforeMS)
			require.Equal(t, 15, function.Plan.Datasets[0].LatenessS)
		}
	}
	require.Equal(t, 20, state.Datasets["ds_swap_1m|1m"].LatenessS)
}
