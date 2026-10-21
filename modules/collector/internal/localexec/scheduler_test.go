package localexec

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"github.com/stretchr/testify/require"
)

// 本地定时拉取只取 local 项，按计划构造与 SCF 相同的周期提交请求；一个市场的全部标的超过单次上限时切开执行。
func TestSchedulerRunsLocalPlansWithoutProcessEnvironment(t *testing.T) {
	var mu sync.Mutex
	var observed []marketfetch.Request
	executor := New("crypto", 2, recordingHandler(&observed, &mu))
	scheduler := NewScheduler(executor)
	subjects := make([]marketfetch.PlanSubject, 0, 250)
	for i := 0; i < 250; i++ {
		subjects = append(subjects, marketfetch.PlanSubject{ID: "S" + string(rune('A'+i/26%26)) + string(rune('A'+i%26)) + "-USDT", Index: uint32(i)})
	}
	plan := &marketfetch.Plan{V: 1, Executor: marketfetch.PlanExecutorLocal, Fetch: marketfetch.PlanFetch{Concurrency: 8},
		Market:   marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "spot_http", MarketType: "spot"},
		Datasets: []marketfetch.PlanDataset{{DatasetID: "bars_1m", Frequency: "1m", LatenessS: 600, Copies: []marketfetch.PlanCopy{{From: 1000, Hash: "h", Count: 250, Subjects: subjects}}}}}
	require.NoError(t, plan.Validate())
	scheduler.Apply([]stream.PlanItem{{Executor: marketfetch.PlanExecutorStream, Plan: plan}, {Executor: marketfetch.PlanExecutorLocal, Plan: plan}})
	require.Len(t, scheduler.plans, 1)

	t.Setenv("MOOX_SPACE_ID", "")
	scheduled := time.Now().UTC().Truncate(time.Minute)
	response, err := executor.RunPlan(context.Background(), plan, marketfetch.PlanRun{RequestID: "r", FunctionName: FunctionName("crypto") + "-spot", Scheduled: scheduled, Entry: scheduled})
	require.NoError(t, err)
	require.True(t, response.Success, response.Message)
	require.Len(t, observed, 2, "250 个标的切成两次请求")
	require.Len(t, observed[0].Items, marketfetch.MaxPlanItems)
	require.Len(t, observed[1].Items, 50)
	require.True(t, observed[0].RequirePeriodCommit)
	require.Len(t, observed[1].Targets, 50, "目标随条目切开")
}
