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

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
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
	// failFrom 非零时，起点不早于它的读取全部失败。
	failFrom   time.Time
	columnsErr error // 非空时 Columns 失败

	// 变更日志：changeLog 的第 i 条序号为 i+1，一页只返回一条（可带多个标的的范围）；游标早于 changeTrimmed 时断档。
	changeLog     [][]source.ChangeSpan
	changeTrimmed uint64
	changesErr    error
	changeCalls   int
	// headCalls 是只取头部位点（cursor 为 nil）的次数；onHead 在取头部时调用（测试在这里模拟“取头部之前刚写入”）。
	headCalls int
	onHead    func()
	// onRead 在每次读行之后调用（测试用它推进时钟）。
	onRead func(source.Range)
}

func (f *fakeSource) ReadChanges(_ context.Context, _, _ string, cursor *source.ChangeCursor) (source.Changes, error) {
	f.changeCalls++
	if f.changesErr != nil {
		return source.Changes{}, f.changesErr
	}
	if cursor == nil {
		f.headCalls++
		if f.onHead != nil {
			f.onHead()
		}
	}
	head := source.ChangeCursor{StoreID: "store", Sequence: uint64(len(f.changeLog))}
	if cursor == nil {
		return source.Changes{Next: head}, nil
	}
	if cursor.StoreID != head.StoreID || cursor.Sequence < f.changeTrimmed || cursor.Sequence > head.Sequence {
		return source.Changes{Next: head, Gap: true}, nil
	}
	if cursor.Sequence == head.Sequence {
		return source.Changes{Next: head}, nil
	}
	next := source.ChangeCursor{StoreID: head.StoreID, Sequence: cursor.Sequence + 1}
	return source.Changes{Spans: f.changeLog[cursor.Sequence], Next: next, HasMore: next.Sequence < head.Sequence, Bytes: 10}, nil
}

func (f *fakeSource) Columns(context.Context, string, string) ([]staging.Column, error) {
	if f.columnsErr != nil {
		return nil, f.columnsErr
	}
	if f.columns != nil {
		return f.columns, nil
	}
	return columns, nil
}

func (f *fakeSource) ReadRange(_ context.Context, req source.Range) (source.Page, error) {
	f.requests = append(f.requests, req)
	if f.onRead != nil {
		defer f.onRead(req)
	}
	if f.failAll != nil {
		return source.Page{}, f.failAll
	}
	if f.failures > 0 {
		f.failures--
		return source.Page{}, errors.New("transient")
	}
	if !f.failFrom.IsZero() && !req.Start.Before(f.failFrom) {
		return source.Page{}, errors.New("source went away")
	}
	if f.failAfter > 0 && f.served >= f.failAfter {
		return source.Page{}, errors.New("source went away")
	}
	f.served++
	only := map[string]bool{}
	for _, subject := range req.Subjects {
		only[subject] = true
	}
	var matched []staging.Row
	for _, row := range f.rows {
		if len(only) > 0 && !only[row.SubjectID] {
			continue
		}
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

// noPacer 不等待；allowed 非空时由它回答 AllowedAt（模拟秒窗口与整点窗口），waits 记 WaitTurn 的次数。
type noPacer struct {
	throttled int64
	waits     int
	allowed   func(time.Time) time.Time
}

func (p *noPacer) AllowedAt(t time.Time) time.Time {
	if p.allowed != nil {
		return p.allowed(t)
	}
	return t
}
func (p *noPacer) WaitTurn(context.Context) error            { p.waits++; return nil }
func (p *noPacer) Throttle(_ context.Context, n int64) error { p.throttled += n; return nil }
func (*noPacer) Sleep(context.Context, time.Duration) error  { return nil }
func (*noPacer) Interval() time.Duration                     { return 0 }

type env struct {
	t     *testing.T
	root  string
	store *staging.Store
	src   *fakeSource
	pace  *noPacer
	now   time.Time
	// format 是导出格式；默认 csv（便于直接读文件），空串即生产默认的 csv.gz。
	format string
	// remote 是对象存储客户端；nil 表示没有配置对象存储。
	remote *objectstore.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	store, err := staging.Open(filepath.Join(root, ".state", "staging.db"), staging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &env{t: t, root: root, store: store, src: &fakeSource{}, pace: &noPacer{}, now: time.Date(2026, 10, 17, 1, 4, 43, 0, time.UTC), format: export.FormatCSV}
}

func (e *env) runner(pageRows int) *Runner {
	return &Runner{Source: e.src, Staging: e.store, Files: export.Files{Root: e.root, Format: e.format}, Pace: e.pace, PageRows: pageRows, Now: func() time.Time { return e.now }, ReadAttempts: 3, Remote: e.remote}
}

func (e *env) assignment(mutate func(*archivepb.RunAssignment)) *archivepb.RunAssignment {
	a := &archivepb.RunAssignment{
		RunId: "run_1", LeaseToken: "tok", Kind: "follow", SpaceId: "crypto", DatasetId: "ds_1m", Purpose: "collect", DirName: "kline-1m", TaskId: "1", Freq: "1m", PartitionUnit: "day",
		Retention: "168h", AllowedLatenessSeconds: 900, SpaceTz: "UTC", StagingRetentionSeconds: 72 * 3600,
		StartedAt: e.now.Format(time.RFC3339), ScheduledAt: e.now.Format(time.RFC3339),
	}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func (e *env) run(pageRows int, mutate func(*archivepb.RunAssignment), report func(Progress) bool) *Outcome {
	e.t.Helper()
	out, err := e.runner(pageRows).Execute(context.Background(), e.assignment(mutate), report, nil)
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
	if strings.HasSuffix(rel, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		return string(raw)
	}
	raw, _ := io.ReadAll(f)
	return string(raw)
}

// 首次运行：位点记在日志头部，重读进度 [回补起点, 取头部之后的时刻)；按 1 小时一段读（不跨分区），最后一段不设上界，读完两列清空。
func TestFirstFollowRebasesAndRescansFromTheBackfill(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = append(rows("BTC", day, 60, 1), rows("ETH", day, 60, 100)...)
	e.src.changeLog = [][]source.ChangeSpan{{{SubjectID: "BTC", Start: day, End: day}}} // 首次运行之前已有的条目不重放
	var progress []Progress
	out := e.run(50, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T23:00:00Z" }, func(p Progress) bool { progress = append(progress, p); return false })
	if out.Status != "succeeded" || out.Progress.RowsRead != 120 || out.RowsAdded != 120 || out.RowsChanged != 0 || out.Progress.SubjectsDone != 2 || len(out.Unrecoverable) != 0 {
		t.Fatalf("out = %+v", out)
	}
	var bounds []string
	for _, r := range e.src.requests {
		if r.Cursor == "" {
			bounds = append(bounds, r.Start.Format("01-02T15:04")+"→"+r.End.Format("2006-01-02T15:04"))
		}
	}
	want := "10-16T23:00→2026-10-17T00:00 10-17T00:00→2026-10-17T01:00 10-17T01:00→2200-01-01T00:00"
	if strings.Join(bounds, " ") != want {
		t.Fatalf("分段 = %v", bounds)
	}
	if got := changeCursor(t, e); got != 1 {
		t.Fatalf("位点应记在日志头部: %d", got)
	}
	if _, _, ok, _ := e.store.Rescan(context.Background(), "crypto", "ds_1m"); ok {
		t.Fatal("读完后重读进度应清空")
	}
	if e.src.headCalls != 1 || len(progress) == 0 || e.pace.throttled != 12000 {
		t.Fatalf("headCalls=%d progress=%d throttled=%d", e.src.headCalls, len(progress), e.pace.throttled)
	}
	// 今天的分区还没到导出时间：只登记为 open（暂存中），没有文件。
	p := report(out, "2026-10-17")
	if p == nil || p.GetStatus() != "open" || p.GetRowCount() != 120 || p.GetSubjectCount() != 2 || p.GetFilePath() != "" || out.Exported != 0 {
		t.Fatalf("partition = %v", p)
	}
	if report(out, "2026-10-16") != nil {
		t.Fatal("没有行的分区不应登记")
	}
	if _, err := os.Stat(filepath.Join(e.root, "crypto", "collect", "kline-1m", "dataset.json")); err != nil {
		t.Fatalf("dataset.json: %v", err)
	}
}

// 没有新写入的一轮：不读行，到期分区照样导出、定稿，位点不变；之后按日志只读改动的范围。
func TestFollowWithoutNewWritesExportsDuePartitionsAndKeepsTheCursor(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day, 120, 1) // 00:00–01:59
	e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-17T00:00:00Z" }, nil)
	before := changeCursor(t, e)
	e.src.requests = nil
	e.now = time.Date(2026, 10, 18, 0, 20, 43, 0, time.UTC)    // 10-17 收盘 + 15 分钟之后
	e.src.rows = append(e.src.rows, rows("ETH", day, 1, 5)...) // 日志里没有的写入不会被读到
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || len(e.src.requests) != 0 || out.Exported != 1 || changeCursor(t, e) != before {
		t.Fatalf("out=%+v requests=%d cursor=%d", out, len(e.src.requests), changeCursor(t, e))
	}
	if p := report(out, "2026-10-17"); p.GetStatus() != "exported" || p.GetRowCount() != 120 {
		t.Fatalf("p = %v", p)
	}
	// 日志里有一条写入：只读这个标的这段时间，一条新增、一条值变化。
	e.src.rows[115].Values[1] = 999.0 // BTC 01:55
	e.src.rows = append(e.src.rows, rows("BTC", day.Add(5*time.Hour), 1, 7)...)
	e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{{SubjectID: "BTC", Start: day.Add(115 * time.Minute), End: day.Add(5 * time.Hour)}})
	e.now = e.now.Add(time.Minute)
	out = e.run(2000, nil, nil)
	if out.RowsAdded != 1 || out.RowsChanged != 1 || len(e.src.requests) != 1 || strings.Join(e.src.requests[0].Subjects, ",") != "BTC" || changeCursor(t, e) != before+1 {
		t.Fatalf("out=%+v requests=%+v", out, e.src.requests)
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
	if p == nil || p.GetStatus() != "exported" || out.Exported != 1 || p.GetFilePath() != "crypto/collect/kline-1m/2026/20261016.csv" || p.GetFileSha256() == "" ||
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
	if _, err := os.Stat(filepath.Join(e.root, "crypto", "collect", "kline-1m", "2026", "20261016.csv")); err != nil {
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
	orig := readFile(t, e.root, "crypto/collect/kline-1m/2026/20261010.csv")
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
	if changeCursor(t, e) != 0 {
		t.Fatal("补拉区间不能推进位点")
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

func TestRangeIsReadPartitionByPartition(t *testing.T) {
	e := newEnv(t)
	start := time.Date(2026, 10, 14, 22, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", start, 60*30, 1) // 跨 14 日晚到 16 日
	e.now = time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.RangeStart, a.RangeEnd, a.Retention = "range", start.Format(time.RFC3339), e.now.Format(time.RFC3339), "forever"
	}, nil)
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
	if _, _, ok, _ := e.store.ChangeCursor(context.Background(), "crypto", "ds_1m"); ok || e.src.changeCalls != 0 {
		t.Fatal("补拉区间不读变更日志、不写位点")
	}
}

func TestRetentionClampsTheRangeAndReportsWhatCannotBeRecovered(t *testing.T) {
	e := newEnv(t)
	e.src.rows = rows("BTC", e.now.Add(-30*time.Hour), 10, 1)
	out := e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.Retention = "range", "24h"
		a.RangeStart, a.RangeEnd = e.now.Add(-48*time.Hour).Format(time.RFC3339), e.now.Format(time.RFC3339)
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
	if _, err := e.runner(10).Execute(context.Background(), e.assignment(func(a *archivepb.RunAssignment) { a.Retention = "soon" }), nil, nil); err != nil && out == nil {
		t.Fatal(err)
	}
}

func TestCancellationLeavesStagedBatchesAndKeepsTheRescanProgress(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day, 60, 1) // 00:00–00:59
	backfill := func(a *archivepb.RunAssignment) { a.BackfillFrom = day.Format(time.RFC3339) }
	out := e.run(20, backfill, func(p Progress) bool { return p.RowsRead >= 40 })
	if out.Status != "canceled" || out.Progress.RowsRead != 40 {
		t.Fatalf("out = %+v", out)
	}
	if from, _, ok, _ := e.store.Rescan(context.Background(), "crypto", "ds_1m"); !ok || !from.Equal(day) {
		t.Fatalf("取消的那一段没读完，重读进度不动: %v %v", from, ok)
	}
	if p := report(out, "2026-10-17"); p == nil || p.GetRowCount() != 40 || p.GetStatus() != "open" {
		t.Fatalf("已提交的批次留在暂存区并登记: %v", p)
	}
	// 下一次运行从同一段重读，已写入的行值没变，不重复计数。
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
	e2.src.failAll = errors.New("storage unreachable")
	out := e2.run(2000, nil, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "storage unreachable") {
		t.Fatalf("out = %+v", out)
	}
	if len(e2.src.requests) != 3 {
		t.Fatalf("requests = %d, want 3 attempts", len(e2.src.requests))
	}
	if from, _, ok, _ := e2.store.Rescan(context.Background(), "crypto", "ds_1m"); !ok || !from.Equal(e2.now.Add(-168*time.Hour)) {
		t.Fatalf("失败的那一段重读进度不动: %v %v", from, ok)
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
		a.Freq, a.PartitionUnit, a.Retention = "1d", "month", "forever"
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
		"no dir name":    func(a *archivepb.RunAssignment) { a.DirName = "" },
		"no purpose":     func(a *archivepb.RunAssignment) { a.Purpose = "" },
		"unsafe dir":     func(a *archivepb.RunAssignment) { a.DirName = "../x" },
		"dot purpose":    func(a *archivepb.RunAssignment) { a.Purpose = ".state" },
		"unknown kind":   func(a *archivepb.RunAssignment) { a.Kind = "tick" },
		"import no feed": func(a *archivepb.RunAssignment) { a.Kind = "import" },
	} {
		out, err := e.runner(10).Execute(context.Background(), e.assignment(mutate), nil, nil)
		if err == nil && (out == nil || out.Status != "failed") {
			t.Fatalf("%s: 应报错或失败: out=%+v", name, out)
		}
	}
	e.src.columns = []staging.Column{{Name: "Bad-Name", Type: staging.TypeDouble}}
	if out, err := e.runner(10).Execute(context.Background(), e.assignment(nil), nil, nil); err == nil && (out == nil || out.Status != "failed") {
		t.Fatalf("非法列名必须报错: out=%+v", out)
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

func TestExportedPartitionWhoseFileWasDeletedIsExportedAgain(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(time.Hour), 30, 1)
	e.now = time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	p := report(out, "2026-10-10")
	if p == nil || p.GetStatus() != "exported" {
		t.Fatalf("out=%+v", out)
	}
	// 文件被删：行还在暂存区，下一次运行按记录的路径重新导出。
	if err := os.Remove(filepath.Join(e.root, filepath.FromSlash(p.GetFilePath()))); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, nil, nil)
	again := report(out, "2026-10-10")
	if again == nil || out.Exported != 1 {
		t.Fatalf("应重新导出: exported=%d out=%+v", out.Exported, out)
	}
	if _, err := os.Stat(filepath.Join(e.root, filepath.FromSlash(again.GetFilePath()))); err != nil {
		t.Fatalf("重新导出后文件应存在: %v", err)
	}
}

func partitionState(t *testing.T, e *env, key string) staging.PartitionState {
	t.Helper()
	p, ok, err := e.store.Partition(context.Background(), "crypto", "ds_1m", key)
	if err != nil || !ok {
		t.Fatalf("partition %s: ok=%v err=%v", key, ok, err)
	}
	return p
}

func TestLostStagingIsRebuiltFromTheManagerRegistrationWithoutLosingRows(t *testing.T) {
	e := newEnv(t)
	e.format = "" // 走生产默认的 csv.gz：读回前校验、解压读回、重新压缩导出
	day := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	e.src.rows = append(rows("BTC", day.Add(23*time.Hour), 60, 1), rows("ETH", day.Add(23*time.Hour+30*time.Minute), 30, 50)...)
	e.now = time.Date(2026, 10, 17, 0, 34, 43, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T22:00:00Z" }, nil)
	registered := report(out, "2026-10-16")
	if registered.GetStatus() != "exported" || registered.GetRowCount() != 90 || !strings.HasSuffix(registered.GetFilePath(), ".csv.gz") {
		t.Fatalf("registered = %v", registered)
	}
	lastSuccess := e.now
	orig := readFile(t, e.root, registered.GetFilePath())

	// 暂存库整个丢失；Storage 里那一天只剩部分行（例如线上已删一部分），另有一条晚到的行。
	_ = e.store.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(e.root, ".state", "staging.db"+suffix))
	}
	store, err := staging.Open(filepath.Join(e.root, ".state", "staging.db"), staging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.store = store
	e.src.rows = append(rows("BTC", day.Add(23*time.Hour+40*time.Minute), 20, 41), rows("SOL", day.Add(23*time.Hour), 1, 7)...)
	e.src.requests = nil
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, func(a *archivepb.RunAssignment) {
		a.LastSuccessAt = lastSuccess.Format(time.RFC3339)
		a.RegisteredPartitions = []*archivepb.PartitionReport{registered}
	}, nil)
	if out.Status != "succeeded" {
		t.Fatalf("out = %+v", out)
	}
	// 起点前移到登记里 exported 分区的开始。
	if !e.src.requests[0].Start.Equal(day) {
		t.Fatalf("start = %s", e.src.requests[0].Start)
	}
	p := report(out, "2026-10-16")
	if p == nil || p.GetRowCount() != 91 || p.GetSubjectCount() != 3 || p.GetExportCount() != 2 || p.GetFilePath() != registered.GetFilePath() {
		t.Fatalf("读回合并后: %v", p)
	}
	rewritten := readFile(t, e.root, p.GetFilePath())
	for _, line := range strings.Split(strings.TrimSpace(orig), "\n")[1:] {
		if !strings.Contains(rewritten, line) {
			t.Fatalf("重建后丢了原有的行 %q", line)
		}
	}
}

// 改了导出格式之后，读回用记录的路径，重新导出也写回原路径，目录里只有一份文件。
func TestReadBackUsesTheRecordedPathAfterTheExportFormatChanged(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(time.Hour), 30, 1)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	if p := report(out, "2026-10-10"); p.GetStatus() != "final" || p.GetFilePath() != "crypto/collect/kline-1m/2026/20261010.csv" {
		t.Fatalf("p = %v", p)
	}
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(2*time.Hour), 1, 9)...)
	r := e.runner(2000)
	r.Files.Format = export.FormatCSVGz
	e.now = e.now.Add(time.Hour)
	out, err := r.Execute(context.Background(), e.assignment(func(a *archivepb.RunAssignment) {
		a.Kind, a.RangeStart, a.RangeEnd = "range", "2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z"
	}), nil, nil)
	if err != nil || out.Status != "succeeded" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	p := report(out, "2026-10-10")
	if p.GetRowCount() != 31 || p.GetFilePath() != "crypto/collect/kline-1m/2026/20261010.csv" || p.GetExportCount() != 2 {
		t.Fatalf("p = %v", p)
	}
	entries, _ := os.ReadDir(filepath.Join(e.root, "crypto", "collect", "kline-1m", "2026"))
	if len(entries) != 1 {
		t.Fatalf("该分区只应有一份文件: %v", entries)
	}
}

// 读回前 sha256 与记录不一致：报错，运行失败，不改动文件。
func TestReadBackRefusesAFileWhoseSHA256Changed(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(time.Hour), 30, 1)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	path := filepath.Join(e.root, filepath.FromSlash(report(out, "2026-10-10").GetFilePath()))
	raw, _ := os.ReadFile(path)
	truncated := raw[:len(raw)-len("2026-10-10T01:29:00Z,BTC,\"\",30.0,30.5\n")]
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatal(err)
	}
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(2*time.Hour), 1, 9)...)
	e.now = e.now.Add(time.Hour)
	out = e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.RangeStart, a.RangeEnd = "range", "2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z"
	}, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "sha256") {
		t.Fatalf("out = %+v", out)
	}
	if after, _ := os.ReadFile(path); string(after) != string(truncated) {
		t.Fatal("校验失败不能改动文件")
	}
	if p := partitionState(t, e, "2026-10-10"); p.Status != staging.StatusFinal {
		t.Fatalf("分区应保持 final: %+v", p)
	}
}

// 目标文件已存在、本地却没有导出记录：拒绝导出，运行失败，不覆盖文件。
func TestExportRefusesToOverwriteAFileTheStagingDoesNotKnow(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(23*time.Hour), 60, 1)
	target := filepath.Join(e.root, "crypto", "collect", "kline-1m", "2026", "20261016.csv")
	_ = os.MkdirAll(filepath.Dir(target), 0o755)
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.now = time.Date(2026, 10, 17, 0, 34, 43, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T22:00:00Z" }, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "已存在") {
		t.Fatalf("out = %+v", out)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "old" {
		t.Fatal("不能覆盖已有文件")
	}
}

// 首次运行的回补起点早于保留期起点：[回补起点, R) 记为无法补齐，从 R 读起；截掉的不到一根 K 线跨度时不记。
func TestFirstRunBeforeTheRetentionReportsWhatCannotBeRecovered(t *testing.T) {
	e := newEnv(t)
	out := e.run(2000, func(a *archivepb.RunAssignment) {
		a.Retention = "24h"
		a.BackfillFrom = e.now.Add(-48 * time.Hour).Format(time.RFC3339)
	}, nil)
	if out.Status != "succeeded" || len(out.Unrecoverable) != 1 || out.Unrecoverable[0].GetStartTime() != e.now.Add(-48*time.Hour).Format(time.RFC3339) ||
		out.Unrecoverable[0].GetEndTime() != e.now.Add(-24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("out = %+v", out)
	}
	if !e.src.requests[0].Start.Equal(e.now.Add(-24 * time.Hour)) {
		t.Fatalf("start = %s", e.src.requests[0].Start)
	}
}

func TestRetentionCutShorterThanABarIsNotReported(t *testing.T) {
	e := newEnv(t)
	out := e.run(2000, func(a *archivepb.RunAssignment) {
		a.Retention = "24h"
		a.BackfillFrom = e.now.Add(-24*time.Hour - 30*time.Second).Format(time.RFC3339)
	}, nil)
	if out.Status != "succeeded" || len(out.Unrecoverable) != 0 {
		t.Fatalf("out = %+v", out)
	}
	if !e.src.requests[0].Start.Equal(e.now.Add(-24 * time.Hour)) {
		t.Fatalf("start = %s", e.src.requests[0].Start)
	}
}

func TestEmptyRetentionIsAnError(t *testing.T) {
	if _, _, err := parseRetention(""); err == nil {
		t.Fatal("空保留期必须报错")
	}
	if _, forever, err := parseRetention("forever"); err != nil || !forever {
		t.Fatal("forever 是永久")
	}
}
