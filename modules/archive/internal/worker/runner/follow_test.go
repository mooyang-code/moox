package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

func changeCursor(t *testing.T, e *env) uint64 {
	t.Helper()
	storeID, sequence, ok, err := e.store.ChangeCursor(context.Background(), "crypto", "ds_1m")
	if err != nil || !ok || storeID != "store" {
		t.Fatalf("change cursor = %q/%d ok=%v err=%v", storeID, sequence, ok, err)
	}
	return sequence
}

func rescanState(t *testing.T, e *env) (time.Time, time.Time, bool) {
	t.Helper()
	from, until, ok, err := e.store.Rescan(context.Background(), "crypto", "ds_1m")
	if err != nil {
		t.Fatal(err)
	}
	return from, until, ok
}

func span(subject string, start, end time.Time) source.ChangeSpan {
	return source.ChangeSpan{SubjectID: subject, Start: start, End: end}
}

// firstFollow 做一次首次运行：回补起点 10-16 23:00，BTC 在 10-17 00:00 起有 60 行，读完后只有 10-17 一个分区。
func firstFollow(t *testing.T, e *env) {
	t.Helper()
	e.src.rows = rows("BTC", time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), 60, 1)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T23:00:00Z" }, nil)
	if out.Status != "succeeded" {
		t.Fatalf("首次运行: %+v", out)
	}
	if _, _, ok := rescanState(t, e); ok {
		t.Fatal("首次运行应读完整段重读")
	}
	e.src.requests = nil
}

// minuteWindow 模拟秒窗口：每分钟只在 [第 25 秒, 第 55 秒) 允许读取。
func minuteWindow(t time.Time) time.Time {
	minute := t.Truncate(time.Minute)
	switch offset := t.Sub(minute); {
	case offset < schedule.AllowFromSecond*time.Second:
		return minute.Add(schedule.AllowFromSecond * time.Second)
	case offset >= schedule.AllowUntilSecond*time.Second:
		return minute.Add(time.Minute + schedule.AllowFromSecond*time.Second)
	}
	return t
}

func TestHourQuietWindowOnlyExportsAndManualOnlyRegistersTheRescan(t *testing.T) {
	e := newEnv(t)
	firstFollow(t, e)
	cursor, calls := changeCursor(t, e), e.src.changeCalls
	e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", time.Date(2026, 10, 17, 0, 5, 0, 0, time.UTC), time.Date(2026, 10, 17, 0, 5, 0, 0, time.UTC))})
	// 整点窗口：下一个允许读取的时刻要等 5 分钟以上。10-17 的分区已到导出时间，照样导出。
	e.pace.allowed = func(t time.Time) time.Time { return t.Add(5 * time.Minute) }
	e.now = time.Date(2026, 10, 18, 0, 59, 0, 0, time.UTC)
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || !strings.Contains(out.Message, "整点窗口内只做导出与定稿") || out.Exported != 1 {
		t.Fatalf("out = %+v", out)
	}
	if e.src.changeCalls != calls || len(e.src.requests) != 0 || changeCursor(t, e) != cursor {
		t.Fatalf("整点窗口内不读日志也不读行: changeCalls %d→%d requests=%d", calls, e.src.changeCalls, len(e.src.requests))
	}
	// manual：只登记重读进度（取头部位点不等窗口），交给之后的 follow。
	waits, heads := e.pace.waits, e.src.headCalls
	out = e.run(2000, func(a *archivepb.RunAssignment) { a.Kind = "manual" }, nil)
	from, until, ok := rescanState(t, e)
	if out.Status != "succeeded" || !ok || !from.Equal(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)) || !until.Equal(e.now) {
		t.Fatalf("manual 应登记 [最早分区, 现在): %v %v %v out=%+v", from, until, ok, out)
	}
	if e.pace.waits != waits || e.src.headCalls != heads+1 || len(e.src.requests) != 0 || !strings.Contains(out.Message, schedule.RescanProgressLabel) {
		t.Fatalf("manual 不等窗口、不读行: waits %d→%d heads %d→%d requests=%d msg=%q", waits, e.pace.waits, heads, e.src.headCalls, len(e.src.requests), out.Message)
	}
	if changeCursor(t, e) != uint64(len(e.src.changeLog)) {
		t.Fatal("manual 把位点重设到日志头部")
	}
}

func TestRescanStopsAtASegmentBoundaryWhenTheBudgetRunsOut(t *testing.T) {
	backfill := func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T00:00:00Z" }
	t.Run("超过 60 秒", func(t *testing.T) {
		e := newEnv(t)
		e.src.onRead = func(source.Range) { e.now = e.now.Add(25 * time.Second) }
		e.run(2000, backfill, nil)
		if len(e.src.requests) != 3 {
			t.Fatalf("每段 25 秒，第三段结束时超过 60 秒: %d 段", len(e.src.requests))
		}
		if from, _, ok := rescanState(t, e); !ok || !from.Equal(time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)) {
			t.Fatalf("rescan_from = %v %v", from, ok)
		}
		// 下一轮从 rescan_from 接着读。
		e.src.requests = nil
		e.run(2000, nil, nil)
		if !e.src.requests[0].Start.Equal(time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)) {
			t.Fatalf("接着重读的起点 = %s", e.src.requests[0].Start)
		}
	})
	t.Run("过了第 55 秒", func(t *testing.T) {
		e := newEnv(t)
		e.now = time.Date(2026, 10, 17, 1, 10, 50, 0, time.UTC)
		e.pace.allowed = minuteWindow
		e.src.onRead = func(source.Range) { e.now = e.now.Add(3 * time.Second) }
		e.run(2000, backfill, nil)
		if len(e.src.requests) != 2 {
			t.Fatalf("第 56 秒时不再开始下一段: %d 段", len(e.src.requests))
		}
		if from, _, ok := rescanState(t, e); !ok || !from.Equal(time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)) {
			t.Fatalf("rescan_from = %v %v", from, ok)
		}
	})
}

// 重读进行中只导出、定稿结束不晚于 rescan_from 的分区：同一个日分区只导出一次，不会定稿成半天数据。
func TestRescanExportsOnlyPartitionsBeforeTheProgress(t *testing.T) {
	e := newEnv(t)
	d0 := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	for d := 0; d < 4; d++ {
		e.src.rows = append(e.src.rows, rows("BTC", d0.AddDate(0, 0, d).Add(time.Hour), 30, float64(d))...)
	}
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	e.src.onRead = func(source.Range) { e.now = e.now.Add(25 * time.Second) }
	mutate := func(a *archivepb.RunAssignment) { a.BackfillFrom = d0.Format(time.RFC3339) }
	rounds := 0
	for ; rounds < 100; rounds++ {
		out := e.run(2000, mutate, nil)
		if out.Status != "succeeded" {
			t.Fatalf("第 %d 轮: %+v", rounds, out)
		}
		from, _, ok := rescanState(t, e)
		parts, _ := e.store.Partitions(context.Background(), "crypto", "ds_1m")
		for _, p := range parts {
			if ok && p.End.After(from) && (p.FilePath != "" || p.Status != staging.StatusOpen) {
				t.Fatalf("第 %d 轮：重读还没读完的分区 %s 不应导出或定稿: %+v（rescan_from %s）", rounds, p.Key, p, from)
			}
		}
		if ok && !strings.Contains(out.Message, schedule.RescanProgressLabel) {
			t.Fatalf("重读进行中运行消息应带进度: %q", out.Message)
		}
		if !ok {
			break
		}
	}
	if rounds < 10 {
		t.Fatalf("预算应把重读分到多轮: %d 轮", rounds)
	}
	for key, status := range map[string]string{"2026-10-10": staging.StatusFinal, "2026-10-11": staging.StatusExported, "2026-10-12": staging.StatusExported} {
		p := partitionState(t, e, key)
		if p.ExportCount != 1 || p.Status != status {
			t.Fatalf("%s 应只导出一次: %+v", key, p)
		}
	}
	if p := partitionState(t, e, "2026-10-10"); p.RowCount != 30 {
		t.Fatalf("10-10 定稿时应是整天的行: %+v", p)
	}
}

func TestRescanResumesFromTheProgressAfterAFailure(t *testing.T) {
	e := newEnv(t)
	d0 := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", d0, 60*25, 1) // 到 10-17 01:00
	e.src.failFrom = d0.Add(5 * time.Hour)
	r := e.runner(2000)
	r.ReadAttempts = 1
	out, err := r.Execute(context.Background(), e.assignment(func(a *archivepb.RunAssignment) { a.BackfillFrom = d0.Format(time.RFC3339) }), nil, nil)
	if err != nil || out.Status != "failed" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if from, _, ok := rescanState(t, e); !ok || !from.Equal(e.src.failFrom) {
		t.Fatalf("rescan_from = %v %v", from, ok)
	}
	e.src.failFrom, e.src.requests = time.Time{}, nil
	e.now = e.now.Add(time.Minute)
	out = e.run(2000, nil, nil)
	if out.Status != "succeeded" || !e.src.requests[0].Start.Equal(d0.Add(5*time.Hour)) || out.Progress.RowsRead != 60*20 {
		t.Fatalf("接着重读: start=%s out=%+v", e.src.requests[0].Start, out)
	}
	if _, _, ok := rescanState(t, e); ok {
		t.Fatal("读完后应清空")
	}
	if p, q := report(out, "2026-10-16"), report(out, "2026-10-17"); p.GetRowCount() != 1440 || q.GetRowCount() != 60 {
		t.Fatalf("不漏不重: %v %v", p, q)
	}
}

// 运行开始之后、取头部之前写入、数据时间晚于运行开始的行：重读最后一段不设上界，照样读到。
func TestRowsWrittenBeforeTheHeadWithLaterDataTimesAreArchived(t *testing.T) {
	e := newEnv(t)
	late := e.now.Add(30 * time.Second).Truncate(time.Minute).Add(time.Minute)
	e.src.onHead = func() { e.src.rows = append(e.src.rows, rows("BTC", late, 1, 7)...) }
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-17T00:00:00Z" }, nil)
	if out.Status != "succeeded" || out.RowsAdded != 1 || report(out, "2026-10-17").GetMaxDataTime() != late.Format(time.RFC3339) {
		t.Fatalf("out = %+v", out)
	}
}

func TestChangeLogGapRebasesAndReportsWhatCannotBeRecovered(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 10, 17, 0, 1, 0, 0, time.UTC)
	e.src.changeLog = [][]source.ChangeSpan{{span("BTC", at, at)}, {span("BTC", at, at)}}
	firstFollow(t, e)
	e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", at, at)}, []source.ChangeSpan{span("BTC", at, at)})
	e.src.changeTrimmed = 3 // 位点 2 早于日志保留起点
	e.now = e.now.Add(time.Minute)
	lastSuccess := e.now.Add(-10 * 24 * time.Hour)
	out := e.run(2000, func(a *archivepb.RunAssignment) {
		a.BackfillFrom = "2026-10-16T23:00:00Z"
		a.LastSuccessAt = lastSuccess.UnixMilli()
	}, nil)
	if out.Status != "succeeded" || !strings.Contains(out.Message, "断档") {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Unrecoverable) != 1 || out.Unrecoverable[0].GetStartTime() != lastSuccess.Format(time.RFC3339) ||
		out.Unrecoverable[0].GetEndTime() != e.now.Add(-168*time.Hour).Format(time.RFC3339) {
		t.Fatalf("无法补齐 = %v", out.Unrecoverable)
	}
	if changeCursor(t, e) != 4 || len(e.src.requests) == 0 || !e.src.requests[0].Start.Equal(time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("重建基线后从 min(最早分区, 回补起点) 整段重读: cursor=%d requests=%v", changeCursor(t, e), e.src.requests)
	}
	if _, _, ok := rescanState(t, e); ok {
		t.Fatal("预算够时本轮读完")
	}
}

func TestChangesAreReadPerSubjectUnlessTooMany(t *testing.T) {
	e := newEnv(t)
	e.now = time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	forever := func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-19T00:00:00Z"; a.Retention = "forever" }
	e.run(2000, forever, nil)
	e.src.requests = nil
	many := func(at time.Time) []source.ChangeSpan {
		var out []source.ChangeSpan
		for i := 0; i < 150; i++ {
			out = append(out, span(fmt.Sprintf("S%03d", i), at, at))
		}
		return out
	}
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	mixedDay := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	e.src.changeLog = append(e.src.changeLog,
		[]source.ChangeSpan{span("BTC", day, day.AddDate(0, 0, 30).Add(-time.Minute))},                  // 一个标的补 30 天
		many(time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)),                                            // 实时写入：150 个标的同一分钟
		append(many(mixedDay.Add(30*time.Minute)), span("BTC", mixedDay, mixedDay.Add(59*time.Minute))), // 同一分区里混在一起
	)
	e.now = e.now.Add(time.Minute)
	if out := e.run(2000, forever, nil); out.Status != "succeeded" {
		t.Fatalf("out = %+v", out)
	}
	if len(e.src.requests) != 32 {
		t.Fatalf("requests = %d, want 30 + 1 + 1", len(e.src.requests))
	}
	for i, req := range e.src.requests[:30] {
		if strings.Join(req.Subjects, ",") != "BTC" || req.Freq != "1m" || !req.Start.Equal(day.AddDate(0, 0, i)) {
			t.Fatalf("第 %d 个请求应只带 BTC: %+v", i, req)
		}
	}
	realtime, mixed := e.src.requests[30], e.src.requests[31]
	if realtime.Subjects != nil || !realtime.Start.Equal(time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)) || !realtime.End.Equal(realtime.Start.Add(time.Nanosecond)) {
		t.Fatalf("超过 100 个标的不带 selectors: %+v", realtime)
	}
	if mixed.Subjects != nil || !mixed.Start.Equal(mixedDay) || !mixed.End.Equal(mixedDay.Add(59*time.Minute+time.Nanosecond)) {
		t.Fatalf("混在一起时读全部标的的时间并集: %+v", mixed)
	}
}

func TestCursorAdvancesPageByPage(t *testing.T) {
	e := newEnv(t)
	firstFollow(t, e)
	base := changeCursor(t, e)
	for _, minute := range []int{10, 20, 30} {
		at := time.Date(2026, 10, 17, 0, minute, 0, 0, time.UTC)
		e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", at, at)})
	}
	e.src.failFrom = time.Date(2026, 10, 17, 0, 20, 0, 0, time.UTC) // 第 2 页的读取失败
	e.now = e.now.Add(time.Minute)
	if out := e.run(2000, nil, nil); out.Status != "failed" {
		t.Fatalf("out = %+v", out)
	}
	if got := changeCursor(t, e); got != base+1 {
		t.Fatalf("位点应停在第 1 页之后: %d", got)
	}
	e.src.failFrom, e.src.requests = time.Time{}, nil
	e.now = e.now.Add(time.Minute)
	if out := e.run(2000, nil, nil); out.Status != "succeeded" || !e.src.requests[0].Start.Equal(time.Date(2026, 10, 17, 0, 20, 0, 0, time.UTC)) {
		t.Fatalf("下一轮从第 2 页接着读: %+v %v", out, e.src.requests)
	}
	if got := changeCursor(t, e); got != base+3 {
		t.Fatalf("cursor = %d", got)
	}
}

func TestChangeLogBacklogIsSpreadOverRoundsWithAtLeastOnePagePerRound(t *testing.T) {
	e := newEnv(t)
	firstFollow(t, e)
	base := changeCursor(t, e)
	for i := 0; i < 10; i++ {
		at := time.Date(2026, 10, 17, 0, i, 0, 0, time.UTC)
		e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", at, at)})
	}
	e.src.onRead = func(source.Range) { e.now = e.now.Add(25 * time.Second) }
	e.run(2000, nil, nil)
	if got := changeCursor(t, e); got != base+3 {
		t.Fatalf("每页 25 秒，一轮读 3 页: %d", got)
	}
	// 领到时已过第 55 秒：第一页仍照常处理，之后不再继续。
	e.src.onRead = nil
	e.pace.allowed = minuteWindow
	e.now = time.Date(2026, 10, 17, 1, 20, 56, 0, time.UTC)
	e.run(2000, nil, nil)
	if got := changeCursor(t, e); got != base+4 {
		t.Fatalf("第一页不受预算限制: %d", got)
	}
	// 一轮先读日志，剩余预算再整段重读。
	e.pace.allowed = nil
	e.src.changeLog = e.src.changeLog[:base+5]
	rescanFrom := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	if err := e.store.SetRebase(context.Background(), "crypto", "ds_1m", "store", base+4, rescanFrom, e.now); err != nil {
		t.Fatal(err)
	}
	e.src.requests = nil
	e.run(2000, nil, nil)
	if len(e.src.requests) < 2 || strings.Join(e.src.requests[0].Subjects, ",") != "BTC" || e.src.requests[1].Subjects != nil || !e.src.requests[1].Start.Equal(rescanFrom) {
		t.Fatalf("先日志后重读: %+v", e.src.requests)
	}
	if got := changeCursor(t, e); got != base+5 {
		t.Fatalf("cursor = %d", got)
	}
}

// 先建任务（数据集还是空的）→ 实时行建出今天的分区 → 再写入更早的数据（补采、补算、导入 Storage）：
// 下界取 min(最早分区, 回补起点)，更早的数据跟随后照样进归档。
func TestHistoryWrittenAfterTheTaskIsCreatedIsArchived(t *testing.T) {
	e := newEnv(t)
	backfill := func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-11T02:00:00Z" }
	if out := e.run(2000, backfill, nil); out.Status != "succeeded" || out.RowsAdded != 0 {
		t.Fatalf("首次运行（空数据集）: %+v", out)
	}
	live := time.Date(2026, 10, 17, 1, 5, 0, 0, time.UTC)
	e.src.rows = rows("BTC", live, 1, 1)
	e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", live, live)})
	e.now = e.now.Add(time.Minute)
	if out := e.run(2000, backfill, nil); out.Status != "succeeded" || out.RowsAdded != 1 {
		t.Fatalf("实时行: %+v", out)
	}
	history := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	e.src.rows = append(e.src.rows, rows("BTC", history, 60, 5)...)
	e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", history, history.Add(59*time.Minute))})
	e.src.requests = nil
	e.now = e.now.Add(time.Minute)
	out := e.run(2000, backfill, nil)
	if out.Status != "succeeded" || out.RowsAdded != 60 || len(e.src.requests) != 1 || !e.src.requests[0].Start.Equal(history) {
		t.Fatalf("更早的数据应读进来: %+v %+v", out, e.src.requests)
	}
	if got := report(out, "2026-10-13"); got == nil || got.GetRowCount() != 60 {
		t.Fatalf("10-13 分区应导出 60 行: %+v", got)
	}
}

func TestChangesBeforeTheBackfillOrTheRetentionAreNotRead(t *testing.T) {
	t.Run("早于回补起点与最早分区", func(t *testing.T) {
		e := newEnv(t)
		firstFollow(t, e) // 回补起点 10-16 23:00，只有 10-17 一个分区
		backfill := func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T23:00:00Z" }
		e.src.rows = append(e.src.rows, rows("BTC", time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), 1, 9)...)
		e.src.changeLog = append(e.src.changeLog,
			[]source.ChangeSpan{span("BTC", time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 17, 0, 10, 0, 0, time.UTC))})
		e.now = e.now.Add(time.Minute)
		e.run(2000, backfill, nil)
		if len(e.src.requests) == 0 || !e.src.requests[0].Start.Equal(time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)) {
			t.Fatalf("只读回补起点之后的部分: %+v", e.src.requests)
		}
		if _, ok, _ := e.store.Partition(context.Background(), "crypto", "ds_1m", "2026-10-16"); ok {
			t.Fatal("不能新建只含早于回补起点改动的分区")
		}
	})
	t.Run("早于保留期", func(t *testing.T) {
		e := newEnv(t)
		old := &archivepb.PartitionReport{PartitionKey: "2026-10-05", RangeStart: "2026-10-05T00:00:00Z", RangeEnd: "2026-10-06T00:00:00Z", Status: "final",
			FilePath: "crypto/collect/kline-1m/2026/20261005.csv", FileSha256: "x", ExportedAt: rfcMillis("2026-10-06T00:20:00Z"), RowCount: 1, SubjectCount: 1}
		registered := func(a *archivepb.RunAssignment) { a.RegisteredPartitions = []*archivepb.PartitionReport{old} }
		e.run(2000, func(a *archivepb.RunAssignment) { registered(a); a.BackfillFrom = "2026-10-05T00:00:00Z" }, nil)
		retention := e.now.Add(-168 * time.Hour)
		if !e.src.requests[0].Start.Equal(retention) {
			t.Fatalf("首次重读从保留期起点读: %s", e.src.requests[0].Start)
		}
		e.src.requests = nil
		e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC))})
		e.now = e.now.Add(time.Minute)
		e.run(2000, registered, nil)
		if len(e.src.requests) != 0 {
			t.Fatalf("保留期之前的改动不读: %+v", e.src.requests)
		}
	})
}

func TestManualRebuildsTheBaselineEvenWithAValidCursor(t *testing.T) {
	e := newEnv(t)
	firstFollow(t, e)
	heads := e.src.headCalls
	e.src.changeLog = append(e.src.changeLog, []source.ChangeSpan{span("BTC", e.now, e.now)})
	e.now = e.now.Add(time.Minute)
	out := e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind = "manual"
		a.BackfillFrom = "2026-10-16T23:00:00Z"
		a.LastSuccessAt = rfcMillis("2026-09-01T00:00:00Z")
	}, nil)
	if out.Status != "succeeded" || e.src.headCalls != heads+1 || out.RowsAdded != 0 || out.Progress.RowsRead != 60 || len(out.Unrecoverable) != 0 {
		t.Fatalf("out = %+v", out)
	}
	if !e.src.requests[0].Start.Equal(time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)) || changeCursor(t, e) != uint64(len(e.src.changeLog)) {
		t.Fatalf("从 min(最早分区, 回补起点) 整段重读、位点取头部: %+v", e.src.requests[0])
	}
	if _, _, ok := rescanState(t, e); ok {
		t.Fatal("预算够时本轮读完")
	}
}
