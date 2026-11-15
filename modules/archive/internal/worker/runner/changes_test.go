package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

// finalizedDay 回补 2026-10-10 的 30 行并一路走到定稿（运行时刻 10-14 01:00），返回这一天的零点。
func finalizedDay(t *testing.T, e *env) time.Time {
	t.Helper()
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(time.Hour), 30, 1)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	if p := report(out, "2026-10-10"); p == nil || p.GetStatus() != "final" || out.Message != "" {
		t.Fatalf("应已定稿: %v out=%+v", p, out)
	}
	return day
}

func changeCursor(t *testing.T, e *env) uint64 {
	t.Helper()
	storeID, sequence, ok, err := e.store.ChangeCursor(context.Background(), "crypto", "ds_1m")
	if err != nil || !ok || storeID != "store" {
		t.Fatalf("change cursor = %q/%d ok=%v err=%v", storeID, sequence, ok, err)
	}
	return sequence
}

// lateRequests 返回起点早于 before 的读取请求。
func lateRequests(e *env, before time.Time) []source.Range {
	var out []source.Range
	for _, req := range e.src.requests {
		if req.Start.Before(before) {
			out = append(out, req)
		}
	}
	return out
}

func TestLateWritesOutsideTheLookbackWindowAreReReadFromTheChangeLog(t *testing.T) {
	e := newEnv(t)
	// 首次运行之前日志里已有的条目不重放：首次运行取日志头部当位点。
	e.src.changeLog = [][2]time.Time{{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}
	day := finalizedDay(t, e)
	if got := changeCursor(t, e); got != 1 {
		t.Fatalf("首次运行应把位点记在日志头部: %d", got)
	}
	// 窗口之外的两次晚到写入（同一分区）：一条值变化、一条新行；外加一次窗口之内的写入（不该单独重读）。
	e.src.rows[5].Values[1] = 999.0 // 01:05
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(2*time.Hour), 1, 9)...)
	e.now = e.now.Add(time.Hour) // 10-14 02:00，窗口起点 10-14 00:44
	inWindow := e.now.Add(-5 * time.Minute)
	e.src.changeLog = append(e.src.changeLog,
		[2]time.Time{day.Add(2 * time.Hour), day.Add(2 * time.Hour)},
		[2]time.Time{day.Add(65 * time.Minute), day.Add(65 * time.Minute)},
		[2]time.Time{inWindow, inWindow})
	e.src.requests = nil
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || out.RowsAdded != 1 || out.RowsChanged != 1 || !strings.Contains(out.Message, "重读了 1 个") {
		t.Fatalf("out = %+v", out)
	}
	windowStart := time.Date(2026, 10, 14, 0, 44, 0, 0, time.UTC)
	late := lateRequests(e, windowStart)
	if len(late) != 1 || !late[0].Start.Equal(day.Add(65*time.Minute)) || !late[0].End.Equal(day.Add(2*time.Hour).Add(time.Nanosecond)) {
		t.Fatalf("应只重读分区内的改动范围 [01:05, 02:00]: %+v", late)
	}
	// 已定稿的分区读回原文件、写入晚到的行、重新导出并再次定稿：原有 30 行 + 1 行。
	p := report(out, "2026-10-10")
	if p == nil || p.GetStatus() != "final" || p.GetExportCount() != 2 || p.GetRowCount() != 31 {
		t.Fatalf("partition = %v", p)
	}
	csv := readFile(t, e.root, p.GetFilePath())
	if strings.Count(csv, "\n") != 1+31 || !strings.Contains(csv, ",999") || !strings.Contains(csv, "2026-10-10T02:00:00Z,ETH,") {
		t.Fatalf("csv = %q", csv)
	}
	if got := changeCursor(t, e); got != 4 {
		t.Fatalf("成功后位点应推进到已扫描的位置: %d", got)
	}
	// 没有新变更时不再重读。
	e.now = e.now.Add(time.Hour)
	e.src.requests = nil
	out = e.run(2000, nil, nil)
	if late := lateRequests(e, windowStart); len(late) != 0 || out.Message != "" || out.Exported != 0 {
		t.Fatalf("不应再重读: %+v out=%+v", late, out)
	}
}

func TestLateSpanAcrossPartitionsIsClippedToTheArchiveStartAndTheWindow(t *testing.T) {
	e := newEnv(t)
	day := finalizedDay(t, e)
	// 一段横跨归档起点之前到窗口之内的改动（历史导入）：最早的分区是 10-10（晚于保留期截止 10-07 02:00），窗口起点 10-14 00:44。
	e.src.changeLog = append(e.src.changeLog, [2]time.Time{day.AddDate(0, 0, -30), day.AddDate(0, 0, 30)})
	e.now = e.now.Add(time.Hour)
	e.src.requests = nil
	out := e.run(2000, nil, nil)
	windowStart := time.Date(2026, 10, 14, 0, 44, 0, 0, time.UTC)
	late := lateRequests(e, windowStart)
	// 10-10 到 10-13 各一天，再加 10-14 00:00 到窗口起点。
	if out.Status != "succeeded" || len(late) != 5 || !late[0].Start.Equal(day) ||
		!late[0].End.Equal(day.AddDate(0, 0, 1)) || !late[4].End.Equal(windowStart) {
		t.Fatalf("out=%+v late=%+v", out, late)
	}
}

func TestChangeLogGapFallsBackToTheWindowAndRebasesTheCursor(t *testing.T) {
	e := newEnv(t)
	day := finalizedDay(t, e)
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(2*time.Hour), 1, 9)...)
	e.src.changeLog = append(e.src.changeLog, [2]time.Time{day.Add(2 * time.Hour), day.Add(2 * time.Hour)}, [2]time.Time{day, day})
	e.src.changeTrimmed = 1 // 位点 0 已被清理
	e.now = e.now.Add(time.Hour)
	e.src.requests = nil
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || out.RowsAdded != 0 || !strings.Contains(out.Message, "断档") {
		t.Fatalf("out = %+v", out)
	}
	if late := lateRequests(e, time.Date(2026, 10, 14, 0, 44, 0, 0, time.UTC)); len(late) != 0 {
		t.Fatalf("断档时只按回看窗口读取: %+v", late)
	}
	if got := changeCursor(t, e); got != 2 {
		t.Fatalf("断档后位点应重新记在日志头部: %d", got)
	}
}

func TestChangeLogFailureKeepsTheCursorAndOnlySuccessfulReviewsAdvanceIt(t *testing.T) {
	e := newEnv(t)
	day := finalizedDay(t, e)
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(2*time.Hour), 1, 9)...)
	e.src.changeLog = append(e.src.changeLog, [2]time.Time{day.Add(2 * time.Hour), day.Add(2 * time.Hour)})
	// 到点拉取与补拉区间不读变更日志。
	e.src.changeCalls = 0
	e.now = e.now.Add(time.Hour)
	e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.RangeStart, a.RangeEnd = "tick", "2026-10-14T01:50:00Z", "2026-10-14T02:00:00Z"
	}, nil)
	if e.src.changeCalls != 0 || changeCursor(t, e) != 0 {
		t.Fatalf("到点拉取不应读变更日志: calls=%d", e.src.changeCalls)
	}
	// 读变更日志失败：运行照常成功，只按窗口读取，位点不动。
	e.src.changesErr = errors.New("no permission")
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || out.RowsAdded != 0 || !strings.Contains(out.Message, "读变更日志失败") || changeCursor(t, e) != 0 {
		t.Fatalf("out = %+v", out)
	}
	e.src.changesErr = nil
	// 运行失败（窗口读取出错）：位点不动，下次还会重读。
	e.now = e.now.Add(time.Hour)
	e.src.failFrom = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	out = e.run(2000, nil, nil)
	if out.Status != "failed" || changeCursor(t, e) != 0 {
		t.Fatalf("out = %+v", out)
	}
	e.src.failFrom = time.Time{}
	e.src.requests = nil
	out = e.run(2000, nil, nil)
	if out.Status != "succeeded" || changeCursor(t, e) != 1 || len(lateRequests(e, day.AddDate(0, 0, 1))) != 1 {
		t.Fatalf("out = %+v requests=%+v", out, e.src.requests)
	}
	if p := report(out, "2026-10-10"); p == nil || p.GetRowCount() != 31 || p.GetStatus() != "final" {
		t.Fatalf("partition = %v", p)
	}
}

func TestLateWriteBeforeTheArchiveStartIsNotReRead(t *testing.T) {
	e := newEnv(t)
	day := finalizedDay(t, e) // 归档从 10-10 开始
	// Storage 在 10-08 有 60 行（保留期内、归档起点之前），其中 11 行是晚到写入：不能生成只含改动段的残缺分区。
	before := day.AddDate(0, 0, -2)
	e.src.rows = append(e.src.rows, rows("BTC", before.Add(time.Hour), 60, 1)...)
	e.src.changeLog = append(e.src.changeLog, [2]time.Time{before.Add(time.Hour), before.Add(70 * time.Minute)})
	e.now = e.now.Add(time.Hour)
	e.src.requests = nil
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || out.RowsAdded != 0 || out.Exported != 0 || out.Message != "" || len(lateRequests(e, day)) != 0 {
		t.Fatalf("归档起点之前的改动不应重读: out=%+v requests=%+v", out, e.src.requests)
	}
	if _, ok, err := e.store.Partition(context.Background(), "crypto", "ds_1m", "2026-10-08"); err != nil || ok {
		t.Fatalf("不应新建分区 2026-10-08: ok=%v err=%v", ok, err)
	}
	if got := changeCursor(t, e); got != 1 {
		t.Fatalf("位点照常推进: %d", got)
	}
}
