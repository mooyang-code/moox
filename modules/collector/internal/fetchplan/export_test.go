package fetchplan

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// 这个文件只在测试时编译，把对账器的测试夹具借给外部测试包 fetchplan_test 用：
// klinecheck 引用 fetchplan，包内测试不能反过来引 klinecheck，要同时用到两边的测试（时间线的新旧对照）只能放在外部测试包里。

// TimelineScenario 是驱动对账器跑到某个时刻的全部采集计划记录（按创建先后，等同 FetchPlanRepository.ListAll 的结果），
// 以及同一时刻标的时间线表里的行（从真实的 SQLite 表读出，等同 SeriesTimelineRepository.ListBySpace 的结果）。
type TimelineScenario struct {
	Name     string
	Records  []*store.FetchPlanRecord
	Timeline []store.SeriesTimelineEntry
}

// timelineReregisterStore 让内存计划库满足重新登记要用的接口：改写已生效计划，并删掉该空间的全部时间线行。
type timelineReregisterStore struct{ *memPlanStore }

func (s timelineReregisterStore) ReregisterAppliedPlan(ctx context.Context, id int64, plan string) error {
	if err := s.shadow.ReregisterAppliedPlan(ctx, id, plan); err != nil {
		return err
	}
	s.records[id-1].Plan = plan
	return nil
}

// withSeriesTimeline 给内存计划库挂上一个真实的计划库（临时目录里的新库）：对账器的每次写入经生产路径同样落进去，
// 返回这个库的时间线仓库供读取。
func withSeriesTimeline(t *testing.T, plans *memPlanStore) *store.SeriesTimelineRepository {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	plans.shadow = db.FetchPlans()
	return db.SeriesTimeline()
}

// TimelineScenarios 驱动对账器跑现有的几类场景，在每个有代表性的时刻留一份计划记录：
// 普通变更、退役、数据集退出后重新启用、重新登记、未生效但已登记的计划、统一数据集、交接、生效周期过期重选。
func TimelineScenarios(t *testing.T) []TimelineScenario {
	t.Helper()
	var scenarios []TimelineScenario
	// snapshot 留下计划记录的拷贝与时间线表此刻的行。传入的 records 必须是挂了时间线表的那个内存计划库的记录。
	var timeline *store.SeriesTimelineRepository
	attach := func(plans *memPlanStore) { timeline = withSeriesTimeline(t, plans) }
	snapshot := func(name string, records []*store.FetchPlanRecord) {
		cloned := make([]*store.FetchPlanRecord, 0, len(records))
		for _, record := range records {
			item := *record
			cloned = append(cloned, &item)
		}
		entries, err := timeline.ListBySpace(context.Background(), "crypto")
		require.NoError(t, err, name)
		scenarios = append(scenarios, TimelineScenario{Name: name, Records: cloned, Timeline: entries})
	}

	// 普通变更：首份计划登记后未生效、生效；标的减少一个后的第二份计划登记后未生效、生效。
	{
		h := newPlanHarness(t, 120)
		attach(h.store)
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
		attach(h.store)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(40)
		planBaselineSettle(t, h, r)
		snapshot("retire_functions", h.store.records)
	}

	// 数据集退出后重新启用：全部标的被排除，去抖后数据集退出计划；排除到期后作为新数据集回到计划。
	{
		h := newIngestHarness(t, ingestSCF, "1m")
		attach(h.store)
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
		attach(h.store)
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
		attach(h.store)
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(59)
		planBaselineSettle(t, h, r)
		snapshot("unified_dataset", h.store.records)
	}

	// 交接：云函数 → 推送 → 云函数（哈希不变，不重新登记）。
	{
		h := newIngestHarness(t, ingestSCF, "1m", "1h")
		attach(h.store)
		h.settle(true)
		h.r.lastVerified = h.now
		h.setIngest("ds_spot_1m", ingestStream)
		h.setIngest("ds_spot_1h", ingestStream)
		h.settle(true)
		h.r.lastVerified = h.now
		h.setIngest("ds_spot_1m", ingestSCF)
		h.setIngest("ds_spot_1h", ingestSCF)
		h.settle(true)
		snapshot("handoff_scf_stream_scf", h.store.records)
	}

	// 生效周期过期重选：登记结果不明时原样重放；Storage 明确答复过期后重选更晚的周期。
	{
		h := newPlanHarness(t, 60)
		attach(h.store)
		h.registrar.failures = 1
		r := h.reconciler()
		require.Error(t, h.reconcile(r))
		firstP := h.content(h.open()).Datasets["ds_spot|1m"].P
		snapshot("stale_period/saved_not_registered", h.store.records)
		h.now = h.now.Add(10 * time.Minute)
		h.registrar.stale = 1
		require.NoError(t, h.reconcile(r))
		snapshot("stale_period/reselected_not_registered", h.store.records)
		planBaselineSettle(t, h, r)
		snapshot("stale_period/applied", h.store.records)
		applied, err := h.store.LatestApplied(context.Background(), "crypto")
		require.NoError(t, err)
		finalP := h.content(applied).Datasets["ds_spot|1m"].P
		require.Greater(t, finalP, firstP, "过期后重选了更晚的生效周期")
		entries, err := timeline.ListBySpace(context.Background(), "crypto")
		require.NoError(t, err)
		require.Len(t, entries, 1, "第一次选的 P 没有登记成功，时间线里不留它的行")
		require.Equal(t, finalP, entries[0].EffectiveFrom)
	}

	// 标的增加：60 个增到 61 个。
	{
		h := newPlanHarness(t, 60)
		attach(h.store)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(61)
		planBaselineSettle(t, h, r)
		snapshot("subject_increase", h.store.records)
	}

	// 空间暂停与恢复：计划已登记、未生效时暂停（作废下发确认，保留登记与 P），恢复后推进到生效。时间线行不增不减。
	{
		h := newPlanHarness(t, 60)
		attach(h.store)
		r := h.reconciler()
		require.NoError(t, h.reconcile(r))
		require.NotNil(t, h.open())
		done, err := r.DisableAll(context.Background())
		require.NoError(t, err)
		require.True(t, done)
		snapshot("pause_resume/paused_registered_not_applied", h.store.records)
		done, err = r.EnableAll(context.Background())
		require.NoError(t, err)
		require.True(t, done)
		snapshot("pause_resume/resumed_not_applied", h.store.records)
		planBaselineSettle(t, h, r)
		snapshot("pause_resume/applied", h.store.records)
	}

	// 计划未生效时标的又变了：一个空间只有一份未生效计划，新的变更不会建新计划去取代它，
	// 等它生效之后才由下一份计划接手（store 层用唯一索引保证，Create 返回 ErrFetchPlanOpen）。
	{
		h := newPlanHarness(t, 120)
		attach(h.store)
		r := h.reconciler()
		require.NoError(t, h.reconcile(r))
		require.NotNil(t, h.open())
		h.setSubjects(119)
		for i := 0; i < 3; i++ {
			h.now = h.now.Add(20 * time.Second)
			require.NoError(t, h.reconcile(r))
		}
		require.NotNil(t, h.open(), "首份计划仍未生效")
		require.Len(t, h.store.records, 1, "未生效的计划没有被新计划取代")
		snapshot("change_while_open/first_still_open", h.store.records)
		planBaselineSettle(t, h, r)
		require.Len(t, h.store.records, 1)
		r.lastVerified = h.now
		planBaselineSettle(t, h, r)
		require.Len(t, h.store.records, 2, "首份计划生效后才建下一份")
		snapshot("change_while_open/both_applied", h.store.records)
	}
	return scenarios
}
