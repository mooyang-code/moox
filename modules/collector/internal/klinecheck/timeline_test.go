package klinecheck

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// recordedPlans 是内存里按 ID 排列的计划记录，同时满足时间线读取与 fetchplan.ClearRegisteredSnapshots 的改写接口。
// 重新登记时删掉此前写下的全部时间线行：cleared 之前的记录不再贡献时间线行。
type recordedPlans struct {
	records []*store.FetchPlanRecord
	cleared int
}

func (p *recordedPlans) ListBySpace(context.Context, string) ([]store.SeriesTimelineEntry, error) {
	return timelineRows(p.records[p.cleared:]), nil
}

// timeline 读出时间线行并整理成时间线。
func (p *recordedPlans) timeline() Timeline {
	entries, _ := p.ListBySpace(context.Background(), "crypto")
	return BuildTimeline(entries)
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

func (p *recordedPlans) ReregisterAppliedPlan(_ context.Context, id int64, plan string) error {
	for _, record := range p.records {
		if record.ID == id {
			record.Plan = plan
		}
	}
	p.cleared = len(p.records)
	return nil
}

// reregister 按 ReregisterFetchPlan 的真实步骤清空最近一份已生效计划里的数据集 hash。
func reregister(t *testing.T, plans *recordedPlans) {
	t.Helper()
	keys, err := fetchplan.ClearRegisteredSnapshots(context.Background(), plans, "crypto")
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
	timeline := plans.timeline()
	require.Len(t, timeline["ds|1m"], 2)
	require.Equal(t, base.Unix(), timeline["ds|1m"][0].From)

	reregister(t, plans)
	timeline = plans.timeline()
	require.Empty(t, timeline["ds|1m"], "快照清空后此前的计划段作废")
	require.False(t, timeline.Active("ds|1m"))

	next := record(t, 3, store.FetchPlanPending, restart.Unix(), "A-USDT", "B-USDT")
	plans.records = append(plans.records, next)
	timeline = plans.timeline()
	require.Empty(t, timeline["ds|1m"], "新计划的快照还没登记，生效周期未定")

	next.Progress = `{"registered":{"ds|1m":true}}`
	timeline = plans.timeline()
	require.Equal(t, []Segment{{From: restart.Unix(), Subjects: []string{"A-USDT", "B-USDT"}, Symbols: map[string]string{"A-USDT": "AUSDT", "B-USDT": "BUSDT"}, LatenessS: 15}}, timeline["ds|1m"])

	next.Status = store.FetchPlanApplied
	plans.records = append(plans.records, record(t, 4, store.FetchPlanApplied, restart.Add(time.Hour).Unix(), "A-USDT"))
	timeline = plans.timeline()
	require.Equal(t, []Segment{
		{From: restart.Unix(), To: restart.Add(time.Hour).Unix(), Subjects: []string{"A-USDT", "B-USDT"}, Symbols: map[string]string{"A-USDT": "AUSDT", "B-USDT": "BUSDT"}, LatenessS: 15},
		{From: restart.Add(time.Hour).Unix(), Subjects: []string{"A-USDT"}, Symbols: map[string]string{"A-USDT": "AUSDT"}, LatenessS: 15},
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
	timeline := plans.timeline()
	require.Len(t, timeline["retired|1m"], 1)

	reregister(t, plans)
	timeline = plans.timeline()
	require.Empty(t, timeline, "清库前的计划段全部作废，包括已退役的数据集")

	plans.records = append(plans.records,
		recordFor(t, 4, "ds", store.FetchPlanApplied, restart.Unix(), "A-USDT"),
		recordFor(t, 5, "retired", store.FetchPlanApplied, restart.Add(time.Hour).Unix(), "A-USDT"),
	)
	timeline = plans.timeline()
	require.Equal(t, []Segment{{From: restart.Add(time.Hour).Unix(), Subjects: []string{"A-USDT"}, Symbols: map[string]string{"A-USDT": "AUSDT"}, LatenessS: 15}}, timeline["retired|1m"], "重新启用后从新的生效周期开始")
	require.Equal(t, restart.Unix(), timeline["ds|1m"][0].From)
}

// 重新登记之后，检查的时间线起点是新的生效周期：清库前的数据已经没了，新生效周期之前一根都没有，不再当作缺口。
func TestCheckStartsAtTheReregisteredPeriod(t *testing.T) {
	base := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)
	restart := base.Add(55 * time.Hour)
	plans := &recordedPlans{records: []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT", "B-USDT")}}
	reregister(t, plans)
	rows := newCountingRows(map[string]map[int64]bool{})
	rows.listed = map[string]time.Time{"A-USDT": restart, "B-USDT": restart} // 清库前的数据已经没了
	checker := &Checker{Timeline: plans, Rows: rows, Now: func() time.Time { return restart.Add(time.Hour) }}
	ctx := context.Background()
	request := Request{SpaceID: "crypto", From: base, To: restart.Add(20 * time.Minute)}

	idle, err := checker.Check(ctx, request)
	require.NoError(t, err)
	require.Zero(t, idle.Expected, "新快照登记之前没有可检查的范围")

	plans.records = append(plans.records, record(t, 2, store.FetchPlanApplied, restart.Unix(), "A-USDT", "B-USDT"))
	result, err := checker.Check(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 2*20, result.Expected, "只算新生效周期之后的 20 根")
	require.Empty(t, result.Gaps, "清库前计划覆盖的时间不再报缺口")
}

// 重新登记之后：新快照登记之前自动补采跳过该数据集；登记后自动补采从新生效周期扫起（水位已随重新登记清空），
// 不会回到清库前的计划起点。
func TestScansFollowTheReregisteredPeriod(t *testing.T) {
	base := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)
	restart := base.Add(55 * time.Hour)
	plans := &recordedPlans{records: []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT")}}
	reregister(t, plans)
	rows := &fakeRows{missing: map[string]map[int64]bool{}}
	markMissing(rows, "A-USDT", base, restart)
	now := restart.Add(3*time.Hour + 3*time.Minute) // 落后超过一个自动补采窗口，且已过槽内偏移
	repo := openBackfillStore(t)
	backfiller := &Backfiller{Checker: &Checker{Timeline: plans, Rows: rows, Now: func() time.Time { return now }}, Store: repo, Routes: fakeRoutes{}, Now: func() time.Time { return now }}
	ctx := context.Background()

	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	mark, err := repo.Watermark(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, mark.IsZero(), "新快照登记之前不补采")

	plans.records = append(plans.records, record(t, 2, store.FetchPlanApplied, restart.Unix(), "A-USDT"))
	now = now.Add(autoScanInterval)
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	jobs, err := repo.ActiveJobs(ctx, "crypto")
	require.NoError(t, err)
	require.Empty(t, jobs, "清库前的空区间不当作缺口")
	mark, err = repo.Watermark(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, mark.Equal(restart.Add(autoScanWindow)), "从新生效周期起追赶一个窗口，实际水位 %s", mark)
}

type countingIncrementalTimeline struct {
	repo       *store.SeriesTimelineRepository
	statsCalls atomic.Int32
	fullCalls  atomic.Int32
	afterCalls atomic.Int32
}

func (c *countingIncrementalTimeline) ListBySpace(ctx context.Context, spaceID string) ([]store.SeriesTimelineEntry, error) {
	c.fullCalls.Add(1)
	return c.repo.ListBySpace(ctx, spaceID)
}

func (c *countingIncrementalTimeline) TimelineStats(ctx context.Context, spaceID string) (int64, int64, int64, error) {
	c.statsCalls.Add(1)
	return c.repo.TimelineStats(ctx, spaceID)
}

func (c *countingIncrementalTimeline) ListBySpaceWithBounds(ctx context.Context, spaceID string) ([]store.SeriesTimelineEntry, int64, int64, error) {
	c.fullCalls.Add(1)
	return c.repo.ListBySpaceWithBounds(ctx, spaceID)
}

func (c *countingIncrementalTimeline) ListBySpaceAfter(ctx context.Context, spaceID string, afterID int64) ([]store.SeriesTimelineEntry, int64, error) {
	c.afterCalls.Add(1)
	return c.repo.ListBySpaceAfter(ctx, spaceID, afterID)
}

func makeTimelineEntry(space, dataset string, from int64, symbols map[string]string) store.SeriesTimelineEntry {
	entry := store.SeriesTimelineEntry{SpaceID: space, DatasetID: dataset, Frequency: "1m", EffectiveFrom: from, LatenessS: 15, Symbols: symbols}
	for subject := range symbols {
		entry.Series = append(entry.Series, seriesset.Series{SubjectID: subject})
	}
	if len(entry.Series) > 0 {
		entry.SeriesHash = "hash"
	}
	return entry
}

// Timeline 内存缓存按 (count, min_id, max_id) 命中、按 c_id 增量更新，在同生效点覆盖删旧行、清库重登记与并发读写下均保持准确无竞态。
func TestTimelineCacheHitsIncrementallyUpdatesAndInvalidatesOnReset(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })

	plans := db.FetchPlans()
	src := &countingIncrementalTimeline{repo: db.SeriesTimeline()}
	var cache timelineCache

	// 1. 空表直接命中空缓存，不触发全量查询。
	empty, err := cache.Load(ctx, src, "crypto")
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Zero(t, src.fullCalls.Load())

	// 2. 写入首行后首次冷启动全量加载，之后重复读取全部命中内存缓存（零 JSON 解码）。
	rec, err := plans.Create(ctx, "crypto", `{"v":1}`, "{}", makeTimelineEntry("crypto", "ds", 100, map[string]string{"BTC-USDT": "BTCUSDT"}))
	require.NoError(t, err)
	tl, err := cache.Load(ctx, src, "crypto")
	require.NoError(t, err)
	require.Equal(t, int32(1), src.fullCalls.Load())
	sym, ok := tl.SymbolAt("ds|1m", "BTC-USDT", time.Unix(100, 0).UTC())
	require.True(t, ok)
	require.Equal(t, "BTCUSDT", sym)

	for range 5 {
		again, loadErr := cache.Load(ctx, src, "crypto")
		require.NoError(t, loadErr)
		require.Equal(t, tl, again)
	}
	require.Equal(t, int32(1), src.fullCalls.Load(), "未变化时不得重复全量加载")
	require.Zero(t, src.afterCalls.Load(), "未变化时不得触发增量查询")

	// 3. 纯追加新行：只走 ListBySpaceAfter 增量解码，不重扫全表。
	require.NoError(t, plans.Update(ctx, rec.ID, store.FetchPlanApplying, "{}", "", makeTimelineEntry("crypto", "ds", 200, map[string]string{"BTC-USDT": "BTCNEW"})))
	tl, err = cache.Load(ctx, src, "crypto")
	require.NoError(t, err)
	require.Equal(t, int32(1), src.fullCalls.Load(), "纯追加应走增量路径，不走全量")
	require.Equal(t, int32(1), src.afterCalls.Load())
	require.Len(t, tl["ds|1m"], 2)
	sym, ok = tl.SymbolAt("ds|1m", "BTC-USDT", time.Unix(150, 0).UTC())
	require.True(t, ok)
	require.Equal(t, "BTCUSDT", sym)
	sym, ok = tl.SymbolAt("ds|1m", "BTC-USDT", time.Unix(200, 0).UTC())
	require.True(t, ok)
	require.Equal(t, "BTCNEW", sym)

	// 4. 同生效点覆盖（先删旧行再插更大 c_id 的新行）：自动识别旧行删除并全量重建，不留零长度幽灵段。
	require.NoError(t, plans.Update(ctx, rec.ID, store.FetchPlanApplied, "{}", "", makeTimelineEntry("crypto", "ds", 200, map[string]string{"BTC-USDT": "BTCV3"})))
	tl, err = cache.Load(ctx, src, "crypto")
	require.NoError(t, err)
	require.Equal(t, int32(2), src.fullCalls.Load(), "同生效点覆盖删旧行必须自动触发全量重建")
	require.Len(t, tl["ds|1m"], 2, "不残留零长度幽灵段")
	sym, ok = tl.SymbolAt("ds|1m", "BTC-USDT", time.Unix(200, 0).UTC())
	require.True(t, ok)
	require.Equal(t, "BTCV3", sym)

	// 5. 重新登记清库（删光全空间行）：缓存自动清空；随后重新写入更大 c_id 的新行也不残留清库前的旧段。
	require.NoError(t, plans.ReregisterAppliedPlan(ctx, rec.ID, `{"v":"reset"}`))
	tl, err = cache.Load(ctx, src, "crypto")
	require.NoError(t, err)
	require.Empty(t, tl, "清库后时间线必须为空")

	rec2, err := plans.Create(ctx, "crypto", `{"v":2}`, "{}", makeTimelineEntry("crypto", "ds", 500, map[string]string{"ETH-USDT": "ETHUSDT"}))
	require.NoError(t, err)
	require.NoError(t, plans.Update(ctx, rec2.ID, store.FetchPlanApplied, "{}", ""))
	tl, err = cache.Load(ctx, src, "crypto")
	require.NoError(t, err)
	require.Len(t, tl["ds|1m"], 1)
	require.Equal(t, int64(500), tl["ds|1m"][0].From)
	_, ok = tl.SymbolAt("ds|1m", "BTC-USDT", time.Unix(500, 0).UTC())
	require.False(t, ok, "清库前的旧标的绝不能残留")

	// 6. 并发读取 + 并发追加 + Checker.Check 克隆修改（用于 -race 竞态验证）。
	checker := &Checker{Timeline: src, Rows: &fakeRows{missing: map[string]map[int64]bool{"ETH-USDT": {}}}, Now: func() time.Time { return time.Unix(1000, 0).UTC() }}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				got, loadErr := cache.Load(ctx, src, "crypto")
				require.NoError(t, loadErr)
				_, _ = got.SymbolAt("ds|1m", "ETH-USDT", time.Unix(600, 0).UTC())
				_, checkErr := checker.Check(ctx, Request{SpaceID: "crypto", From: time.Unix(400, 0).UTC(), To: time.Unix(600, 0).UTC()})
				require.NoError(t, checkErr)
			}
		}()
	}
	rec3, err := plans.Create(ctx, "crypto", `{"v":3}`, "{}", makeTimelineEntry("crypto", "ds", 700, map[string]string{"ETH-USDT": "ETHV2"}))
	require.NoError(t, err)
	require.NoError(t, plans.Update(ctx, rec3.ID, store.FetchPlanApplied, "{}", ""))
	wg.Wait()

	finalTL, err := cache.Load(ctx, src, "crypto")
	require.NoError(t, err)
	require.Len(t, finalTL["ds|1m"], 2)
}
