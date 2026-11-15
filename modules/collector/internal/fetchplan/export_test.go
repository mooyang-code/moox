package fetchplan

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// 这个文件只在测试时编译，把对账器的测试夹具借给外部测试包 fetchplan_test 用：
// klinecheck 引用 fetchplan，包内测试不能反过来引 klinecheck，要同时用到两边的测试（时间线的新旧对照）只能放在外部测试包里。

// TimelineScenario 是驱动对账器跑到某个时刻的全部采集计划记录（按创建先后，等同 FetchPlanRepository.ListAll 的结果）。
type TimelineScenario struct {
	Name    string
	Records []*store.FetchPlanRecord
}

// timelineReregisterStore 让内存计划库满足重新登记要用的接口。
type timelineReregisterStore struct{ *memPlanStore }

func (s timelineReregisterStore) UpdateAppliedPlan(_ context.Context, id int64, plan string) error {
	s.records[id-1].Plan = plan
	return nil
}

// TimelineScenarios 驱动对账器跑现有的几类场景，在每个有代表性的时刻留一份计划记录：
// 普通变更、退役、数据集退出后重新启用、重新登记、未生效但已登记的计划、统一数据集、交接、生效周期过期重选。
func TimelineScenarios(t *testing.T) []TimelineScenario {
	t.Helper()
	var scenarios []TimelineScenario
	snapshot := func(name string, records []*store.FetchPlanRecord) {
		cloned := make([]*store.FetchPlanRecord, 0, len(records))
		for _, record := range records {
			item := *record
			cloned = append(cloned, &item)
		}
		scenarios = append(scenarios, TimelineScenario{Name: name, Records: cloned})
	}

	// 普通变更：首份计划登记后未生效、生效；标的减少一个后的第二份计划登记后未生效、生效。
	{
		h := newPlanHarness(t, 120)
		r := h.reconciler()
		require.NoError(t, h.reconcile(r))
		require.NotNil(t, h.open())
		snapshot("subject_change/first_plan_registered_not_applied", h.store.records)
		planBaselineSettle(t, h, r)
		snapshot("subject_change/first_plan_applied", h.store.records)
		r.lastVerified = h.now
		h.setSubjects(119)
		require.NoError(t, h.reconcile(r))
		require.NotNil(t, h.open())
		snapshot("subject_change/second_plan_registered_not_applied", h.store.records)
		planBaselineSettle(t, h, r)
		snapshot("subject_change/second_plan_applied", h.store.records)
	}

	// 退役：标的从 120 个减到 40 个。
	{
		h := newPlanHarness(t, 120)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(40)
		planBaselineSettle(t, h, r)
		snapshot("retire_functions", h.store.records)
	}

	// 数据集退出后重新启用：全部标的被排除，去抖后数据集退出计划；排除到期后作为新数据集回到计划。
	{
		h := newIngestHarness(t, modePullSCF, "1m")
		exclusions := &memExclusions{}
		h.r.Exclusions = exclusions
		h.settle(true)
		for _, subject := range h.symbols.subjects {
			exclusions.items = append(exclusions.items, store.SubjectExclusion{SpaceID: "crypto", SubjectID: subject, ExpiresAt: h.now.Add(time.Hour)})
		}
		for i := 0; i < planGoneDebounce && h.open() == nil; i++ {
			require.NoError(t, h.reconcile(h.r))
			h.now = h.now.Add(20 * time.Second)
		}
		require.NotNil(t, h.open())
		h.settle(true)
		require.True(t, h.applied().Datasets["ds_spot_1m|1m"].Gone)
		snapshot("dataset_gone", h.store.records)
		h.now = h.now.Add(time.Hour)
		h.settle(true)
		require.False(t, h.applied().Datasets["ds_spot_1m|1m"].Gone)
		snapshot("dataset_gone_then_back", h.store.records)
	}

	// 重新登记：清空已生效计划里的哈希，下一份计划重新登记全部数据集。
	{
		h := newPlanHarness(t, 60)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		_, err := ClearRegisteredSnapshots(context.Background(), timelineReregisterStore{h.store}, "crypto")
		require.NoError(t, err)
		snapshot("reregister/hashes_cleared", h.store.records)
		require.NoError(t, h.reconcile(r))
		require.NotNil(t, h.open())
		snapshot("reregister/new_plan_registered_not_applied", h.store.records)
		planBaselineSettle(t, h, r)
		snapshot("reregister/new_plan_applied", h.store.records)
	}

	// 统一数据集：现货、永续两条序列写同一个数据集；之后标的减少一个。
	{
		h, r := newPlanBaselineUnified(t)
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(59)
		planBaselineSettle(t, h, r)
		snapshot("unified_dataset", h.store.records)
	}

	// 交接：云函数 → 推送 → 云函数（哈希不变，不重新登记）。
	{
		h := newIngestHarness(t, modePullSCF, "1m", "1h")
		h.settle(true)
		h.r.lastVerified = h.now
		h.modes.set("ds_spot_1m", IngestStreaming, marketfetch.PlanExecutorLocal, false)
		h.modes.set("ds_spot_1h", IngestStreaming, marketfetch.PlanExecutorLocal, false)
		h.settle(true)
		h.r.lastVerified = h.now
		h.modes.set("ds_spot_1m", IngestPull, marketfetch.PlanExecutorSCF, false)
		h.modes.set("ds_spot_1h", IngestPull, marketfetch.PlanExecutorSCF, false)
		h.settle(true)
		snapshot("handoff_scf_stream_scf", h.store.records)
	}

	// 生效周期过期重选：登记结果不明时原样重放；Storage 明确答复过期后重选更晚的周期。
	{
		h := newPlanHarness(t, 60)
		h.registrar.failures = 1
		r := h.reconciler()
		require.Error(t, h.reconcile(r))
		snapshot("stale_period/saved_not_registered", h.store.records)
		h.now = h.now.Add(10 * time.Minute)
		h.registrar.stale = 1
		require.NoError(t, h.reconcile(r))
		snapshot("stale_period/reselected_not_registered", h.store.records)
		planBaselineSettle(t, h, r)
		snapshot("stale_period/applied", h.store.records)
	}
	return scenarios
}
