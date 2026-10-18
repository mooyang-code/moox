package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 17, 0, 34, 43, 0, time.UTC)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data", "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func task(space, dataset string) Task {
	return Task{
		SpaceID: space, DatasetID: dataset, Freq: "1m", PartitionUnit: "day", DataNodeID: "node-0", Retention: "168h",
		ScheduleCron: "43 4-59/5 * * * *", ScheduleTZ: "Asia/Shanghai", IntervalSeconds: 300,
		StartAt: t0, Status: StatusEnabled, NextRunAt: t0.Add(5 * time.Minute), Creator: "tester",
	}
}

func mustTask(t *testing.T, s *Store, space, dataset string, immediate bool) (Task, *Run) {
	t.Helper()
	created, run, err := s.CreateTask(context.Background(), task(space, dataset), immediate, t0)
	if err != nil {
		t.Fatal(err)
	}
	return created, run
}

func TestOpenCreatesIncrementalVacuumDatabase(t *testing.T) {
	s := openTest(t)
	var mode int
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil || mode != 2 {
		t.Fatalf("auto_vacuum = %d err=%v, want 2 (INCREMENTAL)", mode, err)
	}
	if err := s.IncrementalVacuum(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTaskIsUniquePerDatasetAndImmediateCreatesAManualRun(t *testing.T) {
	s := openTest(t)
	created, run := mustTask(t, s, "crypto", "ds1", true)
	if created.Status != StatusEnabled || created.IntervalSeconds != 300 || !created.NextRunAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("task = %+v", created)
	}
	if run == nil || run.Kind != KindManual || run.Status != RunQueued || !run.ScheduledAt.Equal(t0) {
		t.Fatalf("immediate run = %+v", run)
	}
	if _, _, err := s.CreateTask(context.Background(), task("crypto", "ds1"), false, t0); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复创建应返回 ErrConflict: %v", err)
	}
	if _, run := mustTask(t, s, "crypto", "ds2", false); run != nil {
		t.Fatal("非立即开始不应生成运行")
	}
	tasks, total, err := s.ListTasks(context.Background(), "crypto", 10, 0)
	if err != nil || total != 2 || len(tasks) != 2 || tasks[0].DatasetID != "ds1" {
		t.Fatalf("tasks=%v total=%d err=%v", tasks, total, err)
	}
	if _, _, err := s.ListTasks(context.Background(), "stockcn", 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTask(context.Background(), "crypto", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task: %v", err)
	}
}

func nextEvery5(task Task, now time.Time) (time.Time, error) {
	return now.Truncate(5 * time.Minute).Add(5*time.Minute + 43*time.Second), nil
}

func TestTriggerDueCreatesRunSkipsBusyTasksAndAdvancesPastNow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	mustTask(t, s, "crypto", "ds2", false)
	// 还没到点：什么都不触发。
	if res, _, err := s.TriggerDue(ctx, t0, nextEvery5); err != nil || res.Created != 0 || res.Skipped != 0 {
		t.Fatalf("early trigger = %+v err=%v", res, err)
	}
	due := t0.Add(5*time.Minute + time.Second)
	res, problems, err := s.TriggerDue(ctx, due, nextEvery5)
	if err != nil || len(problems) != 0 || res.Created != 2 || res.Skipped != 0 {
		t.Fatalf("trigger = %+v problems=%v err=%v", res, problems, err)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if !got.NextRunAt.After(due) {
		t.Fatalf("next_run_at 必须推进到当前时刻之后: %s", got.NextRunAt)
	}
	// 错过多个计划时刻（worker 离线一小时）：只合并成一次触发，不补。
	later := due.Add(time.Hour)
	res, _, err = s.TriggerDue(ctx, later, nextEvery5)
	if err != nil || res.Created != 0 || res.Skipped != 2 {
		t.Fatalf("上一次未结束时应跳过: %+v err=%v", res, err)
	}
	got, _ = s.GetTask(ctx, "crypto", "ds1")
	if got.ConsecutiveSkips != 1 || !got.NextRunAt.After(later) {
		t.Fatalf("task after skip = %+v", got)
	}
	runs, total, err := s.ListRuns(ctx, "crypto", "", "", 10, 0)
	if err != nil || total != 2 || len(runs) != 2 || runs[0].Kind != KindScheduled || runs[0].Status != RunQueued {
		t.Fatalf("runs=%v total=%d err=%v", runs, total, err)
	}
}

func TestTriggerDueResetsSkipsAndIgnoresPausedTasks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	mustTask(t, s, "crypto", "ds2", false)
	if _, _, err := s.UpdateTask(ctx, "crypto", "ds2", TaskUpdate{ScheduleCron: "0 0 * * * *", ScheduleTZ: "UTC", IntervalSeconds: 3600, StartAt: t0, Status: StatusPaused}, t0); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(6 * time.Minute)
	if res, _, _ := s.TriggerDue(ctx, now, nextEvery5); res.Created != 1 {
		t.Fatalf("停用的任务不触发: %+v", res)
	}
	runs, _, _ := s.ListRuns(ctx, "crypto", "ds1", "", 10, 0)
	if _, err := s.Progress(ctx, runs[0].RunID, "w1", "x", RunStats{}, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	// 完成运行后下一次到点生成新运行并清零跳过次数。
	claimed, err := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 1, Now: now, LeaseTTL: 10 * time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v err=%v", claimed, err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: claimed[0].Run.RunID, WorkerID: "w1", LeaseToken: claimed[0].Run.LeaseToken, Status: RunSucceeded, Now: now}); err != nil {
		t.Fatal(err)
	}
	next := now.Add(10 * time.Minute)
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	res, _, _ := s.TriggerDue(ctx, next, nextEvery5)
	got2, _ := s.GetTask(ctx, "crypto", "ds1")
	if res.Created != 1 || got2.ConsecutiveSkips != 0 || got.NextRunAt.After(next) {
		t.Fatalf("res=%+v task=%+v", res, got2)
	}
}

func TestTriggerDueReportsPlanProblemsAndBacksOff(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	now := t0.Add(6 * time.Minute)
	res, problems, err := s.TriggerDue(ctx, now, func(Task, time.Time) (time.Time, error) { return time.Time{}, errors.New("bad plan") })
	if err != nil || res.Created != 1 || len(problems) != 1 {
		t.Fatalf("res=%+v problems=%v err=%v", res, problems, err)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if !got.NextRunAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("计划无法解析时推迟一小时: %s", got.NextRunAt)
	}
}

func TestPausingCancelsQueuedRunsAndResumeCanRunNow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, run := mustTask(t, s, "crypto", "ds1", true)
	update := TaskUpdate{ScheduleCron: "0 0 * * * *", ScheduleTZ: "UTC", IntervalSeconds: 3600, StartAt: t0, Status: StatusPaused}
	if _, _, err := s.UpdateTask(ctx, "crypto", "ds1", update, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRun(ctx, run.RunID)
	if got.Status != RunCanceled || got.FinishedAt.IsZero() {
		t.Fatalf("停用应取消排队中的运行: %+v", got)
	}
	update.Status, update.RunNow, update.NextRunAt = StatusEnabled, true, t0.Add(time.Hour)
	_, resumed, err := s.UpdateTask(ctx, "crypto", "ds1", update, t0.Add(2*time.Minute))
	if err != nil || resumed == nil || resumed.Kind != KindManual {
		t.Fatalf("重新启用并立即运行: %v %v", resumed, err)
	}
	if _, _, err := s.UpdateTask(ctx, "crypto", "missing", update, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestCreateRunRejectsConcurrentRunsAndUnknownTasks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	run, err := s.CreateRun(ctx, "crypto", "ds1", KindRange, t0.Add(-time.Hour), t0, t0)
	if err != nil || run.Kind != KindRange || !run.RangeStart.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("range run = %+v err=%v", run, err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "ds1", KindManual, time.Time{}, time.Time{}, t0); !errors.Is(err, ErrConflict) {
		t.Fatalf("已有排队中的运行: %v", err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "nope", KindManual, time.Time{}, time.Time{}, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown task: %v", err)
	}
}

func TestClaimRunsOrdersByScheduleAndHonorsSlotsAndDataNodeLimits(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i, name := range []string{"a", "b", "c"} {
		tk := task("crypto", name)
		tk.DataNodeID = "node-0"
		if name == "c" {
			tk.DataNodeID = "node-1"
		}
		if _, _, err := s.CreateTask(ctx, tk, true, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// 每个 DataNode 最多 1 个：a(node-0) 与 c(node-1) 被领取，b 被 node-0 的上限挡住。
	claimed, err := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 3, MaxPerDataNode: 1, Now: t0, LeaseTTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 || claimed[0].Task.DatasetID != "a" || claimed[1].Task.DatasetID != "c" {
		t.Fatalf("claimed = %+v", claimed)
	}
	for _, c := range claimed {
		if c.Run.Status != RunRunning || c.Run.LeaseToken == "" || !c.Run.LeaseUntil.Equal(t0.Add(10*time.Minute)) || c.Run.WorkerID != "w1" {
			t.Fatalf("run = %+v", c.Run)
		}
	}
	// worker 自己报告的占用也算：node-1 已占 1，剩下的 b 在 node-0 上被 a 占住，仍领不到。
	again, err := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 1, MaxPerDataNode: 1, BusyByDataNode: map[string]int{"node-0": 1}, Now: t0, LeaseTTL: time.Minute})
	if err != nil || len(again) != 0 {
		t.Fatalf("again = %+v err=%v", again, err)
	}
	// 没有空闲槽就什么都不领。
	if none, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 0, Now: t0, LeaseTTL: time.Minute}); len(none) != 0 {
		t.Fatalf("none = %+v", none)
	}
	last, err := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 5, MaxPerDataNode: 2, Now: t0, LeaseTTL: time.Minute})
	if err != nil || len(last) != 1 || last[0].Task.DatasetID != "b" {
		t.Fatalf("last = %+v err=%v", last, err)
	}
}

func finishedPartition(key string, status string) Partition {
	start := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	p := Partition{
		PartitionKey: key, RangeStart: start, RangeEnd: start.AddDate(0, 0, 1), Status: status, RowCount: 100, SubjectCount: 2,
		MinDataTime: start, MaxDataTime: start.Add(23 * time.Hour), LastChangedAt: start.Add(25 * time.Hour),
	}
	if status != PartitionOpen {
		p.FilePath, p.FileBytes, p.FileSHA256 = "crypto/ds1/"+key+".csv.gz", 1234, "abc"
		p.Columns, p.Preview, p.ExportCount, p.ExportedAt = []string{"open", "close"}, "open,close\n1,2", 1, start.Add(26*time.Hour)
	}
	if status == PartitionFinal {
		p.FinalizedAt = start.Add(100 * time.Hour)
	}
	return p
}

func TestClaimRunsOnlyGivesAPartitionedTaskToItsWorker(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	claimed, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 1, Now: t0, LeaseTTL: time.Minute})
	if len(claimed) != 1 {
		t.Fatal("w1 应能领取")
	}
	c := claimed[0]
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0,
		Partitions: []Partition{finishedPartition("2026-10-16", PartitionExported)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "ds1", KindManual, time.Time{}, time.Time{}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if other, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w2", FreeSlots: 1, Now: t0, LeaseTTL: time.Minute}); len(other) != 0 {
		t.Fatalf("分区登记在 w1 的任务不能派给 w2: %+v", other)
	}
	if mine, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 1, Now: t0, LeaseTTL: time.Minute}); len(mine) != 1 {
		t.Fatal("w1 仍可领取")
	}
}

func TestCompleteRunRegistersPartitionsAndAdvancesTaskState(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0)
	final := t0.Add(-72 * time.Hour)
	err := s.CompleteRun(ctx, CompleteRequest{
		RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(time.Minute),
		Stats:         RunStats{SubjectsTotal: 2, SubjectsDone: 2, RequestCount: 3, BytesRead: 10, RowsRead: 20, RowsAdded: 18, RowsChanged: 2, PartitionsExported: 1, PartitionsFinalized: 1},
		Unrecoverable: []Unrecoverable{{SubjectID: "BTC", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}},
		Partitions:    []Partition{finishedPartition("2026-10-16", PartitionExported), finishedPartition("2026-10-14", PartitionFinal), finishedPartition("2026-10-17", PartitionOpen)},
		FinalUntil:    final,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := s.GetRun(ctx, c.Run.RunID)
	if run.Status != RunSucceeded || run.Stats.RowsAdded != 18 || run.Stats.PartitionsExported != 1 || len(run.Unrecoverable) != 1 || !run.LeaseUntil.IsZero() {
		t.Fatalf("run = %+v", run)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if !got.LastSuccessAt.Equal(c.Run.StartedAt) || !got.FinalUntil.Equal(final) {
		t.Fatalf("task = %+v", got)
	}
	// 定稿边界只前进不回退。
	c2, _ := s.CreateRun(ctx, "crypto", "ds1", KindManual, time.Time{}, time.Time{}, t0.Add(time.Hour))
	claimed := mustClaim(t, s, "w1", t0.Add(time.Hour))
	if claimed.Run.RunID != c2.RunID {
		t.Fatal("unexpected run")
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c2.RunID, WorkerID: "w1", LeaseToken: claimed.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(2 * time.Hour), FinalUntil: final.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.FinalUntil.Equal(final) {
		t.Fatalf("final_until 回退了: %s", got.FinalUntil)
	}
	parts, total, err := s.ListPartitions(ctx, "crypto", "ds1", "", "", 10, 0)
	if err != nil || total != 3 || parts[0].PartitionKey != "2026-10-17" {
		t.Fatalf("parts=%v total=%d err=%v", parts, total, err)
	}
	exported, err := s.GetPartition(ctx, "crypto", "ds1", "2026-10-16")
	if err != nil || exported.FileSHA256 != "abc" || exported.ExportCount != 1 || len(exported.Columns) != 2 || exported.WorkerID != "w1" || exported.LastRunID != c.Run.RunID {
		t.Fatalf("partition = %+v err=%v", exported, err)
	}
	ranged, _, _ := s.ListPartitions(ctx, "crypto", "ds1", "2026-10-15", "2026-10-16", 10, 0)
	if len(ranged) != 1 || ranged[0].PartitionKey != "2026-10-16" {
		t.Fatalf("范围过滤: %+v", ranged)
	}
	sum, err := s.PartitionSummaries(ctx, "crypto")
	if err != nil || sum["crypto/ds1"].Partitions != 3 || sum["crypto/ds1"].Files != 2 || sum["crypto/ds1"].Rows != 300 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
}

func mustClaim(t *testing.T, s *Store, worker string, now time.Time) Claimed {
	t.Helper()
	claimed, err := s.ClaimRuns(context.Background(), ClaimRequest{WorkerID: worker, FreeSlots: 1, Now: now, LeaseTTL: 10 * time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v err=%v", claimed, err)
	}
	return claimed[0]
}

func TestCompleteRunRejectsWrongLeaseOrFinishedRunsAndBadPartitions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0)
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: "wrong", Status: RunSucceeded, Now: t0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong token: %v", err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w2", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong worker: %v", err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: "weird", Now: t0}); err == nil {
		t.Fatal("非法状态必须报错")
	}
	bad := finishedPartition("2026-10-16", PartitionExported)
	bad.FilePath = ""
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0, Partitions: []Partition{bad}}); err == nil {
		t.Fatal("exported 分区没有文件路径必须报错")
	}
	// 失败的事务不能留下半个结果：运行仍在 running。
	if run, _ := s.GetRun(ctx, c.Run.RunID); run.Status != RunRunning {
		t.Fatalf("事务应整体回滚: %+v", run)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunFailed, Message: "boom", Now: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("已结束的运行不能再完成: %v", err)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastSuccessAt.IsZero() {
		t.Fatal("失败的运行不能更新最近成功时间")
	}
}

func TestCompletedPartitionsAreOwnedByOneWorkerAndReopenKeepsTheFile(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0)
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0,
		Partitions: []Partition{finishedPartition("2026-10-14", PartitionFinal)}}); err != nil {
		t.Fatal(err)
	}
	// 晚到的行把已定稿的分区重新打开：状态回到 open，文件信息保留，定稿时间清空。
	if _, err := s.CreateRun(ctx, "crypto", "ds1", KindRange, t0.Add(-time.Hour), t0, t0); err != nil {
		t.Fatal(err)
	}
	c2 := mustClaim(t, s, "w1", t0)
	reopened := finishedPartition("2026-10-14", PartitionOpen)
	reopened.RowCount = 101
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c2.Run.RunID, WorkerID: "w1", LeaseToken: c2.Run.LeaseToken, Status: RunSucceeded, Now: t0, Partitions: []Partition{reopened}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPartition(ctx, "crypto", "ds1", "2026-10-14")
	if got.Status != PartitionOpen || got.RowCount != 101 || got.FilePath == "" || got.FileSHA256 != "abc" || !got.FinalizedAt.IsZero() || got.ExportCount != 1 {
		t.Fatalf("reopened = %+v", got)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastSuccessAt.Equal(c.Run.StartedAt) {
		t.Fatalf("补拉区间不推进最近成功时间: %s", got.LastSuccessAt)
	}
	// 另一个 worker 上报同一个分区必须被拒绝。
	if _, err := s.CreateRun(ctx, "crypto", "ds1", KindRange, t0.Add(-time.Hour), t0, t0); err != nil {
		t.Fatal(err)
	}
	c3 := mustClaim(t, s, "w1", t0)
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		p := finishedPartition("2026-10-14", PartitionExported)
		p.SpaceID, p.DatasetID, p.WorkerID = "crypto", "ds1", "w2"
		return upsertPartition(ctx, tx, p)
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("不同 worker 上报同一分区: %v", err)
	}
	_ = c3
}

func TestProgressRenewsLeaseAndSignalsCancellation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0)
	canceled, err := s.Progress(ctx, c.Run.RunID, "w1", c.Run.LeaseToken, RunStats{SubjectsTotal: 5, SubjectsDone: 2, RequestCount: 7, BytesRead: 99, RowsRead: 12}, t0.Add(5*time.Minute), 10*time.Minute)
	if err != nil || canceled {
		t.Fatalf("canceled=%v err=%v", canceled, err)
	}
	run, _ := s.GetRun(ctx, c.Run.RunID)
	if !run.LeaseUntil.Equal(t0.Add(15*time.Minute)) || run.Stats.SubjectsDone != 2 || run.Stats.RequestCount != 7 {
		t.Fatalf("run = %+v", run)
	}
	if wrong, _ := s.Progress(ctx, c.Run.RunID, "w1", "bad", RunStats{}, t0, time.Minute); !wrong {
		t.Fatal("租约不对应视为取消")
	}
	if _, err := s.Progress(ctx, "nope", "w1", "x", RunStats{}, t0, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
	cancelledRun, err := s.CancelRun(ctx, c.Run.RunID, "crypto", t0.Add(6*time.Minute))
	if err != nil || cancelledRun.Status != RunCanceled {
		t.Fatalf("cancel = %+v err=%v", cancelledRun, err)
	}
	if gone, _ := s.Progress(ctx, c.Run.RunID, "w1", c.Run.LeaseToken, RunStats{}, t0, time.Minute); !gone {
		t.Fatal("取消后 worker 应得知")
	}
	ids, err := s.RunningRunIDsCanceled(ctx, "w1", []string{c.Run.RunID, "ghost"})
	if err != nil || len(ids) != 2 {
		t.Fatalf("canceled ids = %v err=%v", ids, err)
	}
	// 取消后的运行仍接受 worker 的收尾统计，状态保持 canceled，已导出的分区照常登记。
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunFailed, Now: t0.Add(7 * time.Minute),
		Stats: RunStats{RowsRead: 50}, Partitions: []Partition{finishedPartition("2026-10-16", PartitionExported)}}); err != nil {
		t.Fatal(err)
	}
	run, _ = s.GetRun(ctx, c.Run.RunID)
	if run.Status != RunCanceled || run.Stats.RowsRead != 50 {
		t.Fatalf("run = %+v", run)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastSuccessAt.IsZero() {
		t.Fatal("被取消的运行不更新最近成功时间")
	}
	if _, err := s.CancelRun(ctx, c.Run.RunID, "crypto", t0); !errors.Is(err, ErrConflict) {
		t.Fatalf("已结束的运行不能再取消: %v", err)
	}
	if _, err := s.CancelRun(ctx, "nope", "", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestCancelRunRespectsSpaceScope(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, run := mustTask(t, s, "crypto", "ds1", true)
	if _, err := s.CancelRun(ctx, run.RunID, "stockcn", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("其他空间不能取消: %v", err)
	}
	got, err := s.CancelRun(ctx, run.RunID, "crypto", t0)
	if err != nil || got.Status != RunCanceled || got.FinishedAt.IsZero() {
		t.Fatalf("cancel queued = %+v err=%v", got, err)
	}
}

func TestExpireLeasesFailsLostRunsAndCleanupHonorsRetention(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0)
	if n, err := s.ExpireLeases(ctx, t0.Add(9*time.Minute)); err != nil || n != 0 {
		t.Fatalf("租约未过期: n=%d err=%v", n, err)
	}
	if n, err := s.ExpireLeases(ctx, t0.Add(11*time.Minute)); err != nil || n != 1 {
		t.Fatalf("租约过期: n=%d err=%v", n, err)
	}
	run, _ := s.GetRun(ctx, c.Run.RunID)
	if run.Status != RunFailed || run.Message != "worker 失联" {
		t.Fatalf("run = %+v", run)
	}
	// 清理：失败的运行保留 90 天，成功的定时运行保留 30 天。
	now := t0.AddDate(0, 0, 45)
	if n, _ := s.CleanupRuns(ctx, now, 30*24*time.Hour, 90*24*time.Hour); n != 0 {
		t.Fatalf("失败的运行 45 天内不清理: %d", n)
	}
	sched, _ := s.CreateRun(ctx, "crypto", "ds1", KindManual, time.Time{}, time.Time{}, t0)
	_ = sched
	mustTask(t, s, "crypto", "ds2", false)
	if _, _, err := s.TriggerDue(ctx, t0.Add(10*time.Minute), nextEvery5); err != nil {
		t.Fatal(err)
	}
	runs, _, _ := s.ListRuns(ctx, "crypto", "ds2", "", 10, 0)
	if len(runs) != 1 || runs[0].Kind != KindScheduled {
		t.Fatalf("runs = %+v", runs)
	}
	claimed, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 5, Now: t0.Add(10 * time.Minute), LeaseTTL: time.Minute})
	for _, cl := range claimed {
		status := RunSucceeded
		if cl.Task.DatasetID == "ds1" {
			status = RunFailed
		}
		if err := s.CompleteRun(ctx, CompleteRequest{RunID: cl.Run.RunID, WorkerID: "w1", LeaseToken: cl.Run.LeaseToken, Status: status, Now: t0.Add(11 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 40), 30*24*time.Hour, 90*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("成功的定时运行超过 30 天应清理 1 条: n=%d err=%v", n, err)
	}
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 100), 30*24*time.Hour, 90*24*time.Hour); err != nil || n < 2 {
		t.Fatalf("失败的运行超过 90 天应清理: n=%d err=%v", n, err)
	}
}

func TestDeleteTaskRefusesTasksWithPartitionsOrRunningRuns(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0)
	if _, err := s.DeleteTask(ctx, "crypto", "ds1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("运行中的任务不能删除: %v", err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0,
		Partitions: []Partition{finishedPartition("2026-10-16", PartitionExported)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTask(ctx, "crypto", "ds1"); !errors.Is(err, ErrHasPartitions) {
		t.Fatalf("有分区的任务不能删除: %v", err)
	}
	mustTask(t, s, "crypto", "ds2", true)
	if _, err := s.DeleteTask(ctx, "crypto", "ds2"); err != nil {
		t.Fatal(err)
	}
	if runs, _, _ := s.ListRuns(ctx, "crypto", "ds2", "", 10, 0); len(runs) != 0 {
		t.Fatalf("删除任务应级联删除运行: %v", runs)
	}
	if _, err := s.DeleteTask(ctx, "crypto", "ds2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("again: %v", err)
	}
}

func TestHeartbeatKeepsASingleActiveWorkerAndDetectsDuplicateInstances(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	w1 := Worker{WorkerID: "w1", InstanceID: "i1", Version: "v1", RootDir: "/data", DiskTotalBytes: 100, DiskFreeBytes: 50, RunningRunIDs: []string{"r1"}}
	res, err := s.Heartbeat(ctx, w1, t0, 45*time.Second)
	if err != nil || !res.Active || res.Conflict {
		t.Fatalf("first heartbeat = %+v err=%v", res, err)
	}
	// 另一个 worker 在 w1 租约内心跳：不活动。
	res, err = s.Heartbeat(ctx, Worker{WorkerID: "w2", InstanceID: "j1"}, t0.Add(10*time.Second), 45*time.Second)
	if err != nil || res.Active || res.Conflict {
		t.Fatalf("w2 = %+v err=%v", res, err)
	}
	// 同一 worker_id 的第二个实例：冲突。
	res, err = s.Heartbeat(ctx, Worker{WorkerID: "w1", InstanceID: "i2"}, t0.Add(10*time.Second), 45*time.Second)
	if err != nil || !res.Conflict || res.Active {
		t.Fatalf("duplicate = %+v err=%v", res, err)
	}
	if ok, _ := s.IsActiveWorker(ctx, "w1", "i1", t0.Add(20*time.Second)); !ok {
		t.Fatal("w1/i1 应是活动实例")
	}
	if ok, _ := s.IsActiveWorker(ctx, "w1", "i2", t0.Add(20*time.Second)); ok {
		t.Fatal("w1/i2 不是活动实例")
	}
	// 45 秒无心跳后 w2 可以接管；w1 恢复后发现自己不再活动。
	later := t0.Add(60 * time.Second)
	res, err = s.Heartbeat(ctx, Worker{WorkerID: "w2", InstanceID: "j1"}, later, 45*time.Second)
	if err != nil || !res.Active {
		t.Fatalf("takeover = %+v err=%v", res, err)
	}
	res, _ = s.Heartbeat(ctx, w1, later.Add(time.Second), 45*time.Second)
	if res.Active {
		t.Fatal("w1 在 w2 持有租约期间不应活动")
	}
	if ok, _ := s.IsActiveWorker(ctx, "w2", "j1", later.Add(time.Second)); !ok {
		t.Fatal("w2 持有租约")
	}
	// 同一 worker_id 的旧实例租约过期后，新实例可以接管。
	res, err = s.Heartbeat(ctx, Worker{WorkerID: "w2", InstanceID: "j2"}, later.Add(2*time.Minute), 45*time.Second)
	if err != nil || !res.Active || res.Conflict {
		t.Fatalf("instance takeover = %+v err=%v", res, err)
	}
	workers, err := s.ListWorkers(ctx)
	if err != nil || len(workers) != 2 || workers[0].WorkerID != "w2" || !workers[0].Active(later.Add(2*time.Minute)) {
		t.Fatalf("workers = %+v err=%v", workers, err)
	}
	if workers[1].RunningRunIDs == nil || len(workers[1].RunningRunIDs) != 1 || workers[1].RootDir != "/data" {
		t.Fatalf("w1 状态没有保存: %+v", workers[1])
	}
}

func TestUpdateTaskDatasetRefreshesRetentionAndDataNode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	if err := s.UpdateTaskDataset(ctx, "crypto", "ds1", "node-9", "336h"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if got.DataNodeID != "node-9" || got.Retention != "336h" {
		t.Fatalf("task = %+v", got)
	}
	runsByTask, err := s.LastRuns(ctx, "crypto")
	if err != nil || len(runsByTask) != 0 {
		t.Fatalf("runs = %v err=%v", runsByTask, err)
	}
	if n, err := s.CountQueued(ctx); err != nil || n != 0 {
		t.Fatalf("queued = %d err=%v", n, err)
	}
}

func TestResumeRunNowDoesNotCreateAParallelRun(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0) // 运行中
	update := TaskUpdate{ScheduleCron: "0 0 * * * *", ScheduleTZ: "UTC", IntervalSeconds: 3600, StartAt: t0, Status: StatusPaused}
	if _, _, err := s.UpdateTask(ctx, "crypto", "ds1", update, t0); err != nil {
		t.Fatal(err)
	}
	update.Status, update.RunNow, update.NextRunAt = StatusEnabled, true, t0.Add(time.Hour)
	_, run, err := s.UpdateTask(ctx, "crypto", "ds1", update, t0.Add(time.Minute))
	if err != nil || run != nil {
		t.Fatalf("已有运行中的运行，不应再生成: run=%v err=%v", run, err)
	}
	if active, _ := s.ListActiveRuns(ctx, "crypto"); len(active) != 1 || active[0].RunID != c.Run.RunID {
		t.Fatalf("active = %+v", active)
	}
}

func TestCanceledRunsAcceptOneCompletionAndNeverAdvanceTheTask(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, "w1", t0)
	if _, err := s.CancelRun(ctx, c.Run.RunID, "crypto", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	complete := CompleteRequest{RunID: c.Run.RunID, WorkerID: "w1", LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(2 * time.Minute),
		FinalUntil: t0.Add(-time.Hour), Partitions: []Partition{finishedPartition("2026-10-16", PartitionExported)}}
	if err := s.CompleteRun(ctx, complete); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if !got.LastSuccessAt.IsZero() || !got.FinalUntil.IsZero() {
		t.Fatalf("取消的运行不能推进任务状态: %+v", got)
	}
	if run, _ := s.GetRun(ctx, c.Run.RunID); run.Status != RunCanceled || run.LeaseToken != "" {
		t.Fatalf("run = %+v", run)
	}
	// 重复上报（响应丢失后的重试）必须被拒绝，不能覆盖之后的登记。
	if err := s.CompleteRun(ctx, complete); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复完成应返回 ErrConflict: %v", err)
	}
}

func TestLastSuccessOnlyMovesForward(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	first := mustClaim(t, s, "w1", t0.Add(10*time.Minute))
	if _, err := s.CreateRun(ctx, "crypto", "ds1", KindRange, t0.Add(-time.Hour), t0, t0); err == nil {
		t.Fatal("第一条运行还没结束，不能再生成")
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: first.Run.RunID, WorkerID: "w1", LeaseToken: first.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(11 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// 一个开始更早的运行迟到完成（例如租约过期后被重新派发的旧令牌）不能让最近成功时间回退。
	older, _ := s.CreateRun(ctx, "crypto", "ds1", KindManual, time.Time{}, time.Time{}, t0)
	claimed := mustClaim(t, s, "w1", t0.Add(5*time.Minute))
	if claimed.Run.RunID != older.RunID {
		t.Fatal("unexpected run")
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: older.RunID, WorkerID: "w1", LeaseToken: claimed.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(12 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastSuccessAt.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("last_success_at 回退了: %s", got.LastSuccessAt)
	}
}

func TestClaimRunsRechecksTheInstanceInsideTheTransaction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	if _, err := s.Heartbeat(ctx, Worker{WorkerID: "w1", InstanceID: "i1"}, t0, 45*time.Second); err != nil {
		t.Fatal(err)
	}
	// 租约过期后 w2 接管；w1 的旧实例还带着已过检查的状态来领取，必须领不到。
	later := t0.Add(time.Minute)
	if res, _ := s.Heartbeat(ctx, Worker{WorkerID: "w2", InstanceID: "j1"}, later, 45*time.Second); !res.Active {
		t.Fatal("w2 应接管")
	}
	if claimed, err := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", InstanceID: "i1", FreeSlots: 1, Now: later, LeaseTTL: time.Minute}); err != nil || len(claimed) != 0 {
		t.Fatalf("旧实例不应再领取: %v err=%v", claimed, err)
	}
	if claimed, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", InstanceID: "stale", FreeSlots: 1, Now: t0, LeaseTTL: time.Minute}); len(claimed) != 0 {
		t.Fatalf("实例 ID 不匹配不应领取: %v", claimed)
	}
	if claimed, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "ghost", InstanceID: "x", FreeSlots: 1, Now: t0, LeaseTTL: time.Minute}); len(claimed) != 0 {
		t.Fatalf("没有登记的 worker 不应领取: %v", claimed)
	}
	if claimed, err := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w2", InstanceID: "j1", FreeSlots: 1, Now: later, LeaseTTL: time.Minute}); err != nil || len(claimed) != 1 {
		t.Fatalf("活动实例可以领取: %v err=%v", claimed, err)
	}
}
