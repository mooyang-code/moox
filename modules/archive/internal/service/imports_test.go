package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

func TestPrepareArchiveImportNeedsATaskAndAnActiveWorker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rsp, _ := h.mgr.PrepareArchiveImport(ctx, &archivepb.PrepareArchiveImportReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	rsp, _ = h.mgr.PrepareArchiveImport(ctx, &archivepb.PrepareArchiveImportReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "没有活动的归档 worker")

	h.heartbeat("w1", "i1")
	rsp, _ = h.mgr.PrepareArchiveImport(ctx, &archivepb.PrepareArchiveImportReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, rsp.GetRetInfo())
	if rsp.GetRootDir() != "/data/archive" || rsp.GetWorkerId() != "w1" || !importIDPattern.MatchString(rsp.GetImportId()) ||
		rsp.GetImportDir() != "/data/archive/.import/"+rsp.GetImportId() {
		t.Fatalf("prepare = %v", rsp)
	}
	again, _ := h.mgr.PrepareArchiveImport(ctx, &archivepb.PrepareArchiveImportReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	if again.GetImportId() == rsp.GetImportId() {
		t.Fatal("每次准备都应生成新的导入 ID")
	}
}

func TestImportArchiveFileValidatesAndYieldsQueuedTicks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	id := newImportID()
	for _, req := range map[string]*archivepb.ImportArchiveFileReq{
		"bad id":         {ImportId: "../x", FileName: "a.csv"},
		"traversal":      {ImportId: id, FileName: "../a.csv"},
		"hidden":         {ImportId: id, FileName: ".a.csv"},
		"wrong suffix":   {ImportId: id, FileName: "a.txt"},
		"with directory": {ImportId: id, FileName: "x/a.csv"},
	} {
		req.SpaceId, req.DatasetId = "crypto", "ds_1m"
		rsp, _ := h.mgr.ImportArchiveFile(ctx, req)
		requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "")
	}
	// 排队中的到点拉取让位给导入：首次检查生成复核，结束后下一分钟生成 tick。
	h.trigger(at(1, 10, 25))
	h.finishQueued()
	h.trigger(at(1, 13, 25))
	tick := h.allRuns()[0]
	if tick.GetKind() != store.KindTick || tick.GetStatus() != store.RunQueued {
		t.Fatalf("应有一条排队的 tick: %v", tick)
	}
	rsp, _ := h.mgr.ImportArchiveFile(ctx, &archivepb.ImportArchiveFileReq{SpaceId: "crypto", DatasetId: "ds_1m", ImportId: id, FileName: "import.csv.gz", Overwrite: true})
	requireOK(t, rsp.GetRetInfo())
	run := rsp.GetRun()
	if run.GetKind() != store.KindImport || run.GetImportFile() != id+"/import.csv.gz" || !run.GetOverwrite() || run.GetStatus() != store.RunQueued {
		t.Fatalf("import run = %v", run)
	}
	got, _ := h.store.GetRun(ctx, tick.GetRunId())
	if got.Status != store.RunCanceled || !strings.Contains(got.Message, "导入") {
		t.Fatalf("排队的 tick 应让位给导入: %+v", got)
	}
	// 同一任务只能有一条排队或运行中的运行。
	dup, _ := h.mgr.ImportArchiveFile(ctx, &archivepb.ImportArchiveFileReq{SpaceId: "crypto", DatasetId: "ds_1m", ImportId: id, FileName: "import.csv"})
	requireCode(t, dup.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "排队中或运行中")
	missing, _ := h.mgr.ImportArchiveFile(ctx, &archivepb.ImportArchiveFileReq{SpaceId: "crypto", DatasetId: "nope", ImportId: id, FileName: "import.csv"})
	requireCode(t, missing.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
}

// 导入下发全部分区登记（不按保留期过滤），带上导入文件；完成时不推进最近成功时间。
func TestImportRunCarriesAllPartitionsAndDoesNotAdvanceLastSuccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo()) // 保留期 168h
	h.heartbeat("w1", "i1")
	first := h.pull("w1", "i1", 1)[0]
	old := &archivepb.PartitionReport{PartitionKey: "2026-01-05", Status: "final", RangeStart: "2026-01-05T00:00:00Z", RangeEnd: "2026-01-06T00:00:00Z",
		FilePath: "crypto/collect/x/2026/20260105.csv.gz", FileSha256: "old", RowCount: 5, SubjectCount: 1, FinalizedAt: "2026-02-01T00:00:00Z"}
	recent := &archivepb.PartitionReport{PartitionKey: "2026-10-16", Status: "final", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z",
		FilePath: "crypto/collect/x/2026/20261016.csv.gz", FileSha256: "new", RowCount: 5, SubjectCount: 1, FinalizedAt: "2026-10-17T00:30:00Z"}
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: first.GetRunId(), LeaseToken: first.GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{old, recent}})
	requireOK(t, r.GetRetInfo())
	before, _ := h.store.GetTask(ctx, "crypto", "ds_1m")

	// 复核只下发保留期内的登记（没启用对象存储）。
	now, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, now.GetRetInfo())
	review := h.pull("w1", "i1", 1)[0]
	if len(review.GetRegisteredPartitions()) != 1 {
		t.Fatalf("复核应只带保留期内的登记: %v", review.GetRegisteredPartitions())
	}
	r, _ = h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: review.GetRunId(), LeaseToken: review.GetLeaseToken(), Status: "canceled"})
	requireOK(t, r.GetRetInfo())

	h.now = h.now.Add(time.Hour)
	id := newImportID()
	imp, _ := h.mgr.ImportArchiveFile(ctx, &archivepb.ImportArchiveFileReq{SpaceId: "crypto", DatasetId: "ds_1m", ImportId: id, FileName: "import.csv"})
	requireOK(t, imp.GetRetInfo())
	h.heartbeat("w1", "i1")
	assigned := h.pull("w1", "i1", 1)
	if len(assigned) != 1 || assigned[0].GetKind() != store.KindImport || assigned[0].GetImportFile() != id+"/import.csv" || assigned[0].GetOverwrite() {
		t.Fatalf("assignment = %v", assigned)
	}
	if regs := assigned[0].GetRegisteredPartitions(); len(regs) != 2 || regs[0].GetPartitionKey() != "2026-01-05" {
		t.Fatalf("导入应下发全部登记: %v", regs)
	}
	r, _ = h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: assigned[0].GetRunId(), LeaseToken: assigned[0].GetLeaseToken(),
		Status: "succeeded", Message: "导入 3 行", RowsAdded: 3})
	requireOK(t, r.GetRetInfo())
	after, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if !after.LastSuccessAt.Equal(before.LastSuccessAt) {
		t.Fatalf("导入不应更新最近成功时间: %s → %s", before.LastSuccessAt, after.LastSuccessAt)
	}
	run, _ := h.mgr.GetArchiveRun(ctx, &archivepb.GetArchiveRunReq{RunId: assigned[0].GetRunId()})
	if run.GetRun().GetStatus() != store.RunSucceeded || run.GetRun().GetRowsAdded() != 3 || run.GetRun().GetImportFile() != id+"/import.csv" {
		t.Fatalf("run = %v", run.GetRun())
	}
}

func TestClearArchiveUnrecoverableRemovesOnlyWholeRangesInside(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1", "i1")
	run := h.pull("w1", "i1", 1)[0]
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded",
		Unrecoverable: []*archivepb.UnrecoverableRange{
			{StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"},
			{StartTime: "2026-10-03T00:00:00Z", EndTime: "2026-10-05T00:00:00Z"},
			{StartTime: "2026-09-30T00:00:00Z", EndTime: "2026-10-01T12:00:00Z"},
		}})
	requireOK(t, r.GetRetInfo())
	bad, _ := h.mgr.ClearArchiveUnrecoverable(ctx, &archivepb.ClearArchiveUnrecoverableReq{SpaceId: "crypto", DatasetId: "ds_1m", Start: "2026-10-05T00:00:00Z", End: "2026-10-01T00:00:00Z"})
	requireCode(t, bad.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "start")
	rsp, _ := h.mgr.ClearArchiveUnrecoverable(ctx, &archivepb.ClearArchiveUnrecoverableReq{SpaceId: "crypto", DatasetId: "ds_1m", Start: "2026-10-01T00:00:00Z", End: "2026-10-05T00:00:00Z"})
	requireOK(t, rsp.GetRetInfo())
	if len(rsp.GetRemoved()) != 2 {
		t.Fatalf("应删掉整段落在范围内的两条: %v", rsp.GetRemoved())
	}
	task, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if len(task.Unrecoverable) != 1 || task.Unrecoverable[0].StartTime != "2026-09-30T00:00:00Z" {
		t.Fatalf("跨出范围的区间应保留: %+v", task.Unrecoverable)
	}
	missing, _ := h.mgr.ClearArchiveUnrecoverable(ctx, &archivepb.ClearArchiveUnrecoverableReq{SpaceId: "crypto", DatasetId: "nope", Start: "2026-10-01T00:00:00Z", End: "2026-10-05T00:00:00Z"})
	requireCode(t, missing.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
}
