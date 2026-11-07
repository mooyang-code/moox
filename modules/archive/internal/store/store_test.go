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
		PullEveryBars: 1, IntervalSeconds: 3600, Status: StatusEnabled, Creator: "tester",
	}
}

// mustTask 创建任务；immediate 为真时再生成一条 manual 运行（计划时刻 t0）。
func mustTask(t *testing.T, s *Store, space, dataset string, immediate bool) (Task, *Run) {
	t.Helper()
	created, err := s.CreateTask(context.Background(), task(space, dataset))
	if err != nil {
		t.Fatal(err)
	}
	if !immediate {
		return created, nil
	}
	run, err := s.CreateRun(context.Background(), space, dataset, KindManual, time.Time{}, time.Time{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return created, &run
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

func TestCreateTaskAllowsOneTaskPerDatasetAndStartsWithEmptyBoundaries(t *testing.T) {
	s := openTest(t)
	created, _ := mustTask(t, s, "crypto", "ds1", false)
	if created.Status != StatusEnabled || created.IntervalSeconds != 3600 || created.PullEveryBars != 1 || !created.LastBoundary.IsZero() || !created.LastReview.IsZero() {
		t.Fatalf("task = %+v", created)
	}
	if runs, total, _ := s.ListRuns(context.Background(), "crypto", "ds1", "", true, 10, 0); total != 0 || len(runs) != 0 {
		t.Fatalf("创建任务不生成运行: %v", runs)
	}
	if _, err := s.CreateTask(context.Background(), task("crypto", "ds1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复创建应返回 ErrConflict: %v", err)
	}
	bad := task("crypto", "ds3")
	bad.PullEveryBars = 1441
	if _, err := s.CreateTask(context.Background(), bad); err == nil {
		t.Fatal("pull_every_bars 超出 1～1440 应被表约束拒绝")
	}
	mustTask(t, s, "crypto", "ds2", false)
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

// fixedPlan 对所有任务返回同一个判定。
func fixedPlan(plan TriggerPlan) func(Task) (TriggerPlan, error) {
	return func(Task) (TriggerPlan, error) { return plan, nil }
}

func TestTriggerDueRecordsBoundariesSkipsBusyTasksAndIgnoresPausedOnes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	mustTask(t, s, "crypto", "ds2", false)
	mustTask(t, s, "crypto", "ds3", false)
	if _, err := s.UpdateTask(ctx, "crypto", "ds3", TaskUpdate{PullEveryBars: 1, Status: StatusPaused}, t0); err != nil {
		t.Fatal(err)
	}
	// 不到点：什么都不写。
	if res, problems, err := s.TriggerDue(ctx, t0, fixedPlan(TriggerPlan{})); err != nil || res != (TriggerResult{}) || len(problems) != 0 {
		t.Fatalf("不到点 = %+v %v %v", res, problems, err)
	}
	if runs, _, _ := s.ListRuns(ctx, "", "", "", true, 10, 0); len(runs) != 0 {
		t.Fatalf("不到点不写运行记录: %v", runs)
	}
	review := TriggerPlan{Kind: KindScheduled, LastBoundary: t0.Truncate(time.Minute), LastReview: t0.Truncate(time.Hour).Add(34 * time.Minute)}
	res, _, err := s.TriggerDue(ctx, t0, fixedPlan(review))
	if err != nil || res.Reviews != 2 || res.Ticks != 0 || res.Skipped != 0 {
		t.Fatalf("复核 = %+v err=%v", res, err)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if !got.LastBoundary.Equal(review.LastBoundary) || !got.LastReview.Equal(review.LastReview) {
		t.Fatalf("生成运行后记下两个边界: %+v", got)
	}
	if paused, _ := s.GetTask(ctx, "crypto", "ds3"); !paused.LastBoundary.IsZero() {
		t.Fatalf("停用的任务不触发: %+v", paused)
	}
	// 已有排队中的运行：跳过、累加次数，边界不前进。
	tick := TriggerPlan{Kind: KindTick, RangeStart: t0.Add(-3 * time.Minute), RangeEnd: t0.Add(time.Minute), LastBoundary: t0.Add(time.Minute).Truncate(time.Minute), LastReview: review.LastReview}
	res, _, err = s.TriggerDue(ctx, t0.Add(time.Minute), fixedPlan(tick))
	if err != nil || res.Skipped != 2 || res.Ticks != 0 {
		t.Fatalf("应跳过: %+v err=%v", res, err)
	}
	got, _ = s.GetTask(ctx, "crypto", "ds1")
	if got.ConsecutiveSkips != 1 || !got.LastBoundary.Equal(review.LastBoundary) {
		t.Fatalf("跳过时边界不前进: %+v", got)
	}
	// 运行结束后到点拉取：生成 tick 运行（带区间），清零跳过次数。
	for _, ds := range []string{"ds1", "ds2"} {
		runs, _, _ := s.ListRuns(ctx, "crypto", ds, "", true, 10, 0)
		if _, err := s.CancelRun(ctx, runs[0].RunID, "", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	res, _, err = s.TriggerDue(ctx, t0.Add(time.Minute), fixedPlan(tick))
	if err != nil || res.Ticks != 2 {
		t.Fatalf("到点拉取 = %+v err=%v", res, err)
	}
	got, _ = s.GetTask(ctx, "crypto", "ds1")
	if got.ConsecutiveSkips != 0 || !got.LastBoundary.Equal(tick.LastBoundary) {
		t.Fatalf("tick 后: %+v", got)
	}
	runs, _, _ := s.ListRuns(ctx, "crypto", "ds1", RunQueued, true, 10, 0)
	if len(runs) != 1 || runs[0].Kind != KindTick || !runs[0].RangeStart.Equal(tick.RangeStart) || !runs[0].RangeEnd.Equal(tick.RangeEnd) || !runs[0].ScheduledAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("tick 运行 = %+v", runs)
	}
}

func TestTriggerDueSkipsTasksWhosePlanFails(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	res, problems, err := s.TriggerDue(ctx, t0, func(Task) (TriggerPlan, error) { return TriggerPlan{}, errors.New("bad timing") })
	if err != nil || res != (TriggerResult{}) || len(problems) != 1 {
		t.Fatalf("res=%+v problems=%v err=%v", res, problems, err)
	}
	if _, problems, _ := s.TriggerDue(ctx, t0, fixedPlan(TriggerPlan{Kind: KindRange})); len(problems) != 1 {
		t.Fatalf("触发器不能生成补拉运行: %v", problems)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastBoundary.IsZero() || got.ConsecutiveSkips != 0 {
		t.Fatalf("判定出错时任务状态不变: %+v", got)
	}
}

func TestPausingCancelsQueuedRunsAndResumeClearsBoundaries(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, run := mustTask(t, s, "crypto", "ds1", true)
	if _, _, err := s.TriggerDue(ctx, t0, fixedPlan(TriggerPlan{Kind: KindScheduled, LastBoundary: t0, LastReview: t0})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRun(ctx, run.RunID, "", t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TriggerDue(ctx, t0, fixedPlan(TriggerPlan{Kind: KindScheduled, LastBoundary: t0, LastReview: t0})); err != nil {
		t.Fatal(err)
	}
	queued, _, _ := s.ListRuns(ctx, "crypto", "ds1", RunQueued, true, 10, 0)
	if len(queued) != 1 {
		t.Fatalf("queued = %v", queued)
	}
	update := TaskUpdate{PullEveryBars: 5, Status: StatusPaused}
	paused, err := s.UpdateTask(ctx, "crypto", "ds1", update, t0.Add(time.Minute))
	if err != nil || paused.PullEveryBars != 5 || !paused.LastBoundary.Equal(t0) {
		t.Fatalf("停用不清边界: %+v err=%v", paused, err)
	}
	if got, _ := s.GetRun(ctx, queued[0].RunID); got.Status != RunCanceled || got.FinishedAt.IsZero() {
		t.Fatalf("停用应取消排队中的运行: %+v", got)
	}
	update.Status = StatusEnabled
	resumed, err := s.UpdateTask(ctx, "crypto", "ds1", update, t0.Add(2*time.Minute))
	if err != nil || resumed.Status != StatusEnabled || !resumed.LastBoundary.IsZero() || !resumed.LastReview.IsZero() {
		t.Fatalf("重新启用清空两个边界: %+v err=%v", resumed, err)
	}
	// 本来就是启用的任务修改设置时不清边界。
	if _, _, err := s.TriggerDue(ctx, t0.Add(3*time.Minute), fixedPlan(TriggerPlan{Kind: KindScheduled, LastBoundary: t0, LastReview: t0})); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.UpdateTask(ctx, "crypto", "ds1", update, t0.Add(4*time.Minute)); !again.LastBoundary.Equal(t0) {
		t.Fatalf("启用 → 启用不清边界: %+v", again)
	}
	if _, err := s.UpdateTask(ctx, "crypto", "missing", update, t0); !errors.Is(err, ErrNotFound) {
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
	if _, err := s.CreateRun(ctx, "crypto", "ds2", KindTick, t0, t0, t0); err == nil {
		t.Fatal("CreateRun 不能生成 tick 运行")
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
		if _, err := s.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateRun(ctx, "crypto", name, KindManual, time.Time{}, time.Time{}, t0.Add(time.Duration(i)*time.Second)); err != nil {
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
	// 清理：失败的运行保留 90 天，成功的复核保留 30 天，成功的到点拉取保留 24 小时。
	const day = 24 * time.Hour
	now := t0.AddDate(0, 0, 45)
	if n, _ := s.CleanupRuns(ctx, now, day, 30*day, 90*day); n != 0 {
		t.Fatalf("失败的运行 45 天内不清理: %d", n)
	}
	mustTask(t, s, "crypto", "ds2", false)
	mustTask(t, s, "crypto", "ds3", false)
	complete := func(kind string, at time.Time) {
		t.Helper()
		plan := TriggerPlan{Kind: kind, LastBoundary: at, LastReview: at}
		if kind == KindTick {
			plan.RangeStart, plan.RangeEnd = at.Add(-3*time.Minute), at
		}
		if _, _, err := s.TriggerDue(ctx, at, func(task Task) (TriggerPlan, error) {
			if task.DatasetID == "ds1" {
				return TriggerPlan{}, nil
			}
			return plan, nil
		}); err != nil {
			t.Fatal(err)
		}
		claimed, _ := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w1", FreeSlots: 5, Now: at, LeaseTTL: time.Minute})
		if len(claimed) != 2 {
			t.Fatalf("claimed = %v", claimed)
		}
		for _, cl := range claimed {
			if err := s.CompleteRun(ctx, CompleteRequest{RunID: cl.Run.RunID, WorkerID: "w1", LeaseToken: cl.Run.LeaseToken, Status: RunSucceeded, Now: at.Add(time.Second)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	complete(KindScheduled, t0.Add(10*time.Minute))
	complete(KindTick, t0.Add(11*time.Minute))
	// 成功的到点拉取不推进最近成功时间。
	if got, _ := s.GetTask(ctx, "crypto", "ds2"); !got.LastSuccessAt.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("last_success_at = %s", got.LastSuccessAt)
	}
	if runs, total, _ := s.ListRuns(ctx, "crypto", "ds2", "", false, 10, 0); total != 1 || runs[0].Kind != KindScheduled {
		t.Fatalf("默认不列成功的 tick: %v", runs)
	}
	if n, err := s.CleanupRuns(ctx, t0.Add(day), day, 30*day, 90*day); err != nil || n != 0 {
		t.Fatalf("tick 成功运行 24 小时内不清理: n=%d err=%v", n, err)
	}
	if n, err := s.CleanupRuns(ctx, t0.Add(day+12*time.Minute), day, 30*day, 90*day); err != nil || n != 2 {
		t.Fatalf("成功的 tick 超过 24 小时应清理 2 条: n=%d err=%v", n, err)
	}
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 40), day, 30*day, 90*day); err != nil || n != 2 {
		t.Fatalf("成功的复核超过 30 天应清理 2 条: n=%d err=%v", n, err)
	}
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 100), day, 30*day, 90*day); err != nil || n != 1 {
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
	if runs, _, _ := s.ListRuns(ctx, "crypto", "ds2", "", true, 10, 0); len(runs) != 0 {
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
	if err := s.UpdateTaskDataset(ctx, "crypto", "ds1", "node-9", "336h", ""); err != nil {
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

func TestLastRunsStayFastWithManyTicks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, ds := range []string{"ds1", "ds2"} {
		mustTask(t, s, "crypto", ds, false)
	}
	// 每个任务 1 条复核，之后 1.5 万条成功的 tick（约 10 天的 1m 拉取）。
	_, err := s.db.ExecContext(ctx, `INSERT INTO t_archive_runs (c_run_id, c_space_id, c_dataset_id, c_kind, c_status, c_scheduled_at, c_started_at, c_finished_at)
SELECT 'review_' || c_dataset_id, c_space_id, c_dataset_id, 'scheduled', 'succeeded', '2026-10-17 00:34:25.000', '2026-10-17 00:34:30.000', '2026-10-17 00:35:00.000'
FROM t_archive_tasks`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 15000)
INSERT INTO t_archive_runs (c_run_id, c_space_id, c_dataset_id, c_kind, c_range_start, c_range_end, c_status, c_scheduled_at)
SELECT 'tick_' || t.c_dataset_id || '_' || n.i, t.c_space_id, t.c_dataset_id, 'tick', '2026-10-17 00:00:00.000', '2026-10-17 00:01:00.000', 'succeeded',
strftime('%Y-%m-%d %H:%M:%S.000', '2026-10-17 00:40:25', '+' || n.i || ' minutes')
FROM n, t_archive_tasks t`)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	last, err := s.LastRuns(ctx, "crypto")
	if err != nil || len(last) != 2 || last["crypto/ds1"].RunID != "tick_ds1_15000" {
		t.Fatalf("last = %+v err=%v", last, err)
	}
	reviews, err := s.LastReviewRuns(ctx, "")
	if err != nil || len(reviews) != 2 || reviews["crypto/ds2"].RunID != "review_ds2" {
		t.Fatalf("reviews = %+v err=%v", reviews, err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("最近运行查询耗时 %s", took)
	}
	t.Logf("两次查询耗时 %s", time.Since(started))
}

// 排队中的到点拉取让位给立即运行与补拉区间；运行中的运行仍然冲突。
func TestManualRunPreemptsQueuedTick(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	tick := TriggerPlan{Kind: KindTick, RangeStart: t0.Add(-3 * time.Minute), RangeEnd: t0, LastBoundary: t0.Truncate(time.Minute), LastReview: t0}
	if res, _, err := s.TriggerDue(ctx, t0, fixedPlan(tick)); err != nil || res.Ticks != 1 {
		t.Fatalf("tick = %+v %v", res, err)
	}
	manual, err := s.CreateRun(ctx, "crypto", "ds1", KindManual, time.Time{}, time.Time{}, t0.Add(time.Second))
	if err != nil {
		t.Fatalf("排队中的 tick 应让位: %v", err)
	}
	canceled, _, _ := s.ListRuns(ctx, "crypto", "ds1", RunCanceled, true, 10, 0)
	if len(canceled) != 1 || canceled[0].Kind != KindTick {
		t.Fatalf("被让位的 tick 应标为取消: %+v", canceled)
	}
	if _, err := s.ClaimRuns(ctx, ClaimRequest{WorkerID: "w", FreeSlots: 1, Now: t0.Add(2 * time.Second), LeaseTTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "ds1", KindRange, t0.Add(-time.Hour), t0, t0.Add(3*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("运行中的 %s 仍应冲突: %v", manual.RunID, err)
	}
}
