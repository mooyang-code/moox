package klinecheck

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// recordedPlans 是内存里按 ID 排列的计划记录，同时满足时间线读取与 marketfetch.ClearRegisteredSnapshots 的改写接口。
type recordedPlans struct{ records []*store.FetchPlanRecord }

func (p *recordedPlans) ListAll(context.Context, string) ([]*store.FetchPlanRecord, error) {
	return p.records, nil
}

func (p *recordedPlans) Open(context.Context, string) (*store.FetchPlanRecord, error) {
	for _, record := range p.records {
		if record.Status != store.FetchPlanApplied {
			return record, nil
		}
	}
	return nil, nil
}

func (p *recordedPlans) LatestApplied(context.Context, string) (*store.FetchPlanRecord, error) {
	for i := len(p.records) - 1; i >= 0; i-- {
		if p.records[i].Status == store.FetchPlanApplied {
			return p.records[i], nil
		}
	}
	return nil, nil
}

func (p *recordedPlans) UpdateAppliedPlan(_ context.Context, id int64, plan string) error {
	for _, record := range p.records {
		if record.ID == id {
			record.Plan = plan
		}
	}
	return nil
}

// reregister 按 ReregisterFetchPlan 的真实步骤清空最近一份已生效计划里的数据集 hash。
func reregister(t *testing.T, plans *recordedPlans) {
	t.Helper()
	keys, err := marketfetch.ClearRegisteredSnapshots(context.Background(), plans, "crypto")
	require.NoError(t, err)
	require.NotEmpty(t, keys)
}

// Storage 清空、重新登记之后，时间线从重新登记的新生效周期开始，此前的计划段作废；新快照登记之前数据集没有计划段，
// 登记之后照常按计划变更延续。
func TestTimelineRestartsAtTheReregisteredPeriod(t *testing.T) {
	base := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)
	restart := base.Add(55 * time.Hour) // 清库后重新登记选出的生效周期
	plans := &recordedPlans{records: []*store.FetchPlanRecord{
		record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT", "B-USDT"),
		record(t, 2, store.FetchPlanApplied, base.Add(time.Hour).Unix(), "A-USDT", "B-USDT", "C-USDT"),
	}}
	timeline, err := BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Len(t, timeline["ds|1m"], 2)
	require.Equal(t, base.Unix(), timeline["ds|1m"][0].From)

	reregister(t, plans)
	timeline, err = BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Empty(t, timeline["ds|1m"], "快照清空后此前的计划段作废")
	require.False(t, timeline.Active("ds|1m"))

	next := record(t, 3, store.FetchPlanPending, restart.Unix(), "A-USDT", "B-USDT")
	plans.records = append(plans.records, next)
	timeline, err = BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Empty(t, timeline["ds|1m"], "新计划的快照还没登记，生效周期未定")

	next.Progress = `{"registered":{"ds|1m":true}}`
	timeline, err = BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Equal(t, []Segment{{From: restart.Unix(), Subjects: []string{"A-USDT", "B-USDT"}, LatenessS: 15}}, timeline["ds|1m"])

	next.Status = store.FetchPlanApplied
	plans.records = append(plans.records, record(t, 4, store.FetchPlanApplied, restart.Add(time.Hour).Unix(), "A-USDT"))
	timeline, err = BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Equal(t, []Segment{
		{From: restart.Unix(), To: restart.Add(time.Hour).Unix(), Subjects: []string{"A-USDT", "B-USDT"}, LatenessS: 15},
		{From: restart.Add(time.Hour).Unix(), Subjects: []string{"A-USDT"}, LatenessS: 15},
	}, timeline["ds|1m"], "重新登记之后的计划变更照常延续时间线")
}

// 清库时已退役的数据集不在最新的已生效计划里，hash 不会被清空，但它的旧计划段同样作废：以后重新启用时
// 时间线从新的生效周期开始，自动补采（水位已随重新登记清空）不会回补清库前的数据。
func TestTimelineResetCoversRetiredDatasets(t *testing.T) {
	base := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)
	restart := base.Add(55 * time.Hour)
	plans := &recordedPlans{records: []*store.FetchPlanRecord{
		recordFor(t, 1, "retired", store.FetchPlanApplied, base.Unix(), "A-USDT"),
		goneRecord(t, 2, "retired", base.Add(time.Hour).Unix()),
		recordFor(t, 3, "ds", store.FetchPlanApplied, base.Add(2*time.Hour).Unix(), "A-USDT"),
	}}
	timeline, err := BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Len(t, timeline["retired|1m"], 1)

	reregister(t, plans)
	timeline, err = BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Empty(t, timeline, "清库前的计划段全部作废，包括已退役的数据集")

	plans.records = append(plans.records,
		recordFor(t, 4, "ds", store.FetchPlanApplied, restart.Unix(), "A-USDT"),
		recordFor(t, 5, "retired", store.FetchPlanApplied, restart.Add(time.Hour).Unix(), "A-USDT"),
	)
	timeline, err = BuildTimeline(plans.records)
	require.NoError(t, err)
	require.Equal(t, []Segment{{From: restart.Add(time.Hour).Unix(), Subjects: []string{"A-USDT"}, LatenessS: 15}}, timeline["retired|1m"], "重新启用后从新的生效周期开始")
	require.Equal(t, restart.Unix(), timeline["ds|1m"][0].From)
}

// 重新登记之后，检查与历史扫描的起点都是新的生效周期：清库前计划覆盖的时间不再当作缺口，
// 历史扫描读回新生效周期之前的 500 根。
func TestCheckAndHistoryStartAtTheReregisteredPeriod(t *testing.T) {
	base := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)
	restart := base.Add(55 * time.Hour)
	plans := &recordedPlans{records: []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT", "B-USDT")}}
	reregister(t, plans)
	rows := &fakeRows{missing: map[string]map[int64]bool{}}
	markMissing(rows, "A-USDT", base, restart) // 清库前的数据已经没了
	markMissing(rows, "B-USDT", base, restart)
	checker := &Checker{Plans: plans, Rows: rows, Now: func() time.Time { return restart.Add(time.Hour) }}
	ctx := context.Background()
	request := Request{SpaceID: "crypto", From: base, To: restart.Add(20 * time.Minute)}

	idle, err := checker.Check(ctx, request)
	require.NoError(t, err)
	require.Zero(t, idle.Expected, "新快照登记之前没有可检查的范围")
	history, err := checker.CheckHistory(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.Nil(t, history)

	plans.records = append(plans.records, record(t, 2, store.FetchPlanApplied, restart.Unix(), "A-USDT", "B-USDT"))
	result, err := checker.Check(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 2*20, result.Expected, "只算新生效周期之后的 20 根")
	require.Empty(t, result.Gaps, "清库前计划覆盖的时间不再报缺口")

	history, err = checker.CheckHistory(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.Equal(t, restart, history.To)
	require.Equal(t, restart.Add(-historyScanBars*time.Minute), history.From)
	require.Equal(t, 2, history.Subjects)
	require.Empty(t, history.Gaps)
}

// 重新登记之后：新快照登记之前自动补采与历史扫描都跳过该数据集；登记后自动补采从新生效周期扫起（水位已随重新登记清空），
// 历史扫描读回新生效周期之前的一段，都不会回到清库前的计划起点。
func TestScansFollowTheReregisteredPeriod(t *testing.T) {
	base := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)
	restart := base.Add(55 * time.Hour)
	plans := &recordedPlans{records: []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT")}}
	reregister(t, plans)
	rows := &fakeRows{missing: map[string]map[int64]bool{}}
	markMissing(rows, "A-USDT", base, restart)
	now := restart.Add(3*time.Hour + 3*time.Minute) // 落后超过一个自动补采窗口，且已过槽内偏移
	repo := openBackfillStore(t)
	backfiller := &Backfiller{Checker: &Checker{Plans: plans, Rows: rows, Now: func() time.Time { return now }}, Store: repo, Plans: plans, Now: func() time.Time { return now }}
	ctx := context.Background()

	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	mark, err := repo.Watermark(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, mark.IsZero(), "新快照登记之前不补采")
	scanned, err := backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Nil(t, scanned, "新快照登记之前不扫历史")

	plans.records = append(plans.records, record(t, 2, store.FetchPlanApplied, restart.Unix(), "A-USDT"))
	now = now.Add(autoScanInterval)
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	jobs, err := repo.ActiveJobs(ctx, "crypto")
	require.NoError(t, err)
	require.Empty(t, jobs, "清库前的空区间不当作缺口")
	mark, err = repo.Watermark(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, mark.Equal(restart.Add(autoScanWindow)), "从新生效周期起追赶一个窗口，实际水位 %s", mark)

	scanned, err = backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, restart, scanned.To)
	scans, err := repo.HistoryScans(ctx, "crypto")
	require.NoError(t, err)
	require.True(t, scans["ds|1m"].To.Equal(restart), "库里记下的扫描范围止于新生效周期")
}
