package klinecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// fakePlans 是内存里的计划记录：检查按 timelineRows 从它得到时间线行。
type fakePlans struct{ records []*store.FetchPlanRecord }

// fakeRoutes 是补采的路由夹具：除 gone 里的数据集外都有采集任务，来源是币安现货。
type fakeRoutes struct{ gone map[string]bool }

func (f fakeRoutes) BackfillRoute(_ context.Context, _, datasetID, frequency string) (*marketfetch.Plan, bool, error) {
	if f.gone[datasetID] {
		return nil, false, nil
	}
	return &marketfetch.Plan{V: 1, Fetch: marketfetch.PlanFetch{Concurrency: 1},
		Market:   marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "binance", MarketType: "spot"},
		Datasets: []marketfetch.PlanDataset{{DatasetID: datasetID, Frequency: frequency}}}, true, nil
}

func (f fakePlans) ListBySpace(context.Context, string) ([]store.SeriesTimelineEntry, error) {
	return timelineRows(f.records), nil
}

// timelineRows 按对账器写时间线的规则，把测试里构造的计划记录换成时间线行（按记录先后）：
// 本计划要写行（Timeline）的数据集，不用登记的在计划建立时写，要登记的在 Storage 确认登记后（进度里已登记，或计划已生效）写；
// 头份是停止份的写空序列。这只是检查类测试的夹具，真实写入路径由 fetchplan 的对账测试驱动。
func timelineRows(records []*store.FetchPlanRecord) []store.SeriesTimelineEntry {
	var entries []store.SeriesTimelineEntry
	for _, record := range records {
		content, err := fetchplan.DecodeFetchPlanContent(record.Plan)
		if err != nil {
			panic(err)
		}
		progress, err := fetchplan.DecodeFetchPlanProgress(record.Progress)
		if err != nil {
			panic(err)
		}
		keys := make([]string, 0, len(content.Datasets))
		for key := range content.Datasets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			dataset := content.Datasets[key]
			if !dataset.Timeline || dataset.P <= 0 || (dataset.Register && record.Status != store.FetchPlanApplied && !progress.Registered[key]) {
				continue
			}
			entry := store.SeriesTimelineEntry{SpaceID: record.SpaceID, DatasetID: dataset.DatasetID, Frequency: dataset.Frequency, EffectiveFrom: dataset.P}
			if head := dataset.Head(); head != nil && head.Stop {
				entries = append(entries, entry)
				continue
			}
			symbols := dataset.Symbols
			if symbols == nil {
				// 手写的计划夹具没有符号：按函数计划的默认规则（去掉连字符）补上。
				symbols = map[string]string{}
				for _, series := range dataset.Series {
					symbols[series.SubjectID] = strings.ReplaceAll(series.SubjectID, "-", "")
				}
			}
			entry.SeriesHash, entry.Series, entry.Symbols, entry.LatenessS, entry.OutputFields = dataset.Hash, dataset.Series, symbols, dataset.LatenessS, dataset.OutputFields
			entries = append(entries, entry)
		}
	}
	return entries
}

// appliedTimeline 返回一份已生效计划（JSON 内容）写下的时间线行，给直接往库里写计划记录的测试补上时间线。
func appliedTimeline(spaceID, plan string) []store.SeriesTimelineEntry {
	return timelineRows([]*store.FetchPlanRecord{{SpaceID: spaceID, Status: store.FetchPlanApplied, Plan: plan, Progress: "{}"}})
}

// fakeRows 按“标的 → 缺失的开始时间”返回行；深度检查时缺 open 字段的时间点单独登记。
// listed 非空时，标的在这个时间之前一根数据都没有（上市或开始有历史的时间）。
type fakeRows struct {
	mu       sync.Mutex
	from, to time.Time
	missing  map[string]map[int64]bool
	noOpen   map[string]map[int64]bool
	listed   map[string]time.Time
	reads    int
}

// has 报告标的在 t 这一根有没有行。
func (f *fakeRows) has(subject string, t time.Time) bool {
	if listed, ok := f.listed[subject]; ok && t.Before(listed) {
		return false
	}
	return !f.missing[subject][t.Unix()]
}

func (f *fakeRows) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if req.GetPage().GetPage() != 0 || req.GetPage().GetSize() > 2000 {
		panic("区间读取只能用游标分页，且单页不超过 2000 行")
	}
	start, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetStartTime())
	end, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetEndTime())
	rsp := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, PageResult: &storagepb.PageResult{}}
	for _, selector := range req.GetSelectors() {
		for t := start; t.Before(end); t = t.Add(time.Minute) {
			if !f.has(selector.GetSubjectId(), t) {
				continue
			}
			row := &storagepb.Row{Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: selector.GetSubjectId(), DataTime: t.Format(time.RFC3339)}}}}
			for _, column := range req.GetColumnNames() {
				if column == "open" && f.noOpen[selector.GetSubjectId()][t.Unix()] {
					continue
				}
				row.Fields = append(row.Fields, &storagepb.FieldValue{FieldId: column, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1}}})
			}
			rsp.Rows = append(rsp.Rows, row)
		}
	}
	return rsp, nil
}

func record(t *testing.T, id int64, status string, p int64, subjects ...string) *store.FetchPlanRecord {
	t.Helper()
	return recordFor(t, id, "ds", status, p, subjects...)
}

// recordFor 构造只含一个“数据集|1m”变更的计划记录。hash 和真实计划一样非空。
func recordFor(t *testing.T, id int64, dataset, status string, p int64, subjects ...string) *store.FetchPlanRecord {
	t.Helper()
	content := &fetchplan.FetchPlanContent{Datasets: map[string]*fetchplan.PlanDataset{}, Functions: map[string]*fetchplan.PlanFunctionChange{}}
	change := &fetchplan.PlanDataset{Action: fetchplan.PlanActionAppend, Register: true, Timeline: true, P: p}
	change.DatasetID, change.Frequency, change.LatenessS, change.Hash = dataset, "1m", 15, "hash:"+strings.Join(subjects, ",")
	for _, subject := range subjects {
		change.Series = append(change.Series, seriesOf(subject))
	}
	change.Count = uint32(len(subjects))
	change.Copies = []fetchplan.DatasetCopy{{From: p, Hash: change.Hash, Count: change.Count, LatenessS: change.LatenessS}}
	content.Datasets[dataset+"|1m"] = change
	return &store.FetchPlanRecord{ID: id, SpaceID: "crypto", Status: status, Plan: mustJSON(content), Progress: "{}"}
}

func TestCheckFindsMergedGapsAcrossSubjectChanges(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	records := []*store.FetchPlanRecord{
		record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT", "ETH-USDT"),
		record(t, 2, store.FetchPlanApplied, base.Add(10*time.Minute).Unix(), "BTC-USDT"),
	}
	rows := &fakeRows{missing: map[string]map[int64]bool{"BTC-USDT": {}, "ETH-USDT": {}}}
	for _, minute := range []int{3, 4, 5, 12} {
		rows.missing["BTC-USDT"][base.Add(time.Duration(minute)*time.Minute).Unix()] = true
	}
	checker := &Checker{Timeline: fakePlans{records}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}
	result, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(20 * time.Minute)})
	require.NoError(t, err)
	// BTC 在 0～20 分钟都应有 20 根；ETH 只在 0～10 分钟应有 10 根（之后被移出标的集合）。
	require.Equal(t, 30, result.Expected)
	require.Equal(t, 4, result.Missing)
	require.Len(t, result.Gaps, 2)
	require.Equal(t, Gap{DatasetID: "ds", Frequency: "1m", SubjectID: "BTC-USDT", From: base.Add(3 * time.Minute), To: base.Add(6 * time.Minute), Missing: 3}, result.Gaps[0])
	require.Equal(t, base.Add(12*time.Minute), result.Gaps[1].From)
	require.Equal(t, 0, len(result.Gaps)-2, "removed subjects are not reported after their effective period")

	one, err := checker.Check(context.Background(), Request{SpaceID: "crypto", SubjectID: "eth-usdt", From: base, To: base.Add(20 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, 10, one.Expected)
}

// 范围可以早于时间线起点（最早的计划生效点），更早的时间按最早一段的标的计算；早于起点的部分每个标的从自己的第一根起算：
// 范围之前已有数据的标的，范围开头的缺口照常报；之前没有数据的（新上市），第一根之前不报，一根都没有的整段不报。
// 时间线之内不裁剪；强制重拉不读数据，也不裁剪。
func TestCheckBeforeTheTimelineCountsFromEachSubjectsFirstBar(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	from, to := base.Add(-10*time.Minute), base.Add(5*time.Minute)
	subjects := []string{"OLD-USDT", "NEW-USDT", "NONE-USDT", "LATE-USDT"}
	rows := newCountingRows(map[string]map[int64]bool{})
	markMissingIn(rows.missing, "OLD-USDT", from, from.Add(2*time.Minute))                   // 范围开头的真实缺口：更早有数据
	markMissingIn(rows.missing, "LATE-USDT", base.Add(time.Minute), base.Add(2*time.Minute)) // 时间线之内的缺口
	rows.listed = map[string]time.Time{
		"NEW-USDT":  base.Add(-5 * time.Minute), // 范围中间才开始有数据
		"NONE-USDT": base,                       // 时间线起点之前一根都没有
		"LATE-USDT": base.Add(3 * time.Minute),  // 时间线之内也晚到：整段缺失照报
	}
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), subjects...)}
	checker := &Checker{Timeline: fakePlans{records}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}

	result, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: from, To: to})
	require.NoError(t, err)
	gaps := map[string][]Gap{}
	for _, gap := range result.Gaps {
		gaps[gap.SubjectID] = append(gaps[gap.SubjectID], gap)
	}
	require.Equal(t, []Gap{{DatasetID: "ds", Frequency: "1m", SubjectID: "OLD-USDT", From: from, To: from.Add(2 * time.Minute), Missing: 2}}, gaps["OLD-USDT"], "更早有数据，范围开头的缺口照报")
	require.Empty(t, gaps["NEW-USDT"], "第一根之前不报")
	require.Empty(t, gaps["NONE-USDT"], "时间线起点之前一根都没有的不报")
	require.Equal(t, []Gap{{DatasetID: "ds", Frequency: "1m", SubjectID: "LATE-USDT", From: base, To: base.Add(3 * time.Minute), Missing: 3}}, gaps["LATE-USDT"], "时间线之内不裁剪")
	require.Equal(t, 4*15-5-10-10, result.Expected, "裁掉的 K 线不算应有")

	// 补采的复查用同一口径：历史中段的分块不会因为范围开头缺失被当成“尚无历史”。
	middle, err := checker.Check(context.Background(), Request{SpaceID: "crypto", SubjectID: "OLD-USDT", From: from, To: from.Add(time.Minute)})
	require.NoError(t, err)
	require.Equal(t, 1, middle.Missing)

	force, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: from, To: to, AssumeMissing: true})
	require.NoError(t, err)
	require.Equal(t, 4*15, force.Expected, "强制重拉覆盖整个范围")
	require.Equal(t, from, force.Gaps[0].From, "缺口从范围起点开始，不被时间线起点截断")
	require.Equal(t, 15, force.Gaps[0].Missing)

	// 历史补齐（FullHistory）不裁剪：新数据集在时间线起点之前一根都没有，整段都是要补的缺口。
	seed, err := checker.Check(context.Background(), Request{SpaceID: "crypto", SubjectID: "NONE-USDT", From: from, To: base, FullHistory: true})
	require.NoError(t, err)
	require.Equal(t, 10, seed.Missing, "补齐按整个范围找缺口")

	// 没有计数能力时不裁剪：宁可多报。
	plain := &Checker{Timeline: fakePlans{records}, Rows: rows.fakeRows, Now: func() time.Time { return base.Add(time.Hour) }}
	untrimmed, err := plain.Check(context.Background(), Request{SpaceID: "crypto", SubjectID: "NONE-USDT", From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, 10, untrimmed.Missing)
}

func TestCheckReadsManySubjectsInBatches(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	subjects := make([]string, 0, subjectsPerRead+20)
	for i := 0; i < subjectsPerRead+20; i++ {
		subjects = append(subjects, fmt.Sprintf("S%03d-USDT", i))
	}
	rows := &fakeRows{missing: map[string]map[int64]bool{"S007-USDT": {base.Add(2 * time.Minute).Unix(): true}}}
	checker := &Checker{Timeline: fakePlans{[]*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), subjects...)}}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}
	result, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(5 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, 2, rows.reads, "标的按每批 subjectsPerRead 个合并读取")
	require.Equal(t, len(subjects)*5, result.Expected)
	require.Len(t, result.Gaps, 1)
	require.Equal(t, "S007-USDT", result.Gaps[0].SubjectID)
}

func TestCheckIgnoresPeriodsBeforeDeadlineAndUnregisteredPlans(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	open := record(t, 2, store.FetchPlanApplying, base.Add(30*time.Minute).Unix(), "BTC-USDT", "SOL-USDT")
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT"), open}
	rows := &fakeRows{}
	// 8:10 之前收盘并过了迟到时间的才应有：8:00～8:09 共 10 根（8:09 这根 8:10:00 收盘，+15 秒）。
	checker := &Checker{Timeline: fakePlans{records}, Rows: rows, Now: func() time.Time { return base.Add(10*time.Minute + 20*time.Second) }}
	result, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, 10, result.Expected, "a plan whose snapshot is not registered yet contributes nothing")

	// 登记成功的未生效计划：P 之后应有新增标的。
	open.Progress = `{"registered":{"ds|1m":true}}`
	checker.Now = func() time.Time { return base.Add(time.Hour) }
	result, err = checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, 30+2*29, result.Expected)
}

func TestDeepCheckRequiresFields(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	rows := &fakeRows{noOpen: map[string]map[int64]bool{"BTC-USDT": {base.Add(2 * time.Minute).Unix(): true}}}
	checker := &Checker{Timeline: fakePlans{[]*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT")}}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}
	shallow, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(5 * time.Minute)})
	require.NoError(t, err)
	require.Zero(t, shallow.Missing)
	deep, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(5 * time.Minute), Deep: true})
	require.NoError(t, err)
	require.Equal(t, 1, deep.Missing)
	require.Equal(t, fmt.Sprint(base.Add(2*time.Minute)), fmt.Sprint(deep.Gaps[0].From))
}

func seriesOf(subject string) seriesset.Series {
	return seriesset.Series{SubjectID: subject, SeriesTag: "t"}
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestCheckSplitsGapsAtNonTradingPeriods(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT")}
	rows := &fakeRows{missing: map[string]map[int64]bool{"BTC-USDT": {}}}
	for _, minute := range []int{2, 3, 4, 5, 6, 7} {
		rows.missing["BTC-USDT"][base.Add(time.Duration(minute)*time.Minute).Unix()] = true
	}
	// 第 4、5 分钟不是交易分钟：它们既不应有、也不算缺口，两侧的缺口不能合并成一段。
	calendar := func(_, _ string, barStart time.Time) (bool, error) {
		minute := int(barStart.Sub(base) / time.Minute)
		return minute != 4 && minute != 5, nil
	}
	checker := &Checker{Timeline: fakePlans{records}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }, Calendar: calendar}
	result, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(10 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, 8, result.Expected)
	require.Equal(t, 4, result.Missing)
	require.Len(t, result.Gaps, 2)
	require.Equal(t, base.Add(2*time.Minute), result.Gaps[0].From)
	require.Equal(t, base.Add(4*time.Minute), result.Gaps[0].To)
	require.Equal(t, base.Add(6*time.Minute), result.Gaps[1].From)
	require.Equal(t, base.Add(8*time.Minute), result.Gaps[1].To)
}

func TestCheckRejectsTooManyPointsWithShrinkableError(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	subjects := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		subjects = append(subjects, fmt.Sprintf("S%d-USDT", i))
	}
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), subjects...)}
	checker := &Checker{Timeline: fakePlans{records}, Rows: &fakeRows{}, Now: func() time.Time { return base.Add(24 * 365 * time.Hour) }}
	_, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(24 * 365 * time.Hour)})
	require.ErrorIs(t, err, errTooManyChunks)
}

func TestMonthlyBarsUseCalendarArithmetic(t *testing.T) {
	month, err := marketdata.ParseFrequency("1M")
	require.NoError(t, err)
	from := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	require.Equal(t, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), alignUp(month, from))
	require.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), alignDown(month, from))
	require.Equal(t, 3, barCount(month, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)))
	parts := splitGap(Gap{From: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}, month)
	require.Len(t, parts, 1)
}

func TestWeeklyBarsAlignOnMonday(t *testing.T) {
	week, err := marketdata.ParseFrequency("1w")
	require.NoError(t, err)
	// 2026-10-14 是周三；周线 K 线开始于周一 00:00 UTC。
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	require.Equal(t, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), alignDown(week, at))
	require.Equal(t, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), alignUp(week, at))
	monday := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	require.Equal(t, monday, alignUp(week, monday))
}

// 深度检查的必需字段不含附加字段：来源没有值时这一列本来就是空的，不能把这些行算成缺失。
func TestDeepFieldsExcludeExtraFields(t *testing.T) {
	checker := &Checker{Fields: marketdata.FieldOffers{{MarketID: "stockcn", Instrument: "equity", ProviderID: "fake", Spec: marketdata.FieldSpec{ID: "adj_factor"}}}}
	require.Equal(t, []string{"close", "amount"}, checker.deepFields("stockcn", []string{"close", "adj_factor", "amount"}))
	require.Equal(t, []string{"open", "high", "low", "close"}, checker.deepFields("stockcn", []string{"adj_factor"}), "只选了附加字段时按 OHLC")
	require.Equal(t, []string{"open", "high", "low", "close"}, checker.deepFields("stockcn", nil))
	require.Equal(t, []string{"close", "adj_factor"}, checker.deepFields("crypto", []string{"close", "adj_factor"}), "别的市场里它不是附加字段")
}

func openBackfillStore(t *testing.T) *store.BackfillRepository {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db.Backfill()
}

// markMissing 把 [from, to) 内每分钟登记为缺失。
func markMissing(rows *fakeRows, subject string, from, to time.Time) {
	if rows.missing[subject] == nil {
		rows.missing[subject] = map[int64]bool{}
	}
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		rows.missing[subject][t.Unix()] = true
	}
}

// goneRecord 构造把数据集移出采集计划的记录（任务删除后对账产生）。
func goneRecord(t *testing.T, id int64, dataset string, p int64) *store.FetchPlanRecord {
	t.Helper()
	content := &fetchplan.FetchPlanContent{Datasets: map[string]*fetchplan.PlanDataset{}, Functions: map[string]*fetchplan.PlanFunctionChange{}}
	change := &fetchplan.PlanDataset{Action: fetchplan.PlanActionAppend, Timeline: true, P: p, Copies: []fetchplan.DatasetCopy{{From: p, Stop: true}}}
	change.DatasetID, change.Frequency = dataset, "1m"
	content.Datasets[dataset+"|1m"] = change
	return &store.FetchPlanRecord{ID: id, SpaceID: "crypto", Status: store.FetchPlanApplied, Plan: mustJSON(content), Progress: "{}"}
}

// failingRows 的每次读取都失败。
type failingRows struct{ err error }

func (f failingRows) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return nil, f.err
}

// 已停止采集的数据集（结果数据可能已随任务删除）不再参与自动补采，否则会因读不到 View 反复失败。
func TestAutoScanSkipsDatasetsThatStoppedCollecting(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	records := []*store.FetchPlanRecord{
		recordFor(t, 1, "removed", store.FetchPlanApplied, base.Unix(), "A-USDT"),
		goneRecord(t, 2, "removed", base.Add(5*time.Minute).Unix()),
	}
	timeline := BuildTimeline(timelineRows(records))
	require.False(t, timeline.Active("removed|1m"))

	now := base.Add(48 * time.Hour)
	repo := openBackfillStore(t)
	backfiller := &Backfiller{Checker: &Checker{Timeline: fakePlans{records}, Rows: failingRows{errors.New("dataset has no active view")}, Now: func() time.Time { return now }}, Store: repo, Routes: fakeRoutes{},
		Now: func() time.Time { return now.Truncate(autoScanInterval).Add(autoScanOffset + time.Second) }}
	require.NoError(t, backfiller.AutoScan(context.Background(), "crypto"))
	jobs, err := repo.ActiveJobs(context.Background(), "crypto")
	require.NoError(t, err)
	require.Empty(t, jobs)
}
