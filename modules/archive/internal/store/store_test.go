package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/sqlitedb"
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

// task 返回一个测试任务；目录名按数据集取，同一空间里多个任务不会撞目录名。
func task(space, dataset string) Task {
	return Task{
		SpaceID: space, DatasetID: dataset, Purpose: "collect", DirName: "dir-" + strings.ReplaceAll(dataset, "_", "-"), Freq: "1m",
		BackfillFrom: t0.Add(-6 * 24 * time.Hour).Truncate(time.Second), Status: StatusEnabled,
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
	run, err := s.CreateRun(context.Background(), space, dataset, RunSpec{Kind: KindManual}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return created, &run
}

func TestOpenCreatesIncrementalVacuumDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "archive.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
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
	rec, err := dataformat.ReadFile(filepath.Join(dir, dataformat.FileName))
	if err != nil {
		t.Fatalf("应写入 archive-mgr 数据格式文件：%v", err)
	}
	if rec.Component != "archive-mgr" || rec.Parts[dataformat.PartSQLiteSchema] == "" || rec.Parts[dataformat.PartLifecycle] != "" {
		t.Fatalf("格式记录异常：%+v", rec)
	}

	// 旧表结构应在 Open 时被拒绝。
	badPath := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", badPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE t_archive_tasks (c_id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if _, err := Open(badPath); !errors.Is(err, sqlitedb.ErrIncompatibleSchema) {
		t.Fatalf("旧表结构应被拒绝：%v", err)
	}
}

func TestCreateTaskAllowsOneTaskPerDatasetAndKeepsTheBackfillStart(t *testing.T) {
	s := openTest(t)
	created, _ := mustTask(t, s, "crypto", "ds1", false)
	if created.Status != StatusEnabled || !created.BackfillFrom.Equal(task("crypto", "ds1").BackfillFrom) {
		t.Fatalf("task = %+v", created)
	}
	if runs, total, _ := s.ListRuns(context.Background(), "crypto", "ds1", "", 10, 0); total != 0 || len(runs) != 0 {
		t.Fatalf("创建任务不生成运行: %v", runs)
	}
	if _, err := s.CreateTask(context.Background(), task("crypto", "ds1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复创建应返回 ErrConflict: %v", err)
	}
	bad := task("crypto", "ds3")
	bad.BackfillFrom = time.Time{}
	if _, err := s.CreateTask(context.Background(), bad); err == nil {
		t.Fatal("没有回补起点的任务应被拒绝")
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

// 可跟随清单只返回启用、源数据集未缺失且当前无排队/运行中一次性运行的任务。
func TestListFollowableTasksReturnsOnlyEnabledIdleTasks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, ds := range []string{"idle", "busy", "importing", "disabled", "missing"} {
		mustTask(t, s, "crypto", ds, false)
	}
	if _, err := s.CreateRun(ctx, "crypto", "busy", RunSpec{Kind: KindManual}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "importing", RunSpec{Kind: KindImport}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "crypto", "disabled", TaskUpdate{Status: StatusDisabled}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskSourceMissing(ctx, "crypto", "missing", true); err != nil {
		t.Fatal(err)
	}
	followable, err := s.ListFollowableTasks(ctx)
	if err != nil || len(followable) != 1 || followable[0].DatasetID != "idle" {
		t.Fatalf("followable = %+v err=%v", followable, err)
	}
}

func TestDisablingCancelsQueuedRunsAndResumeKeepsTheTaskState(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, queued := mustTask(t, s, "crypto", "ds1", true)
	disabled, err := s.UpdateTask(ctx, "crypto", "ds1", TaskUpdate{Status: StatusDisabled}, t0.Add(time.Minute))
	if err != nil || disabled.Status != StatusDisabled {
		t.Fatalf("停用: %+v err=%v", disabled, err)
	}
	if got, _ := s.GetRun(ctx, queued.RunID); got.Status != RunCancelled || got.FinishedAt.IsZero() {
		t.Fatalf("停用应取消排队中的运行: %+v", got)
	}
	if tasks, _ := s.ListFollowableTasks(ctx); len(tasks) != 0 {
		t.Fatalf("停用的任务不进入跟随清单: %+v", tasks)
	}
	resumed, err := s.UpdateTask(ctx, "crypto", "ds1", TaskUpdate{Status: StatusEnabled}, t0.Add(2*time.Minute))
	if err != nil || resumed.Status != StatusEnabled || !resumed.BackfillFrom.Equal(disabled.BackfillFrom) {
		t.Fatalf("重新启用: %+v err=%v", resumed, err)
	}
	if tasks, _ := s.ListFollowableTasks(ctx); len(tasks) != 1 {
		t.Fatalf("重新启用后恢复进入跟随清单: %+v", tasks)
	}
	if _, err := s.UpdateTask(ctx, "crypto", "missing", TaskUpdate{Status: StatusEnabled}, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestCreateRunRejectsConcurrentRunsAndUnknownTasks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	run, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindRange, RangeStart: t0.Add(-time.Hour), RangeEnd: t0}, t0)
	if err != nil || run.Kind != KindRange || !run.RangeStart.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("range run = %+v err=%v", run, err)
	}
	for _, kind := range []string{KindManual, KindImport} {
		_, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: kind}, t0)
		var conflict *ConflictError
		if !errors.Is(err, ErrConflict) || !errors.As(err, &conflict) || conflict.Blocking.RunID != run.RunID || conflict.Blocking.Kind != KindRange {
			t.Fatalf("%s 应被排队中的补拉挡住并给出挡住它的运行: %v", kind, err)
		}
	}
	if _, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: "follow"}, t0); err == nil {
		t.Fatal("CreateRun 不能生成 follow 运行")
	}
	if _, err := s.CreateRun(ctx, "crypto", "nope", RunSpec{Kind: KindManual}, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown task: %v", err)
	}
	// 运行中的补拉同样挡住。
	claimed := mustClaim(t, s, t0)
	if _, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindImport}, t0); !errors.Is(err, ErrConflict) {
		t.Fatalf("运行中的 %s 应挡住导入: %v", claimed.Run.Kind, err)
	}
}

func TestReportFollowUpdatesTaskStateVerifiesTaskIDAndBlocksRegression(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	created, _ := mustTask(t, s, "crypto", "ds1", false)
	final := t0.Add(-72 * time.Hour)
	rescanFrom := t0.Add(-24 * time.Hour)
	rescanUntil := t0

	// 1. 成功上报：更新跟随状态、重读进度、定稿边界、分区与无法补齐区间。
	err := s.ReportFollow(ctx, FollowReport{
		SpaceID: "crypto", DatasetID: "ds1", TaskID: created.ID, Status: RunSucceeded,
		StartedAt: t0, FinishedAt: t0.Add(10 * time.Second), Message: "导出 1 个分区",
		Partitions:    []Partition{finishedPartition("2026-10-16", PartitionExported)},
		Unrecoverable: []Unrecoverable{{SubjectID: "BTC", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}},
		FinalUntil:    final, RescanFrom: rescanFrom, RescanUntil: rescanUntil,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if !got.LastSuccessAt.Equal(t0) || !got.LastFollowAt.Equal(t0.Add(10*time.Second)) || got.LastFollowError != "" ||
		got.LastFollowMessage != "导出 1 个分区" || !got.FinalUntil.Equal(final) ||
		!got.RescanFrom.Equal(rescanFrom) || !got.RescanUntil.Equal(rescanUntil) || !got.IsRescanning() || len(got.Unrecoverable) != 1 {
		t.Fatalf("task = %+v", got)
	}

	// 2. 失败上报：写 last_follow_error、不推进 last_success_at、不清空重读进度。
	err = s.ReportFollow(ctx, FollowReport{
		SpaceID: "crypto", DatasetID: "ds1", TaskID: created.ID, Status: RunFailed,
		StartedAt: t0.Add(time.Minute), FinishedAt: t0.Add(time.Minute + 5*time.Second), Message: "storage timeout",
		RescanFrom: rescanFrom, RescanUntil: rescanUntil,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(ctx, "crypto", "ds1")
	if !got.LastSuccessAt.Equal(t0) || got.LastFollowError != "storage timeout" || !got.IsRescanning() {
		t.Fatalf("失败后 task = %+v", got)
	}

	// 3. 再次成功且重读完成：清空 last_follow_error 与 rescan 进度。
	err = s.ReportFollow(ctx, FollowReport{
		SpaceID: "crypto", DatasetID: "ds1", TaskID: created.ID, Status: RunSucceeded,
		StartedAt: t0.Add(2 * time.Minute), FinishedAt: t0.Add(2*time.Minute + 5*time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(ctx, "crypto", "ds1")
	if !got.LastSuccessAt.Equal(t0.Add(2*time.Minute)) || got.LastFollowError != "" || got.IsRescanning() {
		t.Fatalf("成功后 task = %+v", got)
	}

	// 4. task_id 不匹配（任务已删除重建）：返回 ErrConflict；任务不存在返回 ErrNotFound。
	if err := s.ReportFollow(ctx, FollowReport{
		SpaceID: "crypto", DatasetID: "ds1", TaskID: created.ID + 1, Status: RunSucceeded, StartedAt: t0, FinishedAt: t0,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("task_id 不匹配应返回 ErrConflict: %v", err)
	}
	if err := s.ReportFollow(ctx, FollowReport{
		SpaceID: "crypto", DatasetID: "nope", TaskID: 1, Status: RunSucceeded, StartedAt: t0, FinishedAt: t0,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的任务应返回 ErrNotFound: %v", err)
	}

	// 5. 分区倒退被拒并记下 blocked_reason。
	shrunk := finishedPartition("2026-10-16", PartitionExported)
	shrunk.RowCount = 1
	if err := s.ReportFollow(ctx, FollowReport{
		SpaceID: "crypto", DatasetID: "ds1", TaskID: created.ID, Status: RunSucceeded,
		StartedAt: t0.Add(3 * time.Minute), FinishedAt: t0.Add(3*time.Minute + time.Second),
		Partitions: []Partition{shrunk},
	}); !errors.Is(err, ErrPartitionRegression) {
		t.Fatalf("分区倒退应返回 ErrPartitionRegression: %v", err)
	}
	got, _ = s.GetTask(ctx, "crypto", "ds1")
	if got.BlockedReason == "" || got.LastFollowError == "" {
		t.Fatalf("倒退应记录 blocked_reason 与 last_follow_error: %+v", got)
	}
}

func TestClaimRunsOrdersByScheduleAndHonorsSlotsAndStorageLimit(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i, name := range []string{"a", "b", "c"} {
		if _, err := s.CreateTask(ctx, task("crypto", name)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateRun(ctx, "crypto", name, RunSpec{Kind: KindManual}, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// 读 Storage 的运行最多 2 个：按计划时刻领取 a 与 b，c 被上限挡住。
	claimed, err := s.ClaimRuns(ctx, ClaimRequest{FreeSlots: 3, MaxStorageRuns: 2, Now: t0, LeaseTTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 || claimed[0].Task.DatasetID != "a" || claimed[1].Task.DatasetID != "b" {
		t.Fatalf("claimed = %+v", claimed)
	}
	for _, c := range claimed {
		if c.Run.Status != RunRunning || c.Run.LeaseToken == "" || !c.Run.LeaseUntil.Equal(t0.Add(10*time.Minute)) {
			t.Fatalf("run = %+v", c.Run)
		}
	}
	// worker 自己报告的占用也算：已在运行 2 个，c 仍领不到。
	again, err := s.ClaimRuns(ctx, ClaimRequest{FreeSlots: 1, MaxStorageRuns: 2, BusyStorageRuns: 2, Now: t0, LeaseTTL: time.Minute})
	if err != nil || len(again) != 0 {
		t.Fatalf("again = %+v err=%v", again, err)
	}
	// 没有空闲槽就什么都不领。
	if none, _ := s.ClaimRuns(ctx, ClaimRequest{FreeSlots: 0, Now: t0, LeaseTTL: time.Minute}); len(none) != 0 {
		t.Fatalf("none = %+v", none)
	}
	last, err := s.ClaimRuns(ctx, ClaimRequest{FreeSlots: 5, MaxStorageRuns: 3, BusyStorageRuns: 2, Now: t0, LeaseTTL: time.Minute})
	if err != nil || len(last) != 1 || last[0].Task.DatasetID != "c" {
		t.Fatalf("last = %+v err=%v", last, err)
	}
}

// 导入不读 Storage：读 Storage 的名额已满时照样领取，也不占名额。
func TestClaimRunsLetsImportBypassStorageLimit(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, name := range []string{"busy", "imp", "after"} {
		if _, err := s.CreateTask(ctx, task("crypto", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateRun(ctx, "crypto", "busy", RunSpec{Kind: KindManual}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "imp", RunSpec{Kind: KindImport}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// 读 Storage 的名额已被 worker 占满（上限 1）：manual 领不到，import 照样领取。
	claimed, err := s.ClaimRuns(ctx, ClaimRequest{FreeSlots: 3, MaxStorageRuns: 1, BusyStorageRuns: 1, Now: t0, LeaseTTL: time.Minute})
	if err != nil || len(claimed) != 1 || claimed[0].Run.Kind != KindImport {
		t.Fatalf("claimed = %+v err=%v", claimed, err)
	}
}

// 同一次领取里，排在前面的 import 不计入读 Storage 的占用：上限 1 时后面的普通运行仍能领到。
func TestClaimRunsImportDoesNotConsumeStorageSlotWithinOneClaim(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, name := range []string{"imp", "plain"} {
		if _, err := s.CreateTask(ctx, task("crypto", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateRun(ctx, "crypto", "imp", RunSpec{Kind: KindImport}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "plain", RunSpec{Kind: KindManual}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimRuns(ctx, ClaimRequest{FreeSlots: 3, MaxStorageRuns: 1, Now: t0, LeaseTTL: time.Minute})
	if err != nil || len(claimed) != 2 || claimed[0].Run.Kind != KindImport || claimed[1].Task.DatasetID != "plain" {
		t.Fatalf("claimed = %+v err=%v", claimed, err)
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

func TestCompleteRunRegistersPartitionsAndAdvancesTaskState(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	final := t0.Add(-72 * time.Hour)
	err := s.CompleteRun(ctx, CompleteRequest{
		RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(time.Minute),
		Stats:         RunStats{SubjectsDone: 2, RequestCount: 3, BytesRead: 10, RowsRead: 20, RowsAdded: 18, RowsChanged: 2, PartitionsExported: 1, PartitionsFinalized: 1},
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
	// 归档起点成功后不再清空：暂存库丢失后整段重读仍以它为下界。
	if !got.LastSuccessAt.Equal(c.Run.StartedAt) || !got.FinalUntil.Equal(final) || !got.BackfillFrom.Equal(task("crypto", "ds1").BackfillFrom) {
		t.Fatalf("task = %+v", got)
	}
	// 定稿边界只前进不回退。
	c2, _ := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindManual}, t0.Add(time.Hour))
	claimed := mustClaim(t, s, t0.Add(time.Hour))
	if claimed.Run.RunID != c2.RunID {
		t.Fatal("unexpected run")
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c2.RunID, LeaseToken: claimed.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(2 * time.Hour), FinalUntil: final.Add(-24 * time.Hour)}); err != nil {
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
	if err != nil || exported.FileSHA256 != "abc" || exported.ExportCount != 1 || len(exported.Columns) != 2 || exported.LastRunID != c.Run.RunID {
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

func mustClaim(t *testing.T, s *Store, now time.Time) Claimed {
	t.Helper()
	claimed, err := s.ClaimRuns(context.Background(), ClaimRequest{FreeSlots: 1, Now: now, LeaseTTL: 10 * time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v err=%v", claimed, err)
	}
	return claimed[0]
}

func TestCompleteRunRejectsWrongLeaseOrFinishedRunsAndBadPartitions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: "wrong", Status: RunSucceeded, Now: t0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong token: %v", err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: "weird", Now: t0}); err == nil {
		t.Fatal("非法状态必须报错")
	}
	bad := finishedPartition("2026-10-16", PartitionExported)
	bad.FilePath = ""
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0, Partitions: []Partition{bad}}); err == nil {
		t.Fatal("exported 分区没有文件路径必须报错")
	}
	// 失败的事务不能留下半个结果：运行仍在 running。
	if run, _ := s.GetRun(ctx, c.Run.RunID); run.Status != RunRunning {
		t.Fatalf("事务应整体回滚: %+v", run)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunFailed, Message: "boom", Now: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("已结束的运行不能再完成: %v", err)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastSuccessAt.IsZero() {
		t.Fatal("失败的运行不能更新最近成功时间")
	}
}

func TestReopenKeepsTheFileAndTheRemoteCopy(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	final := finishedPartition("2026-10-14", PartitionFinal)
	final.Remote = Remote{Key: "crypto/ds1/2026-10-14.abc.csv.gz", SHA256: "abc", ETag: "md5", UploadedAt: t0, VerifiedAt: t0}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0,
		Partitions: []Partition{final}}); err != nil {
		t.Fatal(err)
	}
	// 晚到的行把已定稿的分区重新打开：状态回到 open，文件信息与远端副本保留，定稿时间清空。
	if _, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindRange, RangeStart: t0.Add(-time.Hour), RangeEnd: t0}, t0); err != nil {
		t.Fatal(err)
	}
	c2 := mustClaim(t, s, t0)
	reopened := finishedPartition("2026-10-14", PartitionOpen)
	reopened.RowCount = 101
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c2.Run.RunID, LeaseToken: c2.Run.LeaseToken, Status: RunSucceeded, Now: t0, Partitions: []Partition{reopened}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPartition(ctx, "crypto", "ds1", "2026-10-14")
	if got.Status != PartitionOpen || got.RowCount != 101 || got.FilePath == "" || got.FileSHA256 != "abc" || !got.FinalizedAt.IsZero() || got.ExportCount != 1 {
		t.Fatalf("reopened = %+v", got)
	}
	if got.Remote.Key != final.Remote.Key || got.Remote.SHA256 != "abc" || !got.Remote.VerifiedAt.Equal(t0) || !got.Replicated() {
		t.Fatalf("open 不带文件时远端副本应沿用: %+v", got.Remote)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastSuccessAt.Equal(c.Run.StartedAt) {
		t.Fatalf("补拉区间不推进最近成功时间: %s", got.LastSuccessAt)
	}
}

// upsert 直接登记一个分区（绕过运行）。
func upsert(t *testing.T, s *Store, p Partition) error {
	t.Helper()
	p.SpaceID, p.DatasetID = "crypto", "ds1"
	return s.tx(context.Background(), func(tx *sql.Tx) error { return upsertPartition(context.Background(), tx, p) })
}

func TestUpsertPartitionReplacesRemoteFieldsAndKeepsTheFileSize(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	p := finishedPartition("2026-10-14", PartitionExported)
	p.Remote = Remote{Key: "k1", SHA256: "abc", ETag: "e1", UploadedAt: t0, VerifiedAt: t0, Error: ""}
	if err := upsert(t, s, p); err != nil {
		t.Fatal(err)
	}
	// 带文件的上报整体替换远端字段：远端只有旧版本、新版本上传失败。
	p2 := p
	p2.FileSHA256, p2.Remote = "def", Remote{Key: "k1", SHA256: "abc", ETag: "e1", UploadedAt: t0, VerifiedAt: t0, Error: "上传失败"}
	if err := upsert(t, s, p2); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPartition(ctx, "crypto", "ds1", "2026-10-14")
	if got.Remote.Error != "上传失败" || got.Replicated() || got.FileSHA256 != "def" {
		t.Fatalf("远端字段应整体替换: %+v", got)
	}
	// 有对象键却没有 sha256：拒绝。
	bad := p
	bad.Remote = Remote{Key: "k2"}
	if err := upsert(t, s, bad); err == nil {
		t.Fatal("remote_key 非空、remote_sha256 为空应被拒")
	}
	// 文件大小与预览兜底：本地缺文件时上报 0、没有重新导出时不带预览，路径与 sha256 不变 → 保留登记的值。
	zero := p2
	zero.FileBytes, zero.Preview = 0, ""
	if err := upsert(t, s, zero); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPartition(ctx, "crypto", "ds1", "2026-10-14"); got.FileBytes != 1234 || got.Preview != p2.Preview {
		t.Fatalf("file_bytes 与预览不应被冲成空: %d %q", got.FileBytes, got.Preview)
	}
	// sha256 变了就按上报值写。
	changed := p2
	changed.FileBytes, changed.FileSHA256 = 0, "fff"
	if err := upsert(t, s, changed); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPartition(ctx, "crypto", "ds1", "2026-10-14"); got.FileBytes != 0 {
		t.Fatalf("sha256 变了应按上报值写: %d", got.FileBytes)
	}
}

func TestTaskPartitionsAlsoSendsOldUnreplicatedPartitionsWhenTheObjectStoreIsOn(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	day := func(key string, start time.Time) Partition {
		p := finishedPartition(key, PartitionFinal)
		p.RangeStart, p.RangeEnd = start, start.AddDate(0, 0, 1)
		return p
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	replicated := day("2026-01-01", old)
	replicated.Remote = Remote{Key: "k", SHA256: replicated.FileSHA256, VerifiedAt: t0}
	stale := day("2026-01-02", old.AddDate(0, 0, 1))
	stale.Remote = Remote{Key: "k", SHA256: "older", VerifiedAt: t0}
	pending := day("2026-01-03", old.AddDate(0, 0, 2))
	openOld := finishedPartition("2026-01-04", PartitionOpen)
	openOld.RangeStart, openOld.RangeEnd = old.AddDate(0, 0, 3), old.AddDate(0, 0, 4)
	recent := day("2026-10-16", time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	recent.Remote = Remote{Key: "k", SHA256: recent.FileSHA256, VerifiedAt: t0}
	for _, p := range []Partition{replicated, stale, pending, openOld, recent} {
		if err := upsert(t, s, p); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 没启用对象存储：所有分区都“未复制”，只下发保留期以内的，下发量不随年份增长。
	if plain, err := s.TaskPartitions(ctx, "crypto", "ds1", since, false); err != nil || len(plain) != 1 || plain[0].PartitionKey != "2026-10-16" {
		t.Fatalf("未启用时只下发保留期以内的: %+v %v", plain, err)
	}
	got, err := s.TaskPartitions(ctx, "crypto", "ds1", since, true)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, p := range got {
		keys = append(keys, p.PartitionKey)
	}
	if strings.Join(keys, ",") != "2026-01-02,2026-01-03,2026-10-16" {
		t.Fatalf("下发的分区 = %v（早于保留期、已复制的与没有文件的不下发）", keys)
	}
	if got[0].Remote.SHA256 != "older" {
		t.Fatalf("下发的登记要带远端字段: %+v", got[0].Remote)
	}
}

func TestRemoteBacklogsCountsUnreplicatedPartitionsAndTheLatestError(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	a := finishedPartition("2026-10-14", PartitionFinal)
	a.ExportedAt = t0.Add(-80 * time.Hour)
	a.Remote = Remote{Error: "旧错误"}
	b := finishedPartition("2026-10-15", PartitionExported)
	b.ExportedAt = t0.Add(-30 * time.Hour)
	c := finishedPartition("2026-10-16", PartitionExported)
	c.Remote = Remote{Key: "k", SHA256: c.FileSHA256, VerifiedAt: t0}
	open := finishedPartition("2026-10-17", PartitionOpen)
	for _, p := range []Partition{a, b, c, open} {
		if err := upsert(t, s, p); err != nil {
			t.Fatal(err)
		}
	}
	// 让 b 的错误更新得更晚（c_mtime 为 Unix 毫秒）。
	if _, err := s.db.ExecContext(ctx, `UPDATE t_archive_partitions SET c_mtime = ? WHERE c_partition_key = '2026-10-14'`,
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	b.Remote.Error = "新错误"
	if err := upsert(t, s, b); err != nil {
		t.Fatal(err)
	}
	backlogs, err := s.RemoteBacklogs(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := backlogs["crypto/ds1"]
	if got.Unreplicated != 2 || !got.OldestExportedAt.Equal(a.ExportedAt) || got.OldestKey != "2026-10-14" || got.LastError != "新错误" || got.ErrorKey != "2026-10-15" {
		t.Fatalf("backlog = %+v", got)
	}
	if other, _ := s.RemoteBacklogs(ctx, "stockcn"); len(other) != 0 {
		t.Fatalf("按空间过滤: %+v", other)
	}
}

func TestCreateTaskKeepsDirNamesUniquePerSpaceAndPurpose(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	first := task("crypto", "ds1")
	first.DirName = "binance-spot-kline-1m"
	created, err := s.CreateTask(ctx, first)
	if err != nil || created.Purpose != "collect" || created.DirName != "binance-spot-kline-1m" {
		t.Fatalf("created = %+v err=%v", created, err)
	}
	second := task("crypto", "ds2")
	second.DirName = first.DirName
	var taken *DirNameTakenError
	if _, err := s.CreateTask(ctx, second); !errors.As(err, &taken) || taken.DatasetID != "ds1" || !strings.Contains(err.Error(), "已被 ds1 使用") {
		t.Fatalf("同空间同用途的目录名冲突应返回 DirNameTakenError: %v", err)
	}
	// 用途不同、空间不同都可以同名。
	second.Purpose = "factor"
	if _, err := s.CreateTask(ctx, second); err != nil {
		t.Fatal(err)
	}
	other := task("stockcn", "ds1")
	other.DirName = first.DirName
	if _, err := s.CreateTask(ctx, other); err != nil {
		t.Fatal(err)
	}
	// 数据集已有任务（即便目录名一样）仍是 ErrConflict。
	if _, err := s.CreateTask(ctx, first); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复创建应返回 ErrConflict: %v", err)
	}
	if holder, ok, err := s.DirNameHolder(ctx, "crypto", "collect", "binance-spot-kline-1m"); err != nil || !ok || holder != "ds1" {
		t.Fatalf("DirNameHolder = %q %v %v", holder, ok, err)
	}
	if _, ok, err := s.DirNameHolder(ctx, "crypto", "collect", "unused"); err != nil || ok {
		t.Fatalf("没人用的目录名: %v %v", ok, err)
	}
	// 修改任务与源缺失标记更新都不动用途与目录名。
	if _, err := s.UpdateTask(ctx, "crypto", "ds1", TaskUpdate{Status: StatusDisabled}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskSourceMissing(ctx, "crypto", "ds1", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); got.Purpose != "collect" || got.DirName != "binance-spot-kline-1m" {
		t.Fatalf("task = %+v", got)
	}
}

func TestHeartbeatStoresDiskUsageAndTheFreeSpaceFloor(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	w := Worker{WorkerID: "w1", DiskTotalBytes: 2000, DiskFreeBytes: 1600, DiskUsedBytes: 300, MinFreeBytes: 500}
	if err := s.Heartbeat(ctx, w, t0); err != nil {
		t.Fatal(err)
	}
	w.DiskFreeBytes, w.DiskUsedBytes = 400, 1500
	if err := s.Heartbeat(ctx, w, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.GetWorker(ctx)
	if !ok || got.DiskTotalBytes != 2000 || got.DiskFreeBytes != 400 || got.DiskUsedBytes != 1500 || got.MinFreeBytes != 500 {
		t.Fatalf("worker = %+v", got)
	}
}

func TestHeartbeatStoresTheObjectStoreSummaryAndRunsStoreUploads(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	summary := `{"endpoint":"cos.ap-nanjing.myqcloud.com","bucket":"b-1"}`
	if err := s.Heartbeat(ctx, Worker{WorkerID: "w1", ObjectStoreJSON: summary}, t0); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.GetWorker(ctx); !ok || got.ObjectStoreJSON != summary {
		t.Fatalf("worker = %+v", got)
	}
	if err := s.Heartbeat(ctx, Worker{WorkerID: "w1"}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetWorker(ctx); got.ObjectStoreJSON != "{}" {
		t.Fatalf("没配对象存储时存 {}: %q", got.ObjectStoreJSON)
	}
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0,
		Stats: RunStats{PartitionsUploaded: 3}}); err != nil {
		t.Fatal(err)
	}
	if run, _ := s.GetRun(ctx, c.Run.RunID); run.Stats.PartitionsUploaded != 3 {
		t.Fatalf("partitions_uploaded = %d", run.Stats.PartitionsUploaded)
	}
}

func TestProgressRenewsLeaseAndSignalsCancellation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	canceled, err := s.Progress(ctx, c.Run.RunID, c.Run.LeaseToken, RunStats{SubjectsDone: 2, RequestCount: 7, BytesRead: 99, RowsRead: 12}, t0.Add(5*time.Minute), 10*time.Minute)
	if err != nil || canceled {
		t.Fatalf("canceled=%v err=%v", canceled, err)
	}
	run, _ := s.GetRun(ctx, c.Run.RunID)
	if !run.LeaseUntil.Equal(t0.Add(15*time.Minute)) || run.Stats.SubjectsDone != 2 || run.Stats.RequestCount != 7 {
		t.Fatalf("run = %+v", run)
	}
	if wrong, _ := s.Progress(ctx, c.Run.RunID, "bad", RunStats{}, t0, time.Minute); !wrong {
		t.Fatal("租约不对应视为取消")
	}
	if _, err := s.Progress(ctx, "nope", "x", RunStats{}, t0, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
	cancelledRun, err := s.CancelRun(ctx, c.Run.RunID, "crypto", t0.Add(6*time.Minute))
	if err != nil || cancelledRun.Status != RunCancelled {
		t.Fatalf("cancel = %+v err=%v", cancelledRun, err)
	}
	if gone, _ := s.Progress(ctx, c.Run.RunID, c.Run.LeaseToken, RunStats{}, t0, time.Minute); !gone {
		t.Fatal("取消后 worker 应得知")
	}
	ids, err := s.RunningRunIDsCancelled(ctx, []string{c.Run.RunID, "ghost"})
	if err != nil || len(ids) != 2 {
		t.Fatalf("cancelled ids = %v err=%v", ids, err)
	}
	// 取消后的运行仍接受 worker 的收尾统计，状态保持 cancelled，已导出的分区照常登记。
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunFailed, Now: t0.Add(7 * time.Minute),
		Stats: RunStats{RowsRead: 50}, Partitions: []Partition{finishedPartition("2026-10-16", PartitionExported)}}); err != nil {
		t.Fatal(err)
	}
	run, _ = s.GetRun(ctx, c.Run.RunID)
	if run.Status != RunCancelled || run.Stats.RowsRead != 50 {
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
	if err != nil || got.Status != RunCancelled || got.FinishedAt.IsZero() {
		t.Fatalf("cancel queued = %+v err=%v", got, err)
	}
}

func TestExpireLeasesFailsLostRunsAndCleanupHonorsRetention(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	if n, err := s.ExpireLeases(ctx, t0.Add(9*time.Minute), false); err != nil || n != 0 {
		t.Fatalf("租约未过期: n=%d err=%v", n, err)
	}
	if n, err := s.ExpireLeases(ctx, t0.Add(11*time.Minute), false); err != nil || n != 1 {
		t.Fatalf("租约过期: n=%d err=%v", n, err)
	}
	run, _ := s.GetRun(ctx, c.Run.RunID)
	if run.Status != RunFailed || run.Message != LeaseExpiredWorkerOffline {
		t.Fatalf("run = %+v", run)
	}
	// 运行记录统一按单档 retention（90 天）清理。
	const day = 24 * time.Hour
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 45), 90*day); err != nil || n != 0 {
		t.Fatalf("45 天内不清理: %d %v", n, err)
	}
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 100), 90*day); err != nil || n != 1 {
		t.Fatalf("超过 90 天应清理: %d %v", n, err)
	}
}

func TestDeleteTaskRefusesTasksWithPartitionsOrRunningRuns(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	if _, err := s.DeleteTask(ctx, "crypto", "ds1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("运行中的任务不能删除: %v", err)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0,
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

func TestCanceledRunsAcceptOneCompletionAndNeverAdvanceTheTask(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c := mustClaim(t, s, t0)
	if _, err := s.CancelRun(ctx, c.Run.RunID, "crypto", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	complete := CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(2 * time.Minute),
		FinalUntil: t0.Add(-time.Hour), Partitions: []Partition{finishedPartition("2026-10-16", PartitionExported)}}
	if err := s.CompleteRun(ctx, complete); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, "crypto", "ds1")
	if !got.LastSuccessAt.IsZero() || !got.FinalUntil.IsZero() {
		t.Fatalf("取消的运行不能推进任务状态: %+v", got)
	}
	if run, _ := s.GetRun(ctx, c.Run.RunID); run.Status != RunCancelled || run.LeaseToken != "" {
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
	first := mustClaim(t, s, t0.Add(10*time.Minute))
	if _, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindRange, RangeStart: t0.Add(-time.Hour), RangeEnd: t0}, t0); err == nil {
		t.Fatal("第一条运行还没结束，不能再生成")
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: first.Run.RunID, LeaseToken: first.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(11 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// 一个开始更早的运行迟到完成（例如租约过期后被重新派发的旧令牌）不能让最近成功时间回退。
	older, _ := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindManual}, t0)
	claimed := mustClaim(t, s, t0.Add(5*time.Minute))
	if claimed.Run.RunID != older.RunID {
		t.Fatal("unexpected run")
	}
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: older.RunID, LeaseToken: claimed.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(12 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask(ctx, "crypto", "ds1"); !got.LastSuccessAt.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("last_success_at 回退了: %s", got.LastSuccessAt)
	}
}

func TestLastRunsAndQueuedTasks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, ds := range []string{"ds1", "ds2"} {
		mustTask(t, s, "crypto", ds, false)
	}
	if runsByTask, err := s.LastRuns(ctx, "crypto"); err != nil || len(runsByTask) != 0 {
		t.Fatalf("runs = %v err=%v", runsByTask, err)
	}
	r1, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindManual}, t0)
	if err != nil {
		t.Fatal(err)
	}
	c := mustClaim(t, s, t0)
	if err := s.CompleteRun(ctx, CompleteRequest{RunID: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Status: RunFailed, Now: t0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	r2, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindRange, RangeStart: t0.Add(-time.Hour), RangeEnd: t0}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r3, err := s.CreateRun(ctx, "crypto", "ds2", RunSpec{Kind: KindImport}, t0.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.LastRuns(ctx, "crypto")
	if err != nil || len(last) != 2 || last["crypto/ds1"].RunID != r2.RunID || last["crypto/ds2"].RunID != r3.RunID || r1.RunID == r2.RunID {
		t.Fatalf("last = %+v err=%v", last, err)
	}
	queued, err := s.QueuedTasks(ctx)
	if err != nil || len(queued) != 2 || queued[0].DatasetID != "ds1" || queued[1].DatasetID != "ds2" {
		t.Fatalf("queued = %+v err=%v", queued, err)
	}
}

// 导入运行按统一的 retention 保留。
func TestImportRunsAreKeptLikeOtherRuns(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	run, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindImport}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRun(ctx, run.RunID); err != nil || got.Kind != KindImport || got.Status != RunQueued {
		t.Fatalf("run = %+v, %v", got, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'succeeded', c_finished_at = ? WHERE c_run_id = ?`, millisOf(t0), run.RunID); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 40), 90*day); err != nil || n != 0 {
		t.Fatalf("40 天时不应清理导入运行: %d %v", n, err)
	}
	if n, err := s.CleanupRuns(ctx, t0.AddDate(0, 0, 91), 90*day); err != nil || n != 1 {
		t.Fatalf("超过 90 天应清理导入运行: %d %v", n, err)
	}
}

func TestClearUnrecoverableKeepsPartlyOverlappingRanges(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", false)
	items := `[{"start_time":"2026-10-01T00:00:00Z","end_time":"2026-10-02T00:00:00Z"},{"subject_id":"BTC","start_time":"2026-10-02T00:00:00Z","end_time":"2026-10-03T00:00:00Z"},{"start_time":"2026-10-02T12:00:00Z","end_time":"2026-10-04T00:00:00Z"}]`
	if _, err := s.db.ExecContext(ctx, `UPDATE t_archive_tasks SET c_unrecoverable_json = ?`, items); err != nil {
		t.Fatal(err)
	}
	start, end := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	removed, err := s.ClearUnrecoverable(ctx, "crypto", "ds1", start, end)
	if err != nil || len(removed) != 2 || removed[1].SubjectID != "BTC" {
		t.Fatalf("removed = %+v, %v", removed, err)
	}
	task, _ := s.GetTask(ctx, "crypto", "ds1")
	if len(task.Unrecoverable) != 1 || task.Unrecoverable[0].StartTime != "2026-10-02T12:00:00Z" {
		t.Fatalf("剩余 = %+v", task.Unrecoverable)
	}
	if removed, err := s.ClearUnrecoverable(ctx, "crypto", "ds1", start, end); err != nil || len(removed) != 0 {
		t.Fatalf("再清一次应为空: %+v %v", removed, err)
	}
	// 起止都为零值时清空全部剩余区间。
	if removed, err := s.ClearUnrecoverable(ctx, "crypto", "ds1", time.Time{}, time.Time{}); err != nil || len(removed) != 1 {
		t.Fatalf("清空全部应返回剩余 1 条: %+v %v", removed, err)
	}
	task, _ = s.GetTask(ctx, "crypto", "ds1")
	if len(task.Unrecoverable) != 0 {
		t.Fatalf("应已全部清空: %+v", task.Unrecoverable)
	}
	if _, err := s.ClearUnrecoverable(ctx, "crypto", "nope", start, end); err != ErrNotFound {
		t.Fatalf("不存在的任务: %v", err)
	}
}

// 部署里只有一个 worker：表里只留最近心跳的那一行，换了 worker_id 旧行随之删除。
func TestHeartbeatKeepsOnlyTheLatestWorker(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, ok, err := s.GetWorker(ctx); err != nil || ok {
		t.Fatalf("没有心跳时不应有 worker: ok=%v err=%v", ok, err)
	}
	w1 := Worker{WorkerID: "w1", Version: "v1", RootDir: "/data", DiskTotalBytes: 100, DiskFreeBytes: 50, RunningRunIDs: []string{"r1"}}
	if err := s.Heartbeat(ctx, w1, t0); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetWorker(ctx)
	if err != nil || !ok || got.WorkerID != "w1" || got.RootDir != "/data" || len(got.RunningRunIDs) != 1 || !got.LastHeartbeat.Equal(t0) {
		t.Fatalf("worker = %+v ok=%v err=%v", got, ok, err)
	}
	if err := s.Heartbeat(ctx, Worker{WorkerID: "w2"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_workers`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows = %d err=%v", rows, err)
	}
	if got, _, _ := s.GetWorker(ctx); got.WorkerID != "w2" || !got.LastHeartbeat.Equal(t0.Add(time.Minute)) {
		t.Fatalf("worker = %+v", got)
	}
}

func TestMergeUnrecoverableMergesOverlappingAndAdjacentIntervals(t *testing.T) {
	items := []Unrecoverable{
		// BTC: 乱序 + 重叠 + 相邻 + 被包含，应合并为一条 [10-01T00:00Z, 10-04T00:00Z) 与一条独立区间 [10-06, 10-07)
		{SubjectID: "BTC", SeriesTag: "", StartTime: "2026-10-02T00:00:00Z", EndTime: "2026-10-03T12:00:00Z"},
		{SubjectID: "BTC", SeriesTag: "", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}, // 相邻
		{SubjectID: "BTC", SeriesTag: "", StartTime: "2026-10-03T00:00:00Z", EndTime: "2026-10-04T00:00:00Z"}, // 重叠
		{SubjectID: "BTC", SeriesTag: "", StartTime: "2026-10-01T12:00:00Z", EndTime: "2026-10-02T12:00:00Z"}, // 被包含
		{SubjectID: "BTC", SeriesTag: "", StartTime: "2026-10-06T00:00:00Z", EndTime: "2026-10-07T00:00:00Z"},
		// 不同 series_tag 不合并
		{SubjectID: "BTC", SeriesTag: "raw", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"},
		// 不同 subject_id 不合并
		{SubjectID: "ETH", SeriesTag: "", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"},
		// 全局（SubjectID=""）重叠合并
		{StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"},
		{StartTime: "2026-10-01T12:00:00Z", EndTime: "2026-10-03T00:00:00Z"},
	}
	var got []Unrecoverable
	if err := json.Unmarshal([]byte(mergeUnrecoverable(items)), &got); err != nil {
		t.Fatal(err)
	}
	want := []Unrecoverable{
		{StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-03T00:00:00Z"},
		{SubjectID: "BTC", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-04T00:00:00Z"},
		{SubjectID: "BTC", StartTime: "2026-10-06T00:00:00Z", EndTime: "2026-10-07T00:00:00Z"},
		{SubjectID: "BTC", SeriesTag: "raw", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"},
		{SubjectID: "ETH", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items = %+v, want %+v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMergeUnrecoverableCapsAfterMergingAndAccumulatesAcrossRuns(t *testing.T) {
	// 归并后再截断：超过上限的相邻小时块应合并为 1 条，不会在归并前被截断。
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	adjacentCount := maxUnrecoverable + 50
	adjacent := make([]Unrecoverable, 0, adjacentCount)
	for i := range adjacentCount {
		adjacent = append(adjacent, Unrecoverable{
			SubjectID: "BTC",
			StartTime: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
			EndTime:   base.Add(time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		})
	}
	var merged []Unrecoverable
	if err := json.Unmarshal([]byte(mergeUnrecoverable(adjacent)), &merged); err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0].StartTime != "2026-01-01T00:00:00Z" || merged[0].EndTime != base.Add(time.Duration(adjacentCount)*time.Hour).Format(time.RFC3339) {
		t.Fatalf("merged = %+v", merged)
	}

	// 超过上限的互不重叠区间截断到 maxUnrecoverable。
	disjointCount := maxUnrecoverable + 20
	disjoint := make([]Unrecoverable, 0, disjointCount)
	for i := range disjointCount {
		disjoint = append(disjoint, Unrecoverable{
			SubjectID: fmt.Sprintf("S%03d", i),
			StartTime: base.Format(time.RFC3339),
			EndTime:   base.Add(time.Hour).Format(time.RFC3339),
		})
	}
	merged = nil
	if err := json.Unmarshal([]byte(mergeUnrecoverable(disjoint)), &merged); err != nil {
		t.Fatal(err)
	}
	if len(merged) != maxUnrecoverable {
		t.Fatalf("len(merged) = %d, want %d", len(merged), maxUnrecoverable)
	}

	// 跨运行累积也会把新上报的重叠/相邻区间合并进任务。
	s := openTest(t)
	ctx := context.Background()
	mustTask(t, s, "crypto", "ds1", true)
	c1 := mustClaim(t, s, t0)
	if err := s.CompleteRun(ctx, CompleteRequest{
		RunID: c1.Run.RunID, LeaseToken: c1.Run.LeaseToken, Status: RunSucceeded, Now: t0,
		Unrecoverable: []Unrecoverable{{SubjectID: "BTC", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, "crypto", "ds1", RunSpec{Kind: KindManual}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	c2 := mustClaim(t, s, t0.Add(time.Minute))
	if err := s.CompleteRun(ctx, CompleteRequest{
		RunID: c2.Run.RunID, LeaseToken: c2.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(2 * time.Minute),
		Unrecoverable: []Unrecoverable{{SubjectID: "BTC", StartTime: "2026-10-02T00:00:00Z", EndTime: "2026-10-03T00:00:00Z"}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, "crypto", "ds1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unrecoverable) != 1 || got.Unrecoverable[0].StartTime != "2026-10-01T00:00:00Z" || got.Unrecoverable[0].EndTime != "2026-10-03T00:00:00Z" {
		t.Fatalf("跨运行累积应归并相邻区间: %+v", got.Unrecoverable)
	}
}

func TestSourceDatasetMissingLifecycleAndIsolationFromPartitionRegression(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ds1, _ := mustTask(t, s, "crypto", "ds1", true)
	mustTask(t, s, "crypto", "ds2", false)

	c2 := mustClaim(t, s, t0) // ds1
	if c2.Task.DatasetID != "ds1" {
		t.Fatalf("claimed = %+v", c2.Task)
	}
	// 标记 ds1 源数据集不存在：ds1 状态仍是 enabled，不再进入跟随清单；在途运行的带分区完成上报不误清该标记。
	if err := s.SetTaskSourceMissing(ctx, "crypto", "ds1", true); err != nil {
		t.Fatal(err)
	}
	task1, _ := s.GetTask(ctx, "crypto", "ds1")
	if task1.Status != StatusEnabled || !task1.SourceMissing || task1.BlockedReason != "" {
		t.Fatalf("ds1 = %+v", task1)
	}
	if err := s.CompleteRun(ctx, CompleteRequest{
		RunID: c2.Run.RunID, LeaseToken: c2.Run.LeaseToken, Status: RunSucceeded, Now: t0.Add(2 * time.Minute),
		Partitions: []Partition{finishedPartition("2026-10-16", PartitionExported)},
	}); err != nil {
		t.Fatal(err)
	}
	task1, _ = s.GetTask(ctx, "crypto", "ds1")
	if !task1.SourceMissing {
		t.Fatalf("在途运行的带分区完成上报不能清掉源数据集不存在标记: %+v", task1)
	}
	// ds1 源缺失不在跟随清单里，ds2 在清单里。
	if followable, err := s.ListFollowableTasks(ctx); err != nil || len(followable) != 1 || followable[0].DatasetID != "ds2" {
		t.Fatalf("ds1 源缺失不应进入跟随清单: followable=%+v err=%v", followable, err)
	}
	// 分区登记被拒与源数据集不存在互相独立：ds1 同时出现分区倒退时，c_blocked_reason 写入且不盖掉 c_source_missing。
	shrunk := finishedPartition("2026-10-16", PartitionExported)
	shrunk.RowCount = 1
	if err := s.ReportFollow(ctx, FollowReport{
		SpaceID: "crypto", DatasetID: "ds1", TaskID: ds1.ID, Status: RunSucceeded,
		StartedAt: t0.Add(3 * time.Minute), FinishedAt: t0.Add(3*time.Minute + time.Second),
		Partitions: []Partition{shrunk},
	}); !errors.Is(err, ErrPartitionRegression) {
		t.Fatalf("期望分区倒退错误，实际 %v", err)
	}
	task1, _ = s.GetTask(ctx, "crypto", "ds1")
	if !task1.SourceMissing || task1.BlockedReason == "" {
		t.Fatalf("分区登记被拒不能盖掉源缺失标记: %+v", task1)
	}
	// 再次 SetTaskSourceMissing 也不能盖掉既有的分区登记被拒原因。
	if err := s.SetTaskSourceMissing(ctx, "crypto", "ds1", true); err != nil {
		t.Fatal(err)
	}
	task1, _ = s.GetTask(ctx, "crypto", "ds1")
	if !task1.SourceMissing || task1.BlockedReason == "" {
		t.Fatalf("SetTaskSourceMissing 不能盖掉既有的分区登记被拒原因: %+v", task1)
	}
	// 恢复源数据集存在：只清掉源缺失标记，保留分区登记被拒原因，随后恢复进入跟随清单。
	if err := s.SetTaskSourceMissing(ctx, "crypto", "ds1", false); err != nil {
		t.Fatal(err)
	}
	task1, _ = s.GetTask(ctx, "crypto", "ds1")
	if task1.SourceMissing || task1.BlockedReason == "" {
		t.Fatalf("数据集重现后应只清空源缺失标记、保留分区被拒原因: %+v", task1)
	}
	if followable, err := s.ListFollowableTasks(ctx); err != nil || len(followable) != 2 {
		t.Fatalf("ds1 恢复后应重新进入跟随清单: followable=%+v err=%v", followable, err)
	}
}
