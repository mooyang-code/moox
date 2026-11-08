package klinecheck

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	spotTag = "venue:binance|market:spot|source:spot_http"
	swapTag = "venue:binance|market:swap|source:swap_http"
)

// seriesRows 是认识 series_tag 的假 Storage：缺失按（tag、标的、时间）登记；读行和计数都按选择器里的 tag 选序列，
// 不带 tag 时同一标的的多条序列合并（与真实 Storage 一致：计数里同一时间点只算一次）。
type seriesRows struct {
	mu      sync.Mutex
	missing map[string]map[string]map[int64]bool // tag -> subject -> 时间 -> 缺失
	tags    []string
	// writes 记录补采写入的（tag、标的、时间）。
	writes []string
}

func (f *seriesRows) selected(selector *storagepb.TimeSeriesSelector) []string {
	if selector.SeriesTag != nil {
		return []string{selector.GetSeriesTag()}
	}
	return f.tags
}

func (f *seriesRows) present(selector *storagepb.TimeSeriesSelector, at time.Time) bool {
	for _, tag := range f.selected(selector) {
		if !f.missing[tag][selector.GetSubjectId()][at.Unix()] {
			return true
		}
	}
	return false
}

func (f *seriesRows) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetStartTime())
	end, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetEndTime())
	rsp := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, PageResult: &storagepb.PageResult{}}
	for _, selector := range req.GetSelectors() {
		for at := start; at.Before(end); at = at.Add(time.Minute) {
			if f.present(selector, at) {
				rsp.Rows = append(rsp.Rows, &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{SubjectId: selector.GetSubjectId(), DataTime: at.Format(time.RFC3339)}})
			}
		}
	}
	return rsp, nil
}

func (f *seriesRows) CountTimeSeriesRows(_ context.Context, req *storagepb.CountTimeSeriesRowsReq, _ ...client.Option) (*storagepb.CountTimeSeriesRowsRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetStartTime())
	end, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetEndTime())
	rsp := &storagepb.CountTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}
	for _, selector := range req.GetSelectors() {
		count := uint32(0)
		for at := start; at.Before(end); at = at.Add(time.Minute) {
			if f.present(selector, at) {
				count++
			}
		}
		rsp.Counts = append(rsp.Counts, &storagepb.SubjectRowCount{SubjectId: selector.GetSubjectId(), Rows: count})
	}
	return rsp, nil
}

// tagHealingInvoker 按调用里的路由（来源、市场）判断写出的行属于哪条序列，并补上这条序列的缺失。
type tagHealingInvoker struct {
	mu     sync.Mutex
	rows   *seriesRows
	events []map[string]any
}

func (h *tagHealingInvoker) Invoke(_ context.Context, _, _ string, event map[string]any, _ cloudfunctionpb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
	item := event["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	tag := marketfetch.PlanSeriesTag("crypto", marketfetch.PlanMarket{Provider: item["provider"].(string), SourceID: item["source_id"].(string), MarketType: item["market_type"].(string)})
	from, _ := time.Parse(time.RFC3339, item["start_time"].(string))
	to, _ := time.Parse(time.RFC3339, item["end_time"].(string))
	subject := item["subject_id"].(string)
	h.rows.mu.Lock()
	defer h.rows.mu.Unlock()
	for at := from; at.Before(to); at = at.Add(time.Minute) {
		h.rows.writes = append(h.rows.writes, tag+"|"+subject)
		delete(h.rows.missing[tag][subject], at.Unix())
	}
	return scfinvoker.InvocationResult{Result: map[string]any{"success": true}}, nil
}

// newUnifiedFixture 建一个统一数据集：BTC、ETH 都有现货与永续序列，永续的 BTC 缺 3～5 分钟。
func newUnifiedFixture(t *testing.T) (*Backfiller, *tagHealingInvoker, *seriesRows, time.Time) {
	backfiller, invoker, rows, base, _ := newUnifiedFixtureWithDB(t)
	return backfiller, invoker, rows, base
}

func newUnifiedFixtureWithDB(t *testing.T) (*Backfiller, *tagHealingInvoker, *seriesRows, time.Time, *store.Store) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })

	const slot = 5 * time.Minute
	now := time.Now().UTC().Add(slot).Truncate(slot).Add(3 * time.Minute)
	base := now.Add(-48*time.Hour - 3*time.Minute)
	options := marketfetch.PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}, "swap": {"ap-hongkong"}}, DefaultLatenessS: 15, Fetch: marketfetch.PlanFetch{Concurrency: 5}}
	subjects := []string{"BTC-USDT", "ETH-USDT"}
	desired, err := marketfetch.BuildDesiredState(options, []marketfetch.TaskGroup{
		{Provider: "binance", SourceID: "spot_http", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m", Subjects: subjects},
		{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "perpetual", DatasetID: "ds", Frequency: "1m", Subjects: subjects},
	}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return base.Unix(), nil })
	require.NoError(t, err)
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	record, err := db.FetchPlans().Create(context.Background(), "crypto", string(raw), "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(context.Background(), record.ID, store.FetchPlanApplied, "{}", ""))

	rows := &seriesRows{tags: []string{spotTag, swapTag}, missing: map[string]map[string]map[int64]bool{
		spotTag: {"BTC-USDT": {}, "ETH-USDT": {}}, swapTag: {"BTC-USDT": {}, "ETH-USDT": {}},
	}}
	for _, minute := range []int{3, 4, 5} {
		rows.missing[swapTag]["BTC-USDT"][base.Add(time.Duration(minute)*time.Minute).Unix()] = true
	}
	checker := &Checker{Plans: db.FetchPlans(), Rows: rows, Absences: db.Backfill(), Now: func() time.Time { return now }}
	invoker := &tagHealingInvoker{rows: rows}
	backfiller := &Backfiller{
		Checker: checker, Store: db.Backfill(), Plans: db.FetchPlans(), Invoker: invoker, BaseCtx: context.Background(), Now: func() time.Time { return now },
		Functions: func(string) FunctionProvider { return fakeFunctions{} },
	}
	return backfiller, invoker, rows, base, db
}

// 统一数据集里只缺永续的一段：按序列检查能查出，缺口标明是永续；现货同一时间点有数据不会把它盖住。
// 计数路径（一次只数一条序列）与读行路径都要得到同样的结果。
func TestCheckUnifiedDatasetFindsGapOfOneSeriesOnly(t *testing.T) {
	backfiller, _, rows, base := newUnifiedFixture(t)
	ctx := context.Background()
	for name, checker := range map[string]*Checker{
		"计数后读行": backfiller.Checker,
		"只读行":   {Plans: backfiller.Checker.Plans, Rows: readOnlyRows{rows}, Absences: backfiller.Checker.Absences, Now: backfiller.Checker.Now},
	} {
		result, err := checker.Check(ctx, Request{SpaceID: "crypto", From: base, To: base.Add(time.Hour)})
		require.NoError(t, err, name)
		require.Equal(t, 4*60, result.Expected, name+"：期望点数按序列算，2 个标的 × 2 条序列")
		require.Equal(t, 3, result.Missing, name)
		require.Len(t, result.Gaps, 1, name)
		require.Equal(t, Gap{DatasetID: "ds", Frequency: "1m", SubjectID: "BTC-USDT", SeriesTag: swapTag, From: base.Add(3 * time.Minute), To: base.Add(6 * time.Minute), Missing: 3}, result.Gaps[0], name)
	}

	// 指定序列只检查它自己：现货没有缺口。
	spot, err := backfiller.Checker.Check(ctx, Request{SpaceID: "crypto", SeriesTag: spotTag, From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, 2*60, spot.Expected)
	require.Zero(t, spot.Missing)
}

// readOnlyRows 隐藏计数能力，让检查走逐行比对的路径。
type readOnlyRows struct{ rows *seriesRows }

func (r readOnlyRows) ReadTimeSeriesRows(ctx context.Context, req *storagepb.ReadTimeSeriesRowsReq, opts ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return r.rows.ReadTimeSeriesRows(ctx, req, opts...)
}

// 补采永续的缺口：函数按永续的路由取数（来源 swap_http、市场 swap），写出的行只属于永续序列，现货行一根都没有写。
func TestBackfillUnifiedDatasetUsesTheMarketRouteOfTheGap(t *testing.T) {
	backfiller, invoker, rows, base := newUnifiedFixture(t)
	ctx := context.Background()
	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.Equal(t, 1, chunks)

	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)

	require.Len(t, invoker.events, 1)
	item := invoker.events[0]["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	require.Equal(t, "swap", item["market_type"])
	require.Equal(t, "swap_http", item["source_id"])
	require.Equal(t, "BTC-USDT", item["subject_id"])
	require.Equal(t, []string{swapTag + "|BTC-USDT", swapTag + "|BTC-USDT", swapTag + "|BTC-USDT"}, rows.writes, "只写永续序列的行，没有现货行")

	_, chunkRows, err := backfiller.Store.GetJob(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, swapTag, chunkRows[0].SeriesTag)
	require.Contains(t, chunkRows[0].ChunkID, swapTag)
	after, err := backfiller.Check(ctx, Request{SpaceID: "crypto", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Zero(t, after.Missing)
}

// 补采完成后写下的补写通知带序列，供合成与因子只重算这条序列。
func TestBackfillUnifiedDatasetNoticeCarriesSeriesTag(t *testing.T) {
	backfiller, _, _, base, db := newUnifiedFixtureWithDB(t)
	ctx := context.Background()
	_, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()

	notices, err := db.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, notices, 1)
	require.Equal(t, swapTag, notices[0].SeriesTag)
	require.Equal(t, "BTC-USDT", notices[0].SubjectID)
}

// 只有一个市场的计划能补该序列：缺口属于永续，而计划里没有永续的函数时直接报错，不会退回现货的路由。
func TestDatasetMarketRefusesToBorrowAnotherMarketsPlan(t *testing.T) {
	backfiller, _, _, base := newUnifiedFixture(t)
	ctx := context.Background()
	plan, err := backfiller.datasetMarket(ctx, "crypto", "ds", "1m", "BTC-USDT", swapTag, base.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "swap", plan.Market.MarketType)
	plan, err = backfiller.datasetMarket(ctx, "crypto", "ds", "1m", "BTC-USDT", spotTag, base.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "spot", plan.Market.MarketType)
	_, err = backfiller.datasetMarket(ctx, "crypto", "ds", "1m", "BTC-USDT", "venue:binance|market:delivery|source:delivery_http", base.Add(time.Minute))
	require.ErrorContains(t, err, "not part of any fetch plan")
}

// 已确认的行情源缺失也按序列：永续确认没有的 K 线不会抵消现货同一时间点的缺口。
func TestAbsencesAreRecordedPerSeries(t *testing.T) {
	backfiller, _, _, base := newUnifiedFixture(t)
	ctx := context.Background()
	require.NoError(t, backfiller.Store.AddAbsences(ctx, []store.SourceAbsence{
		{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", SubjectID: "BTC-USDT", SeriesTag: swapTag, From: base.Add(3 * time.Minute), To: base.Add(6 * time.Minute), Bars: 3},
	}))
	swap, err := backfiller.Store.AbsencesIn(ctx, "crypto", "ds", "1m", swapTag, nil, base, base.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, swap, 1)
	spot, err := backfiller.Store.AbsencesIn(ctx, "crypto", "ds", "1m", spotTag, nil, base, base.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, spot)

	// 永续的缺口被确认扣除后检查不再报告；现货不受影响。
	result, err := backfiller.Check(ctx, Request{SpaceID: "crypto", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Zero(t, result.Missing)
	require.Equal(t, 3, result.Absent)
}

// 历史缺口扫描同样按序列：只缺永续的一段，缺口组带永续的 tag，现货的同一时间点不受影响。
func TestCheckHistoryUnifiedDatasetGroupsGapsBySeries(t *testing.T) {
	backfiller, _, rows, base := newUnifiedFixture(t)
	ctx := context.Background()
	rows.missing[swapTag]["ETH-USDT"][base.Add(-10*time.Minute).Unix()] = true
	result, err := backfiller.Checker.CheckHistory(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.Equal(t, 2, result.Subjects)
	require.Len(t, result.Gaps, 1)
	require.Equal(t, swapTag, result.Gaps[0].SeriesTag)
	require.Equal(t, []string{"ETH-USDT"}, result.Gaps[0].Subjects)

	scan := historyScanOf("crypto", base, result)
	require.Equal(t, 1, scan.GapBars)
	require.Contains(t, scan.Gaps, `"series_tag":"`+swapTag+`"`)
}
