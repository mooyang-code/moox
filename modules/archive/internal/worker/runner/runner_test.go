package runner

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

var columns = []staging.Column{{Name: "open", Type: staging.TypeDouble}, {Name: "close", Type: staging.TypeDouble}}

type fakeSource struct {
	rows     []staging.Row
	requests []source.Range
	failures int // 前几次读取失败
	failAll  error
	columns  []staging.Column
	// failAfter 大于 0 时，成功服务 failAfter 页之后的读取全部失败。
	failAfter int
	served    int
}

func (f *fakeSource) Columns(context.Context, string, string) ([]staging.Column, error) {
	if f.columns != nil {
		return f.columns, nil
	}
	return columns, nil
}

func (f *fakeSource) ReadRange(_ context.Context, req source.Range) (source.Page, error) {
	f.requests = append(f.requests, req)
	if f.failAll != nil {
		return source.Page{}, f.failAll
	}
	if f.failures > 0 {
		f.failures--
		return source.Page{}, errors.New("transient")
	}
	if f.failAfter > 0 && f.served >= f.failAfter {
		return source.Page{}, errors.New("source went away")
	}
	f.served++
	var matched []staging.Row
	for _, row := range f.rows {
		if !row.DataTime.Before(req.Start) && row.DataTime.Before(req.End) {
			matched = append(matched, row)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].DataTime.Equal(matched[j].DataTime) {
			return matched[i].SubjectID < matched[j].SubjectID
		}
		return matched[i].DataTime.Before(matched[j].DataTime)
	})
	from := 0
	if req.Cursor != "" {
		from, _ = strconv.Atoi(req.Cursor)
	}
	to := min(from+req.PageRows, len(matched))
	page := source.Page{Rows: matched[from:to], Bytes: int64(100 * (to - from))}
	if to < len(matched) {
		page.NextCursor = strconv.Itoa(to)
	}
	return page, nil
}

type noPacer struct{ throttled int64 }

func (noPacer) WaitTurn(context.Context) error               { return nil }
func (p *noPacer) Throttle(_ context.Context, n int64) error { p.throttled += n; return nil }
func (noPacer) Sleep(context.Context, time.Duration) error   { return nil }
func (noPacer) Interval() time.Duration                      { return 0 }

type env struct {
	t     *testing.T
	root  string
	store *staging.Store
	src   *fakeSource
	pace  *noPacer
	now   time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	store, err := staging.Open(filepath.Join(root, ".state", "staging.duckdb"), staging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &env{t: t, root: root, store: store, src: &fakeSource{}, pace: &noPacer{}, now: time.Date(2026, 10, 17, 1, 4, 43, 0, time.UTC)}
}

func (e *env) runner(pageRows int) *Runner {
	return &Runner{Source: e.src, Staging: e.store, Files: export.Files{Root: e.root}, Pace: e.pace, PageRows: pageRows, Now: func() time.Time { return e.now }, ReadAttempts: 3}
}

func (e *env) assignment(mutate func(*archivepb.RunAssignment)) *archivepb.RunAssignment {
	a := &archivepb.RunAssignment{
		RunId: "run_1", LeaseToken: "tok", Kind: "scheduled", SpaceId: "crypto", DatasetId: "ds_1m", Freq: "1m", PartitionUnit: "day", DataNodeId: "node-0",
		Retention: "168h", IntervalSeconds: 3600, MinLookbackSeconds: 900, SpaceTz: "UTC", StagingRetentionSeconds: 72 * 3600,
		StartedAt: e.now.Format(time.RFC3339), ScheduledAt: e.now.Format(time.RFC3339),
	}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func (e *env) run(pageRows int, mutate func(*archivepb.RunAssignment), report func(Progress) bool) *Outcome {
	e.t.Helper()
	out, err := e.runner(pageRows).Execute(context.Background(), e.assignment(mutate), report)
	if err != nil {
		e.t.Fatalf("Execute: %v", err)
	}
	return out
}

func rows(subject string, from time.Time, n int, base float64) []staging.Row {
	var out []staging.Row
	for i := 0; i < n; i++ {
		out = append(out, staging.Row{SubjectID: subject, DataTime: from.Add(time.Duration(i) * time.Minute), Values: []any{base + float64(i), base + float64(i) + 0.5}})
	}
	return out
}

func report(out *Outcome, key string) *archivepb.PartitionReport {
	for _, p := range out.Partitions {
		if p.GetPartitionKey() == key {
			return p
		}
	}
	return nil
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	return string(raw)
}

func TestFirstRunStagesRowsWithoutBackfillAndAdvancesTheWatermark(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = append(rows("BTC", day, 60, 1), rows("ETH", day, 60, 100)...)
	var progress []Progress
	out := e.run(50, nil, func(p Progress) bool { progress = append(progress, p); return false })
	if out.Status != "succeeded" || out.Progress.RowsRead != 120 || out.RowsAdded != 120 || out.RowsChanged != 0 || out.Progress.RequestCount != 4 || out.Progress.SubjectsDone != 2 {
		t.Fatalf("out = %+v", out)
	}
	// 区间 = 本次运行开始 − 执行间隔 − L − S（1m）= 01:04:43 − 1h − 15m − 1m = 前一天 23:48:43，按分区边界切成两块。
	first, last := e.src.requests[0], e.src.requests[len(e.src.requests)-1]
	if want := e.now.Add(-time.Hour - 15*time.Minute - time.Minute); !first.Start.Equal(want) || !first.End.Equal(day) || !last.End.Equal(e.now) {
		t.Fatalf("range = [%s, %s) … %s, want start %s", first.Start, first.End, last.End, want)
	}
	if len(progress) != 4 || e.pace.throttled != 12000 {
		t.Fatalf("progress=%d throttled=%d", len(progress), e.pace.throttled)
	}
	// 今天的分区还没到导出时间：只登记为 open（暂存中），没有文件。
	p := report(out, "2026-10-17")
	if p == nil || p.GetStatus() != "open" || p.GetRowCount() != 120 || p.GetSubjectCount() != 2 || p.GetFilePath() != "" || out.Exported != 0 {
		t.Fatalf("partition = %v", p)
	}
	// 昨天 23:48 起的区间跨了零点，所以前一天的分区也有行？这里数据只在今天，昨天没有行。
	if report(out, "2026-10-16") != nil {
		t.Fatal("没有行的分区不应登记")
	}
	if w, ok, _ := e.store.Watermark(context.Background(), "crypto", "ds_1m"); !ok || !w.Equal(e.now) {
		t.Fatalf("watermark = %v %v", w, ok)
	}
	if _, err := os.Stat(filepath.Join(e.root, "crypto", "ds_1m", "_dataset.json")); err != nil {
		t.Fatalf("_dataset.json: %v", err)
	}
}

func TestSecondRunReadsFromTheWatermarkMinusLookbackAndSkipsUnchangedRows(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day, 120, 1) // 00:00–01:59
	e.run(2000, nil, nil)
	e.src.requests = nil
	e.now = e.now.Add(time.Hour)
	out := e.run(2000, nil, nil)
	// 起点 = W − L − S = 01:04:43 − 15m − 1m；已读过的行值没变，不产生写入。
	if want := time.Date(2026, 10, 17, 0, 48, 43, 0, time.UTC); !e.src.requests[0].Start.Equal(want) {
		t.Fatalf("start = %s, want %s", e.src.requests[0].Start, want)
	}
	// 第二次运行读 00:48:43 起的 71 行：前 16 行上一次已读过（值没变，不产生写入），后 55 行是新到的。
	if out.Progress.RowsRead != 71 || out.RowsAdded != 55 || out.RowsChanged != 0 {
		t.Fatalf("out = %+v", out)
	}
	// 窗口内的晚到补采：一条新行 + 一条值变化。
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(118*time.Minute), 1, 5)...)
	e.src.rows[115].Values[1] = 999.0   // 01:55
	e.now = e.now.Add(26 * time.Minute) // 02:30:43：窗口起点 02:04:43 − 16m = 01:48:43
	out = e.run(2000, nil, nil)
	if out.RowsAdded != 1 || out.RowsChanged != 1 {
		t.Fatalf("补采应产生 1 新增 1 变化: %+v", out)
	}
}

func TestPartitionIsExportedAfterCloseAndThenFinalizedAfterTheHoldPeriod(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) // 昨天
	e.src.rows = append(rows("BTC", day.Add(23*time.Hour), 60, 1), rows("ETH", day.Add(23*time.Hour+30*time.Minute), 30, 50)...)
	// 第一次运行在次日 00:05：昨天的分区收盘 00:00，还要等 L=15 分钟 → 00:15 之前不导出。
	e.now = time.Date(2026, 10, 17, 0, 5, 43, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T22:00:00Z" }, nil)
	if p := report(out, "2026-10-16"); p == nil || p.GetStatus() != "open" || out.Exported != 0 {
		t.Fatalf("00:05 不应导出: %v", p)
	}
	e.now = time.Date(2026, 10, 17, 0, 34, 43, 0, time.UTC)
	out = e.run(2000, nil, nil)
	p := report(out, "2026-10-16")
	if p == nil || p.GetStatus() != "exported" || out.Exported != 1 || p.GetFilePath() != "crypto/ds_1m/2026/ds_1m_20261016.csv.gz" || p.GetFileSha256() == "" ||
		p.GetFileBytes() == 0 || p.GetExportCount() != 1 || !strings.HasPrefix(p.GetPreview(), "data_time,subject_id,series_tag,open,close\n") || p.GetRowCount() != 90 {
		t.Fatalf("partition = %v", p)
	}
	csv := readFile(t, e.root, p.GetFilePath())
	if strings.Count(csv, "\n") != 1+90 || !strings.HasPrefix(csv, "data_time,subject_id,series_tag,open,close\n2026-10-16T23:00:00Z,BTC,\"\",") {
		t.Fatalf("csv = %q", csv[:200])
	}
	if out.FinalUntil.IsZero() == false {
		t.Fatalf("还没有定稿的分区，定稿边界应为空: %v", out.FinalUntil)
	}
	// 没有变化的分区不会被重复导出。
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, nil, nil)
	if out.Exported != 0 {
		t.Fatalf("没有变化不应重写: %+v", out)
	}
	// 收盘 + L + 暂存保留期（3 天）之后定稿：删除暂存行，文件保留，定稿边界前进。
	e.now = time.Date(2026, 10, 20, 0, 20, 43, 0, time.UTC)
	out = e.run(2000, nil, nil)
	p = report(out, "2026-10-16")
	if p == nil || p.GetStatus() != "final" || out.Finalized != 1 || p.GetRowCount() != 90 || p.GetFinalizedAt() == "" || p.GetFilePath() == "" {
		t.Fatalf("定稿: %v out=%+v", p, out)
	}
	if want := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC); !out.FinalUntil.Equal(want) {
		t.Fatalf("final_until = %s", out.FinalUntil)
	}
	state, _, _ := e.store.Partition(context.Background(), "crypto", "ds_1m", "2026-10-16")
	st, _ := e.store.Stats(context.Background(), staging.Task{SpaceID: "crypto", DatasetID: "ds_1m", Columns: columns}, state)
	if state.Status != staging.StatusFinal || st.Rows != 0 {
		t.Fatalf("state=%+v stats=%+v", state, st)
	}
	if _, err := os.Stat(filepath.Join(e.root, "crypto", "ds_1m", "2026", "ds_1m_20261016.csv.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestLateRowsReopenAFinalPartitionAndKeepEveryOriginalRow(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(time.Hour), 30, 1)
	// 补拉一周前的区间，一路走到定稿。
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	// 分区 10-10 收盘 10-11 00:00，加 L 与 3 天暂存保留期 = 10-14 00:15，本次运行 10-14 01:00 已经满足：先导出、随即定稿。
	if p := report(out, "2026-10-10"); p == nil || p.GetStatus() != "final" || p.GetExportCount() != 1 || out.Exported != 1 || out.Finalized != 1 {
		t.Fatalf("应已导出并定稿: %v out=%+v", p, out)
	}
	orig := readFile(t, e.root, "crypto/ds_1m/2026/ds_1m_20261010.csv.gz")
	// 用补拉区间读到一条晚到的行：先读回原文件，再写入，重新导出，包含原来的 30 行 + 1 行。
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(2*time.Hour), 1, 9)...)
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.RangeStart, a.RangeEnd = "range", "2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z"
	}, nil)
	p := report(out, "2026-10-10")
	if p == nil || out.RowsAdded != 1 {
		t.Fatalf("out = %+v", out)
	}
	if p.GetExportCount() != 2 || p.GetRowCount() != 31 || p.GetStatus() != "final" {
		t.Fatalf("重新导出后（范围已过暂存保留期，立即再次定稿）: %v", p)
	}
	rewritten := readFile(t, e.root, p.GetFilePath())
	if strings.Count(rewritten, "\n") != strings.Count(orig, "\n")+1 {
		t.Fatalf("重写的文件应包含原有的全部行 + 1: %d vs %d", strings.Count(rewritten, "\n"), strings.Count(orig, "\n"))
	}
	for _, line := range strings.Split(strings.TrimSpace(orig), "\n")[1:] {
		if !strings.Contains(rewritten, line) {
			t.Fatalf("重写后丢了原有的行 %q", line)
		}
	}
	if w, _, _ := e.store.Watermark(context.Background(), "crypto", "ds_1m"); !w.Equal(time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("补拉区间不能推进水位: %v", w)
	}
	// 再读一次同样的区间：值没变，不重写（导出次数不变）。
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.RangeStart, a.RangeEnd = "range", "2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z"
	}, nil)
	if p := report(out, "2026-10-10"); p != nil && p.GetExportCount() != 2 {
		t.Fatalf("没有变化不应重写: %v", p)
	}
}

func TestBackfillIsReadPartitionByPartition(t *testing.T) {
	e := newEnv(t)
	start := time.Date(2026, 10, 14, 22, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", start, 60*30, 1) // 跨 14 日晚到 16 日
	e.now = time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = start.Format(time.RFC3339); a.Retention = "forever" }, nil)
	var bounds []string
	for _, r := range e.src.requests {
		bounds = append(bounds, r.Start.Format("01-02T15")+"→"+r.End.Format("01-02T15"))
	}
	want := []string{"10-14T22→10-15T00", "10-15T00→10-16T00", "10-16T00→10-16T06"}
	if strings.Join(bounds, " ") != strings.Join(want, " ") {
		t.Fatalf("按分区切块: %v want %v", bounds, want)
	}
	if out.Progress.RowsRead != 60*30 || report(out, "2026-10-14") == nil || report(out, "2026-10-15") == nil {
		t.Fatalf("out = %+v", out)
	}
}

func TestRetentionClampsTheRangeAndReportsWhatCannotBeRecovered(t *testing.T) {
	e := newEnv(t)
	e.src.rows = rows("BTC", e.now.Add(-30*time.Hour), 10, 1)
	out := e.run(2000, func(a *archivepb.RunAssignment) {
		a.Retention = "24h"
		a.BackfillFrom = e.now.Add(-48 * time.Hour).Format(time.RFC3339)
	}, nil)
	if len(out.Unrecoverable) != 1 {
		t.Fatalf("unrecoverable = %v", out.Unrecoverable)
	}
	u := out.Unrecoverable[0]
	if u.GetStartTime() != e.now.Add(-48*time.Hour).Format(time.RFC3339) || u.GetEndTime() != e.now.Add(-24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("unrecoverable = %v", u)
	}
	for _, r := range e.src.requests {
		if r.Start.Before(e.now.Add(-24 * time.Hour)) {
			t.Fatalf("读取不能早于保留期截止: %s", r.Start)
		}
	}
	if out.Status != "succeeded" {
		t.Fatalf("out = %+v", out)
	}
	if _, err := e.runner(10).Execute(context.Background(), e.assignment(func(a *archivepb.RunAssignment) { a.Retention = "soon" }), nil); err != nil && out == nil {
		t.Fatal(err)
	}
}

func TestCancellationLeavesStagedBatchesAndDoesNotAdvanceTheWatermark(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day, 60, 1) // 00:00–00:59
	calls := 0
	out := e.run(20, nil, func(p Progress) bool { calls++; return p.RowsRead >= 40 })
	if out.Status != "canceled" || out.Progress.RowsRead != 40 {
		t.Fatalf("out = %+v", out)
	}
	if _, ok, _ := e.store.Watermark(context.Background(), "crypto", "ds_1m"); ok {
		t.Fatal("取消的运行不能推进水位")
	}
	if p := report(out, "2026-10-17"); p == nil || p.GetRowCount() != 40 || p.GetStatus() != "open" {
		t.Fatalf("已提交的批次留在暂存区并登记: %v", p)
	}
	// 下一次运行从同一个起点重读，已写入的行值没变，不重复计数。
	e.now = e.now.Add(time.Minute)
	out = e.run(20, nil, nil)
	if out.Status != "succeeded" || out.RowsAdded != 20 {
		t.Fatalf("out = %+v", out)
	}
	if p := report(out, "2026-10-17"); p.GetRowCount() != 60 {
		t.Fatalf("partition = %v", p)
	}
}

func TestReadFailuresAreRetriedThenReportedWithoutAdvancing(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day, 10, 1)
	e.src.failures = 2
	if out := e.run(2000, nil, nil); out.Status != "succeeded" || out.Progress.RowsRead != 10 {
		t.Fatalf("两次失败后重试成功: %+v", out)
	}
	e2 := newEnv(t)
	e2.src.failAll = errors.New("access unreachable")
	out := e2.run(2000, nil, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "access unreachable") {
		t.Fatalf("out = %+v", out)
	}
	if len(e2.src.requests) != 3 {
		t.Fatalf("requests = %d, want 3 attempts", len(e2.src.requests))
	}
	if _, ok, _ := e2.store.Watermark(context.Background(), "crypto", "ds_1m"); ok {
		t.Fatal("失败的运行不能推进水位")
	}
}

func TestStockCNPartitionWaitsForTheLocalCloseAndMonthPartitionsUseTheTwelveHourShift(t *testing.T) {
	e := newEnv(t)
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// A 股 1m：上海 2026-10-16 的交易时段 09:30–15:00 → 这天的分区是 [10-15 16:00Z, 10-16 16:00Z)。
	open := time.Date(2026, 10, 16, 9, 30, 0, 0, sh)
	e.src.rows = rows("600000", open.UTC(), 60, 1)
	cn := func(a *archivepb.RunAssignment) { a.SpaceTz, a.LocalClose = "Asia/Shanghai", "15:00" }
	e.now = time.Date(2026, 10, 16, 7, 10, 0, 0, time.UTC) // 上海 15:10：收盘 + 10 分钟，不到 15 分钟
	out := e.run(2000, func(a *archivepb.RunAssignment) { cn(a); a.BackfillFrom = "2026-10-15T16:00:00Z" }, nil)
	if p := report(out, "2026-10-16"); p == nil || p.GetStatus() != "open" || out.Exported != 0 {
		t.Fatalf("收盘 + L 之前不导出: %v", p)
	}
	e.now = time.Date(2026, 10, 16, 9, 4, 43, 0, time.UTC) // 上海 17:04:43
	out = e.run(2000, cn, nil)
	if p := report(out, "2026-10-16"); p == nil || p.GetStatus() != "exported" || out.Exported != 1 {
		t.Fatalf("17:04 应导出当天的分区: %v", p)
	}
	// 1d 数据按月：两种日线键约定的月初那根都落在当月文件里。
	m := newEnv(t)
	m.src.rows = []staging.Row{
		{SubjectID: "A", DataTime: time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC), Values: []any{1.0, 1.0}}, // 上海 10-01 零点
		{SubjectID: "B", DataTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Values: []any{2.0, 2.0}},  // 本地日期的 UTC 零点
		{SubjectID: "A", DataTime: time.Date(2026, 9, 29, 16, 0, 0, 0, time.UTC), Values: []any{3.0, 3.0}}, // 上海 09-30 零点
	}
	m.now = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	out = m.run(2000, func(a *archivepb.RunAssignment) {
		a.Freq, a.PartitionUnit, a.IntervalSeconds, a.Retention = "1d", "month", 86400, "forever"
		a.BackfillFrom = "2026-09-25T00:00:00Z"
	}, nil)
	oct, sep := report(out, "2026-10"), report(out, "2026-09")
	if oct == nil || oct.GetRowCount() != 2 || sep == nil || sep.GetRowCount() != 1 {
		t.Fatalf("月分区: oct=%v sep=%v", oct, sep)
	}
	if oct.GetRangeStart() != "2026-09-30T12:00:00Z" || oct.GetRangeEnd() != "2026-10-31T12:00:00Z" {
		t.Fatalf("月分区在 data_time 上的范围 = [%s, %s)", oct.GetRangeStart(), oct.GetRangeEnd())
	}
}

func TestExecuteRejectsBadAssignmentsAndMissingColumns(t *testing.T) {
	e := newEnv(t)
	for name, mutate := range map[string]func(*archivepb.RunAssignment){
		"bad started_at": func(a *archivepb.RunAssignment) { a.StartedAt = "now" },
		"bad tz":         func(a *archivepb.RunAssignment) { a.SpaceTz = "Mars/Base" },
		"bad unit":       func(a *archivepb.RunAssignment) { a.PartitionUnit = "week" },
		"bad range":      func(a *archivepb.RunAssignment) { a.Kind, a.RangeStart, a.RangeEnd = "range", "x", "y" },
		"bad backfill":   func(a *archivepb.RunAssignment) { a.BackfillFrom = "later" },
		"bad retention":  func(a *archivepb.RunAssignment) { a.Retention = "soon" },
		"bad freq":       func(a *archivepb.RunAssignment) { a.Freq = "7x" },
	} {
		out, err := e.runner(10).Execute(context.Background(), e.assignment(mutate), nil)
		if err == nil && (out == nil || out.Status != "failed") {
			t.Fatalf("%s: 应报错或失败: out=%+v", name, out)
		}
	}
	e.src.columns = []staging.Column{{Name: "Bad-Name", Type: staging.TypeDouble}}
	if _, err := e.runner(10).Execute(context.Background(), e.assignment(nil), nil); err == nil {
		t.Fatal("非法列名必须报错")
	}
}

func TestFirstRunRetryReadsFromTheSameStartAfterAFailure(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day, 60, 1)
	// 第一次运行：01:04:43 开始，首页成功后第二页失败（页大小 20，重试 1 次）。
	e.src.failAfter = 1
	r := e.runner(20)
	r.ReadAttempts = 1
	out, err := r.Execute(context.Background(), e.assignment(nil), nil)
	if err != nil || out.Status != "failed" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	firstStart := e.src.requests[0].Start
	e.src.requests, e.src.failAfter, e.src.served = nil, 0, 0
	// 一个小时之后重试：起点必须仍是第一次的起点（而不是重新往前推 1h16m），否则两次起点之间没读到的数据永久漏读。
	e.now = e.now.Add(time.Hour)
	out = e.run(20, nil, nil)
	if out.Status != "succeeded" || !e.src.requests[0].Start.Equal(firstStart) {
		t.Fatalf("重试起点 = %s, 第一次起点 = %s, out=%+v", e.src.requests[0].Start, firstStart, out)
	}
	// 水位前进之后，起点回到「水位 − L − S」。
	e.src.requests = nil
	e.now = e.now.Add(time.Hour)
	e.run(20, nil, nil)
	if want := time.Date(2026, 10, 17, 1, 4, 43, 0, time.UTC).Add(time.Hour).Add(-16 * time.Minute); !e.src.requests[0].Start.Equal(want) {
		t.Fatalf("start = %s, want %s", e.src.requests[0].Start, want)
	}
}

func TestPartitionsStayPendingUntilAcknowledgedSoALostCompletionIsReportedAgain(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(time.Hour), 30, 1)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	p := report(out, "2026-10-10")
	if p == nil || p.GetStatus() != "final" || p.GetRowCount() != 30 || len(out.ReportedKeys) != 1 {
		t.Fatalf("out=%+v", out)
	}
	// 上报失败（没有调用 MarkReported）：下一次运行没有触及这个分区，但仍要带着它（行数来自定稿时记下的统计）。
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, nil, nil)
	again := report(out, "2026-10-10")
	if again == nil || again.GetStatus() != "final" || again.GetRowCount() != 30 || again.GetSubjectCount() != 1 || again.GetFinalizedAt() == "" ||
		again.GetMinDataTime() != "2026-10-10T01:00:00Z" || again.GetFileSha256() != p.GetFileSha256() || again.GetFilePath() == "" {
		t.Fatalf("遗留的分区应再次上报: %v", again)
	}
	// 确认登记之后不再上报。
	if err := e.store.MarkReported(context.Background(), "crypto", "ds_1m", out.ReportedKeys); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, nil, nil)
	if report(out, "2026-10-10") != nil || len(out.ReportedKeys) != 0 {
		t.Fatalf("已确认的分区不应再上报: %+v", out.Partitions)
	}
}
