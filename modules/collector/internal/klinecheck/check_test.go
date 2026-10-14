package klinecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakePlans struct{ records []*store.FetchPlanRecord }

func (f fakePlans) ListAll(context.Context, string) ([]*store.FetchPlanRecord, error) {
	return f.records, nil
}

// fakeRows 按“标的 → 缺失的开始时间”返回行；深度检查时缺 open 字段的时间点单独登记。
type fakeRows struct {
	mu       sync.Mutex
	from, to time.Time
	missing  map[string]map[int64]bool
	noOpen   map[string]map[int64]bool
	reads    int
}

func (f *fakeRows) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	start, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetStartTime())
	end, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetEndTime())
	rsp := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, PageResult: &storagepb.PageResult{}}
	for _, selector := range req.GetSelectors() {
		for t := start; t.Before(end); t = t.Add(time.Minute) {
			if f.missing[selector.GetSubjectId()][t.Unix()] {
				continue
			}
			row := &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{SubjectId: selector.GetSubjectId(), DataTime: t.Format(time.RFC3339)}}
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

// recordFor 构造只含一个“数据集|1m”变更的计划记录。
func recordFor(t *testing.T, id int64, dataset, status string, p int64, subjects ...string) *store.FetchPlanRecord {
	t.Helper()
	content := &marketfetch.FetchPlanContent{Datasets: map[string]*marketfetch.PlanDatasetChange{}, Functions: map[string]*marketfetch.PlanFunctionChange{}}
	change := &marketfetch.PlanDatasetChange{Changed: true, HashChanged: true, P: p}
	change.DatasetID, change.Frequency, change.LatenessS = dataset, "1m", 15
	for _, subject := range subjects {
		change.Series = append(change.Series, seriesOf(subject))
	}
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
	checker := &Checker{Plans: fakePlans{records}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}
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

// 采集计划只覆盖生效点之后：默认检查不看更早的时间；显式要求时把最早一段前移，更早的范围按最早一段的标的计算。
func TestCheckReachesBeforeTheFirstPlanOnlyWhenAsked(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	rows := &fakeRows{missing: map[string]map[int64]bool{"BTC-USDT": {}, "ETH-USDT": {}}}
	checker := &Checker{Plans: fakePlans{[]*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT", "ETH-USDT")}}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}
	from, to := base.Add(-10*time.Minute), base.Add(5*time.Minute)

	plain, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: from, To: to, AssumeMissing: true})
	require.NoError(t, err)
	require.Equal(t, 2*5, plain.Expected, "不带 BeforeFirstPlan 时只算生效点之后的 5 根")

	reach, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: from, To: to, AssumeMissing: true, BeforeFirstPlan: true})
	require.NoError(t, err)
	require.Equal(t, 2*15, reach.Expected)
	require.Len(t, reach.Gaps, 2)
	require.Equal(t, from, reach.Gaps[0].From, "缺口从范围起点开始，不被生效点截断")
	require.Equal(t, 15, reach.Gaps[0].Missing)

	// 读行的检查同样：生效点之前读到的缺失才会报出来。
	rows.missing["BTC-USDT"][base.Add(-3*time.Minute).Unix()] = true
	read, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: from, To: to, BeforeFirstPlan: true})
	require.NoError(t, err)
	require.Len(t, read.Gaps, 1)
	require.Equal(t, base.Add(-3*time.Minute), read.Gaps[0].From)
	hidden, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: from, To: to})
	require.NoError(t, err)
	require.Empty(t, hidden.Gaps, "默认检查看不到生效点之前的缺口")
}

func TestCheckReadsManySubjectsInBatches(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	subjects := make([]string, 0, subjectsPerRead+20)
	for i := 0; i < subjectsPerRead+20; i++ {
		subjects = append(subjects, fmt.Sprintf("S%03d-USDT", i))
	}
	rows := &fakeRows{missing: map[string]map[int64]bool{"S007-USDT": {base.Add(2 * time.Minute).Unix(): true}}}
	checker := &Checker{Plans: fakePlans{[]*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), subjects...)}}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}
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
	checker := &Checker{Plans: fakePlans{records}, Rows: rows, Now: func() time.Time { return base.Add(10*time.Minute + 20*time.Second) }}
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
	checker := &Checker{Plans: fakePlans{[]*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "BTC-USDT")}}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }}
	shallow, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(5 * time.Minute)})
	require.NoError(t, err)
	require.Zero(t, shallow.Missing)
	deep, err := checker.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.Add(5 * time.Minute), Deep: true})
	require.NoError(t, err)
	require.Equal(t, 1, deep.Missing)
	require.Equal(t, fmt.Sprint(base.Add(2*time.Minute)), fmt.Sprint(deep.Gaps[0].From))
}

func seriesOf(subject string) seriesset.Series {
	return seriesset.Series{Provider: "binance", SourceID: "binance", MarketType: "spot", SubjectID: subject, SeriesTag: "t"}
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
	checker := &Checker{Plans: fakePlans{records}, Rows: rows, Now: func() time.Time { return base.Add(time.Hour) }, Calendar: calendar}
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
	checker := &Checker{Plans: fakePlans{records}, Rows: &fakeRows{}, Now: func() time.Time { return base.Add(24 * 365 * time.Hour) }}
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
