package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/codec"
)

type fakeDatasets struct {
	mu       sync.Mutex
	datasets map[string]Dataset
	calls    int
}

func (f *fakeDatasets) GetDataset(_ context.Context, space, dataset string) (Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	ds, ok := f.datasets[space+"/"+dataset]
	if !ok {
		return Dataset{}, fmt.Errorf("%w: %s/%s", ErrDatasetNotFound, space, dataset)
	}
	return ds, nil
}

type harness struct {
	t        *testing.T
	svc      *Service
	mgr      *MgrService
	worker   *WorkerService
	datasets *fakeDatasets
	store    *store.Store
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{t: t, store: db, now: time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)}
	h.datasets = &fakeDatasets{datasets: map[string]Dataset{
		"crypto/ds_1m":  {SpaceID: "crypto", DatasetID: "ds_1m", DataKind: "time_series", Freq: "1m", Status: "active", DataNodeID: "node-0", Retention: "168h"},
		"crypto/ds_1h":  {SpaceID: "crypto", DatasetID: "ds_1h", DataKind: "time_series", Freq: "1h", Status: "active", DataNodeID: "node-0", Retention: "forever"},
		"crypto/rec":    {SpaceID: "crypto", DatasetID: "rec", DataKind: "record", Freq: "", Status: "active", DataNodeID: "node-0", Retention: "forever"},
		"crypto/off":    {SpaceID: "crypto", DatasetID: "off", DataKind: "time_series", Freq: "1m", Status: "building", DataNodeID: "node-0", Retention: "168h"},
		"stockcn/cn_1m": {SpaceID: "stockcn", DatasetID: "cn_1m", DataKind: "time_series", Freq: "1m", Status: "active", DataNodeID: "node-0", Retention: "1080h"},
	}}
	svc, err := New(db, config.Default(), h.datasets, WithClock(func() time.Time { return h.now }))
	if err != nil {
		t.Fatal(err)
	}
	h.svc, h.mgr, h.worker = svc, svc.Mgr(), svc.Worker()
	return h
}

func withSpace(space string) context.Context {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerMetaData(codec.MetaData{"X-Space-Id": []byte(space)})
	return ctx
}

func requireOK(t *testing.T, info *commonpb.RetInfo) {
	t.Helper()
	if info.GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %v %q, want success", info.GetCode(), info.GetMsg())
	}
}

func requireCode(t *testing.T, info *commonpb.RetInfo, want commonpb.ErrorCode, contains string) {
	t.Helper()
	if info.GetCode() != want || !strings.Contains(info.GetMsg(), contains) {
		t.Fatalf("ret_info = %v %q, want %v containing %q", info.GetCode(), info.GetMsg(), want, contains)
	}
}

func (h *harness) create(dataset string, mutate func(*archivepb.CreateArchiveTaskReq)) *archivepb.CreateArchiveTaskRsp {
	h.t.Helper()
	req := &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: dataset, ScheduleCron: "43 4-59/5 * * * *"}
	if mutate != nil {
		mutate(req)
	}
	rsp, err := h.mgr.CreateArchiveTask(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

func TestCreateArchiveTaskValidatesTheDatasetAndSchedule(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name   string
		ds     string
		mutate func(*archivepb.CreateArchiveTaskReq)
		code   commonpb.ErrorCode
		text   string
	}{
		{"unknown dataset", "nope", nil, commonpb.ErrorCode_DATASET_NOT_FOUND, "nope"},
		{"record dataset", "rec", nil, commonpb.ErrorCode_INVALID_PARAM, "不是时序数据集"},
		{"inactive dataset", "off", nil, commonpb.ErrorCode_INVALID_PARAM, "尚未激活"},
		{"too frequent", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.ScheduleCron = "0 */4 * * * *" }, commonpb.ErrorCode_INVALID_PARAM, "最小间隔"},
		{"bad cron", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.ScheduleCron = "0 0 * * *" }, commonpb.ErrorCode_INVALID_PARAM, "6 段"},
		{"bad tz", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.ScheduleTz = "Mars/Base" }, commonpb.ErrorCode_INVALID_PARAM, "时区"},
		{"bad start mode", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.StartMode = "later" }, commonpb.ErrorCode_INVALID_PARAM, "start_mode"},
		{"at without time", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.StartMode = "at" }, commonpb.ErrorCode_INVALID_PARAM, "start_at"},
		{"backfill in future", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-18T00:00:00Z" }, commonpb.ErrorCode_INVALID_PARAM, "早于当前时间"},
		{"backfill beyond retention", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-01T00:00:00Z" }, commonpb.ErrorCode_INVALID_PARAM, "不能早于"},
		{"missing space", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.SpaceId = "" }, commonpb.ErrorCode_INVALID_PARAM, "space_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rsp := h.create(tc.ds, tc.mutate)
			requireCode(t, rsp.GetRetInfo(), tc.code, tc.text)
		})
	}
	if rsp, _ := h.mgr.ListArchiveTasks(context.Background(), &archivepb.ListArchiveTasksReq{}); len(rsp.GetTasks()) != 0 {
		t.Fatalf("失败的创建不应留下任务: %v", rsp.GetTasks())
	}
}

func TestCreateArchiveTaskImmediateStartQueuesARunAndSchedulesTheNext(t *testing.T) {
	h := newHarness(t)
	rsp := h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-15T00:00:00Z" })
	requireOK(t, rsp.GetRetInfo())
	task := rsp.GetTask()
	if task.GetFreq() != "1m" || task.GetPartitionUnit() != "day" || task.GetIntervalSeconds() != 300 || task.GetStatus() != "enabled" ||
		task.GetDataNodeId() != "node-0" || task.GetRetention() != "168h" || task.GetScheduleTz() != "Asia/Shanghai" || task.GetBackfillFrom() != "2026-10-15T00:00:00Z" {
		t.Fatalf("task = %v", task)
	}
	if task.GetNextRunAt() != "2026-10-17T01:04:43Z" {
		t.Fatalf("next_run_at = %s, want 01:04:43Z", task.GetNextRunAt())
	}
	runs, _ := h.mgr.ListArchiveRuns(context.Background(), &archivepb.ListArchiveRunsReq{})
	if len(runs.GetRuns()) != 1 || runs.GetRuns()[0].GetKind() != "manual" || runs.GetRuns()[0].GetStatus() != "queued" {
		t.Fatalf("runs = %v", runs.GetRuns())
	}
	dup := h.create("ds_1m", nil)
	requireCode(t, dup.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "已经有归档任务")
}

func TestCreateArchiveTaskAtAFutureTimeAndPausedTasksDoNotRunImmediately(t *testing.T) {
	h := newHarness(t)
	rsp := h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.StartMode, r.StartAt = "at", "2026-10-17T03:00:00Z" })
	requireOK(t, rsp.GetRetInfo())
	if rsp.GetTask().GetNextRunAt() != "2026-10-17T03:04:43Z" {
		t.Fatalf("指定开始时间之前的计划时刻不运行: %s", rsp.GetTask().GetNextRunAt())
	}
	if runs, _ := h.mgr.ListArchiveRuns(context.Background(), &archivepb.ListArchiveRunsReq{}); len(runs.GetRuns()) != 0 {
		t.Fatalf("指定时间开始不应立刻生成运行: %v", runs.GetRuns())
	}
	paused := h.create("ds_1h", func(r *archivepb.CreateArchiveTaskReq) { r.Paused = true })
	requireOK(t, paused.GetRetInfo())
	if paused.GetTask().GetStatus() != "paused" || paused.GetTask().GetNextRunAt() != "" {
		t.Fatalf("paused task = %v", paused.GetTask())
	}
	if paused.GetTask().GetPartitionUnit() != "day" || paused.GetTask().GetRetention() != "forever" {
		t.Fatalf("task = %v", paused.GetTask())
	}
}

func TestSpaceHeaderScopesTheConsoleAPI(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	rsp, _ := h.mgr.CreateArchiveTask(withSpace("stockcn"), &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1h", ScheduleCron: "0 0 * * * *"})
	requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "不一致")
	// 请求头里的空间足够，请求体可以不带 space_id。
	cn, _ := h.mgr.CreateArchiveTask(withSpace("stockcn"), &archivepb.CreateArchiveTaskReq{DatasetId: "cn_1m", ScheduleCron: "43 4 17 * * *"})
	requireOK(t, cn.GetRetInfo())
	list, _ := h.mgr.ListArchiveTasks(withSpace("stockcn"), &archivepb.ListArchiveTasksReq{})
	if len(list.GetTasks()) != 1 || list.GetTasks()[0].GetSpaceId() != "stockcn" || list.GetPageResult().GetTotal() != 1 {
		t.Fatalf("stockcn 只能看到自己的任务: %v", list.GetTasks())
	}
	all, _ := h.mgr.ListArchiveTasks(context.Background(), &archivepb.ListArchiveTasksReq{})
	if len(all.GetTasks()) != 2 {
		t.Fatalf("内部调用方看到全部任务: %v", all.GetTasks())
	}
	// 其他空间不能取消、查看本空间的运行。
	runs, _ := h.mgr.ListArchiveRuns(context.Background(), &archivepb.ListArchiveRunsReq{})
	var cryptoRun string
	for _, r := range runs.GetRuns() {
		if r.GetSpaceId() == "crypto" {
			cryptoRun = r.GetRunId()
		}
	}
	cancel, _ := h.mgr.CancelArchiveRun(withSpace("stockcn"), &archivepb.CancelArchiveRunReq{RunId: cryptoRun})
	requireCode(t, cancel.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
	get, _ := h.mgr.GetArchiveRun(withSpace("stockcn"), &archivepb.GetArchiveRunReq{RunId: cryptoRun})
	requireCode(t, get.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
	scoped, _ := h.mgr.ListArchiveRuns(withSpace("stockcn"), &archivepb.ListArchiveRunsReq{})
	for _, r := range scoped.GetRuns() {
		if r.GetSpaceId() != "stockcn" {
			t.Fatalf("泄漏了其他空间的运行: %v", r)
		}
	}
	parts, _ := h.mgr.ListArchivePartitions(context.Background(), &archivepb.ListArchivePartitionsReq{})
	requireCode(t, parts.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "space_id")
	conflicting := withSpace("crypto")
	if msg := codec.Message(conflicting); msg != nil {
		msg.WithServerMetaData(codec.MetaData{"X-Space-Id": []byte("crypto"), "x-space-id": []byte("stockcn")})
	}
	bad, _ := h.mgr.ListArchiveTasks(conflicting, &archivepb.ListArchiveTasksReq{})
	requireCode(t, bad.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "冲突")
}

func TestUpdateArchiveTaskKeepsAnUnfinishedBackfillStartEvenWhenItAgedOutOfRetention(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-11T01:00:00Z" }).GetRetInfo())
	// 时间过去 2 天：回补起点已早于「当前时间 − 保留期 + 1 天」，只停用任务不能把它清空。
	h.now = h.now.Add(48 * time.Hour)
	rsp, _ := h.mgr.UpdateArchiveTask(context.Background(), &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "paused"})
	requireOK(t, rsp.GetRetInfo())
	if got := rsp.GetTask().GetBackfillFrom(); got != "2026-10-11T01:00:00Z" {
		t.Fatalf("backfill_from = %q，回补起点被改丢", got)
	}
}

func TestUpdateArchiveTaskRecomputesTheScheduleAndHandlesPauseResume(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	ctx := context.Background()
	updated, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", ScheduleCron: "43 34 * * * *"})
	requireOK(t, updated.GetRetInfo())
	if updated.GetTask().GetIntervalSeconds() != 3600 || updated.GetTask().GetNextRunAt() != "2026-10-17T01:34:43Z" || updated.GetTask().GetStatus() != "enabled" {
		t.Fatalf("updated = %v", updated.GetTask())
	}
	bad, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", ScheduleCron: "0 */2 * * * *"})
	requireCode(t, bad.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "最小间隔")
	badStatus, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "weird"})
	requireCode(t, badStatus.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "status")

	paused, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "paused"})
	requireOK(t, paused.GetRetInfo())
	if paused.GetTask().GetStatus() != "paused" || paused.GetTask().GetNextRunAt() != "" {
		t.Fatalf("paused = %v", paused.GetTask())
	}
	h.now = h.now.Add(2 * time.Hour)
	resumed, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "enabled", RunNow: true})
	requireOK(t, resumed.GetRetInfo())
	if resumed.GetTask().GetNextRunAt() != "2026-10-17T03:34:43Z" {
		t.Fatalf("重新启用后从当前时刻往后排: %s", resumed.GetTask().GetNextRunAt())
	}
	queued, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued"})
	if len(queued.GetRuns()) != 1 {
		t.Fatalf("重新启用并立即运行应有一条排队运行: %v", queued.GetRuns())
	}
	missing, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "nope"})
	requireCode(t, missing.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
}

func TestPrepareAndPreviewReportDefaultsAndReadSpans(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	prep, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, prep.GetRetInfo())
	if prep.GetFreq() != "1m" || prep.GetPartitionUnit() != "day" || prep.GetRecommendedCron() != "43 34 * * * *" || prep.GetRecommendedTz() != "Asia/Shanghai" ||
		prep.GetEarliestBackfillFrom() != "2026-10-11T01:00:00Z" || prep.GetExists() || prep.GetDataNodeId() != "node-0" {
		t.Fatalf("prepare = %v", prep)
	}
	forever, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1h"})
	if forever.GetEarliestBackfillFrom() != "" || forever.GetRecommendedCron() != "43 34 8 * * *" {
		t.Fatalf("forever prepare = %v", forever)
	}
	cn, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "stockcn", DatasetId: "cn_1m"})
	if cn.GetRecommendedCron() != "43 4 17 * * *" {
		t.Fatalf("stockcn prepare = %v", cn)
	}
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	again, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	if !again.GetExists() {
		t.Fatal("已有任务时 exists 应为真")
	}

	// crypto 1m 每 5 分钟：读取区间 = 5 分钟 + 15 分钟回看 + 一根 1m = 21 分钟。
	preview, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "crypto", DatasetId: "ds_1m", ScheduleCron: "43 4-59/5 * * * *"})
	requireOK(t, preview.GetRetInfo())
	if preview.GetIntervalSeconds() != 300 || preview.GetReadSpanSeconds() != 21*60 || len(preview.GetNextRuns()) != 5 || preview.GetNextRuns()[0] != "2026-10-17T01:04:43Z" {
		t.Fatalf("preview = %v", preview)
	}
	if len(preview.GetPartitionExportEstimates()) != 2 || !strings.Contains(preview.GetPartitionExportEstimates()[1], "2026-10-17") {
		t.Fatalf("estimates = %v", preview.GetPartitionExportEstimates())
	}
	// 落在避让区间的计划给出提示。
	hint, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "crypto", DatasetId: "ds_1m", ScheduleCron: "0 0 * * * *"})
	requireOK(t, hint.GetRetInfo())
	if len(hint.GetWarnings()) == 0 || !strings.Contains(hint.GetWarnings()[0], "实际读取从") {
		t.Fatalf("warnings = %v", hint.GetWarnings())
	}
	bad, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "crypto", DatasetId: "ds_1m", ScheduleCron: "0 */1 * * * *"})
	requireCode(t, bad.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "最小间隔")
	// stockcn 每天 17:04:43（上海）：当日分区 15:00 收盘 + 15 分钟，当天 17:04:43 导出。
	cnPreview, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "stockcn", DatasetId: "cn_1m", ScheduleCron: "43 4 17 * * *"})
	requireOK(t, cnPreview.GetRetInfo())
	joined := strings.Join(cnPreview.GetPartitionExportEstimates(), "|")
	if !strings.Contains(joined, "2026-10-17T09:04:43Z") {
		t.Fatalf("stockcn estimates = %v", cnPreview.GetPartitionExportEstimates())
	}
	if cnPreview.GetReadSpanSeconds() != uint32(24*3600+15*60+60) {
		t.Fatalf("stockcn read span = %d", cnPreview.GetReadSpanSeconds())
	}
}

func TestBackfillAndRunNowRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.Paused = true }).GetRetInfo())
	run, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, run.GetRetInfo())
	again, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireCode(t, again.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "排队中或运行中")
	cancel, _ := h.mgr.CancelArchiveRun(ctx, &archivepb.CancelArchiveRunReq{RunId: run.GetRun().GetRunId()})
	requireOK(t, cancel.GetRetInfo())
	if cancel.GetRun().GetStatus() != "canceled" {
		t.Fatalf("cancel = %v", cancel.GetRun())
	}
	cancelAgain, _ := h.mgr.CancelArchiveRun(ctx, &archivepb.CancelArchiveRunReq{RunId: run.GetRun().GetRunId()})
	requireCode(t, cancelAgain.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "已经结束")

	for name, tc := range map[string]struct {
		start, end string
		text       string
	}{
		"empty":      {"", "", "不能为空"},
		"reversed":   {"2026-10-17T00:30:00Z", "2026-10-17T00:00:00Z", "早于"},
		"future end": {"2026-10-17T00:00:00Z", "2026-10-18T00:00:00Z", "不能晚于当前时间"},
		"too old":    {"2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z", "保留期截止"},
		"bad format": {"yesterday", "2026-10-02T00:00:00Z", "RFC3339"},
	} {
		rsp, _ := h.mgr.BackfillArchiveRange(ctx, &archivepb.BackfillArchiveRangeReq{SpaceId: "crypto", DatasetId: "ds_1m", RangeStart: tc.start, RangeEnd: tc.end})
		requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, tc.text)
		_ = name
	}
	ok, _ := h.mgr.BackfillArchiveRange(ctx, &archivepb.BackfillArchiveRangeReq{SpaceId: "crypto", DatasetId: "ds_1m", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-16T12:00:00Z"})
	requireOK(t, ok.GetRetInfo())
	if ok.GetRun().GetKind() != "range" || ok.GetRun().GetRangeStart() != "2026-10-16T00:00:00Z" {
		t.Fatalf("range run = %v", ok.GetRun())
	}
	noTask, _ := h.mgr.BackfillArchiveRange(ctx, &archivepb.BackfillArchiveRangeReq{SpaceId: "crypto", DatasetId: "ds_1h", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-16T12:00:00Z"})
	requireCode(t, noTask.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
}

func (h *harness) heartbeat(worker, instance string, running ...string) *archivepb.ArchiveWorkerHeartbeatRsp {
	h.t.Helper()
	rsp, err := h.worker.ArchiveWorkerHeartbeat(context.Background(), &archivepb.ArchiveWorkerHeartbeatReq{
		WorkerId: worker, InstanceId: instance, Version: "test", RootDir: "/data/archive", DiskTotalBytes: 100, DiskFreeBytes: 60, RunningRunIds: running,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

func (h *harness) pull(worker, instance string, slots uint32) []*archivepb.RunAssignment {
	h.t.Helper()
	rsp, err := h.worker.PullArchiveRun(context.Background(), &archivepb.PullArchiveRunReq{WorkerId: worker, InstanceId: instance, FreeSlots: slots, MaxRunsPerDataNode: 2})
	if err != nil {
		h.t.Fatal(err)
	}
	requireOK(h.t, rsp.GetRetInfo())
	return rsp.GetRuns()
}

func TestWorkerLeaseAndPullFlow(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-15T00:00:00Z" }).GetRetInfo())
	if runs := h.pull("w1", "i1", 2); len(runs) != 0 {
		t.Fatalf("没有心跳取得租约前领不到运行: %v", runs)
	}
	hb := h.heartbeat("w1", "i1")
	requireOK(t, hb.GetRetInfo())
	if !hb.GetActive() || hb.GetLeaseTtlSeconds() != 45 {
		t.Fatalf("heartbeat = %v", hb)
	}
	// 备用 worker 不活动，也领不到。
	standby := h.heartbeat("w2", "j1")
	if standby.GetActive() {
		t.Fatal("w2 不应活动")
	}
	if runs := h.pull("w2", "j1", 2); len(runs) != 0 {
		t.Fatalf("备用 worker 领到了运行: %v", runs)
	}
	dup := h.heartbeat("w1", "i2")
	requireCode(t, dup.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "另一个存活的实例")

	runs := h.pull("w1", "i1", 2)
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	run := runs[0]
	if run.GetLeaseToken() == "" || run.GetKind() != "manual" || run.GetFreq() != "1m" || run.GetPartitionUnit() != "day" || run.GetDataNodeId() != "node-0" ||
		run.GetMinLookbackSeconds() != 900 || run.GetStagingRetentionSeconds() != 72*3600 || run.GetSpaceTz() != "UTC" || run.GetLocalClose() != "" ||
		run.GetBackfillFrom() != "2026-10-15T00:00:00Z" || run.GetStartedAt() != "2026-10-17T01:00:00Z" || run.GetIntervalSeconds() != 300 || run.GetLeaseTtlSeconds() != 600 {
		t.Fatalf("assignment = %v", run)
	}
	if more := h.pull("w1", "i1", 2); len(more) != 0 {
		t.Fatalf("同一任务同时只有一个运行: %v", more)
	}

	progress, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{
		WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), SubjectsTotal: 5, SubjectsDone: 2, RequestCount: 3, BytesRead: 100, RowsRead: 40,
	})
	requireOK(t, progress.GetRetInfo())
	if progress.GetCanceled() {
		t.Fatal("不应取消")
	}
	wrong, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: "x"})
	if !wrong.GetCanceled() {
		t.Fatal("租约不对应视为取消")
	}
	empty, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{})
	requireCode(t, empty.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "不能为空")

	// 用户取消：下一次心跳让 worker 停止这条运行。
	cancel, _ := h.mgr.CancelArchiveRun(context.Background(), &archivepb.CancelArchiveRunReq{RunId: run.GetRunId()})
	requireOK(t, cancel.GetRetInfo())
	hb = h.heartbeat("w1", "i1", run.GetRunId())
	if len(hb.GetCancelRunIds()) != 1 || hb.GetCancelRunIds()[0] != run.GetRunId() {
		t.Fatalf("heartbeat = %v", hb)
	}
	gone, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken()})
	if !gone.GetCanceled() {
		t.Fatal("取消后进度上报应返回 canceled")
	}
}

func TestCompleteArchiveRunRegistersPartitionsAndRejectsBadReports(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1", "i1")
	run := h.pull("w1", "i1", 1)[0]
	ctx := context.Background()
	base := func() *archivepb.CompleteArchiveRunReq {
		return &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded"}
	}
	for name, mutate := range map[string]func(*archivepb.CompleteArchiveRunReq){
		"bad status": func(r *archivepb.CompleteArchiveRunReq) { r.Status = "done" },
		"bad time":   func(r *archivepb.CompleteArchiveRunReq) { r.FinalUntil = "soon" },
		"no key": func(r *archivepb.CompleteArchiveRunReq) {
			r.Partitions = []*archivepb.PartitionReport{{Status: "open"}}
		},
		"bad partition": func(r *archivepb.CompleteArchiveRunReq) {
			r.Partitions = []*archivepb.PartitionReport{{PartitionKey: "k", Status: "weird"}}
		},
		"empty range": func(r *archivepb.CompleteArchiveRunReq) {
			r.Partitions = []*archivepb.PartitionReport{{PartitionKey: "k", Status: "open"}}
		},
		"final no time": func(r *archivepb.CompleteArchiveRunReq) {
			r.Partitions = []*archivepb.PartitionReport{{PartitionKey: "k", Status: "final", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z", FilePath: "f"}}
		},
		"duplicate keys": func(r *archivepb.CompleteArchiveRunReq) {
			p := &archivepb.PartitionReport{PartitionKey: "k", Status: "open", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z"}
			r.Partitions = []*archivepb.PartitionReport{p, p}
		},
	} {
		req := base()
		mutate(req)
		rsp, _ := h.worker.CompleteArchiveRun(ctx, req)
		requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "")
		_ = name
	}
	noFile := base()
	noFile.Partitions = []*archivepb.PartitionReport{{PartitionKey: "2026-10-16", Status: "exported", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z"}}
	if rsp, _ := h.worker.CompleteArchiveRun(ctx, noFile); rsp.GetRetInfo().GetCode() == commonpb.ErrorCode_SUCCESS {
		t.Fatal("已导出的分区没有文件路径必须被拒绝")
	}
	good := base()
	good.SubjectsTotal, good.SubjectsDone, good.RowsRead, good.RowsAdded, good.PartitionsExported = 2, 2, 100, 90, 1
	good.FinalUntil = "2026-10-14T00:00:00Z"
	good.Unrecoverable = []*archivepb.UnrecoverableRange{{SubjectId: "BTC-USDT", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}}
	good.Partitions = []*archivepb.PartitionReport{{
		PartitionKey: "2026-10-16", Status: "exported", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z", RowCount: 100, SubjectCount: 2,
		FilePath: "crypto/ds_1m/2026-10-16.csv.gz", FileBytes: 10, FileSha256: "abc", Columns: []string{"open", "close"}, ExportCount: 1, ExportedAt: "2026-10-17T01:05:00Z",
	}}
	rsp, _ := h.worker.CompleteArchiveRun(ctx, good)
	requireOK(t, rsp.GetRetInfo())
	again, _ := h.worker.CompleteArchiveRun(ctx, good)
	requireCode(t, again.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "")

	parts, _ := h.mgr.ListArchivePartitions(context.Background(), &archivepb.ListArchivePartitionsReq{SpaceId: "crypto", DatasetId: "ds_1m", FromMonth: "2026-10", ToMonth: "2026-10"})
	requireOK(t, parts.GetRetInfo())
	if len(parts.GetPartitions()) != 1 || parts.GetPartitions()[0].GetFileSha256() != "abc" || parts.GetPartitions()[0].GetWorkerId() != "w1" {
		t.Fatalf("partitions = %v", parts.GetPartitions())
	}
	none, _ := h.mgr.ListArchivePartitions(context.Background(), &archivepb.ListArchivePartitionsReq{SpaceId: "crypto", DatasetId: "ds_1m", FromMonth: "2026-11"})
	if len(none.GetPartitions()) != 0 {
		t.Fatalf("月份过滤: %v", none.GetPartitions())
	}
	badMonth, _ := h.mgr.ListArchivePartitions(context.Background(), &archivepb.ListArchivePartitionsReq{SpaceId: "crypto", FromMonth: "10/2026"})
	requireCode(t, badMonth.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "YYYY-MM")
	one, _ := h.mgr.GetArchivePartition(context.Background(), &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "2026-10-16"})
	requireOK(t, one.GetRetInfo())
	missing, _ := h.mgr.GetArchivePartition(context.Background(), &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "nope"})
	requireCode(t, missing.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")

	// 任务列表带出汇总与健康等级：有无法补齐的区间 → error。
	list, _ := h.mgr.ListArchiveTasks(context.Background(), &archivepb.ListArchiveTasksReq{})
	task := list.GetTasks()[0]
	if task.GetPartitionCount() != 1 || task.GetFileCount() != 1 || task.GetTotalRows() != 100 || task.GetLastRun().GetStatus() != "succeeded" || task.GetFinalUntil() != "2026-10-14T00:00:00Z" {
		t.Fatalf("task = %v", task)
	}
	if task.GetHealth() != HealthError || !strings.Contains(task.GetHealthReason(), "无法补齐") {
		t.Fatalf("health = %s %s", task.GetHealth(), task.GetHealthReason())
	}
	run2, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, run2.GetRetInfo())
	h.heartbeat("w2", "other")
	// 分区登记在 w1：即使 w1 的租约过期、w2 接管，这个任务也不会派给 w2。
	h.now = h.now.Add(2 * time.Minute)
	if hb := h.heartbeat("w2", "other"); !hb.GetActive() {
		t.Fatal("w1 租约过期后 w2 应接管")
	}
	if runs := h.pull("w2", "other", 1); len(runs) != 0 {
		t.Fatalf("分区登记在 w1 的任务不能派给 w2: %v", runs)
	}
	overview, _ := h.mgr.GetArchiveOverview(context.Background(), &archivepb.GetArchiveOverviewReq{})
	found := false
	for _, alert := range overview.GetAlerts() {
		if strings.Contains(alert, "分区登记在 worker w1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("总览应提示分区属于另一个 worker: %v", overview.GetAlerts())
	}
}

func TestDeleteArchiveTaskNotifiesTheWorkerAndKeepsPartitionedTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	requireOK(t, h.create("ds_1h", func(r *archivepb.CreateArchiveTaskReq) { r.ScheduleCron = "0 34 8 * * *" }).GetRetInfo())
	h.heartbeat("w1", "i1")
	run := h.pull("w1", "i1", 1)[0]
	done := &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded", Partitions: []*archivepb.PartitionReport{{
		PartitionKey: "2026-10-16", Status: "exported", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z", FilePath: "f", FileSha256: "abc",
	}}}
	requireOK(t, func() *commonpb.RetInfo { r, _ := h.worker.CompleteArchiveRun(ctx, done); return r.GetRetInfo() }())
	partitioned := run.GetDatasetId()
	other := "ds_1h"
	if partitioned == "ds_1h" {
		other = "ds_1m"
	}
	refused, _ := h.mgr.DeleteArchiveTask(ctx, &archivepb.DeleteArchiveTaskReq{SpaceId: "crypto", DatasetId: partitioned})
	requireCode(t, refused.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "分区")
	// 另一个任务还有排队中的运行但没有分区：可以删除。
	deleted, _ := h.mgr.DeleteArchiveTask(ctx, &archivepb.DeleteArchiveTaskReq{SpaceId: "crypto", DatasetId: other})
	requireOK(t, deleted.GetRetInfo())
	hb := h.heartbeat("w1", "i1")
	if len(hb.GetDeletedTasks()) != 1 || hb.GetDeletedTasks()[0].GetDatasetId() != other || hb.GetDeletedTasks()[0].GetSpaceId() != "crypto" {
		t.Fatalf("heartbeat = %v", hb)
	}
	h.now = h.now.Add(25 * time.Hour)
	h.heartbeat("w1", "i1")
	if hb := h.heartbeat("w1", "i1"); len(hb.GetDeletedTasks()) != 0 {
		t.Fatalf("通知一天后过期: %v", hb.GetDeletedTasks())
	}
}

func TestTriggerAndMaintainDriveTheScheduleAndLeaseRecovery(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	// 立即运行的 manual 先被领取并完成，之后到点才生成 scheduled 运行。
	h.heartbeat("w1", "i1")
	manual := h.pull("w1", "i1", 1)[0]
	if rsp, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: manual.GetRunId(), LeaseToken: manual.GetLeaseToken(), Status: "succeeded"}); rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatal(rsp.GetRetInfo())
	}
	h.now = time.Date(2026, 10, 17, 1, 4, 44, 0, time.UTC)
	if err := h.svc.Trigger(ctx); err != nil {
		t.Fatal(err)
	}
	scheduled, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued"})
	if len(scheduled.GetRuns()) != 1 || scheduled.GetRuns()[0].GetKind() != "scheduled" || scheduled.GetRuns()[0].GetScheduledAt() != "2026-10-17T01:04:43Z" {
		t.Fatalf("scheduled = %v", scheduled.GetRuns())
	}
	list, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{})
	if list.GetTasks()[0].GetNextRunAt() != "2026-10-17T01:09:43Z" {
		t.Fatalf("next = %s", list.GetTasks()[0].GetNextRunAt())
	}
	// 领取后 worker 失联：租约过期，运行被标为失败，任务健康提示失败。
	h.heartbeat("w1", "i1")
	claimed := h.pull("w1", "i1", 1)
	if len(claimed) != 1 {
		t.Fatalf("claimed = %v", claimed)
	}
	h.now = h.now.Add(11 * time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	failed, _ := h.mgr.GetArchiveRun(ctx, &archivepb.GetArchiveRunReq{RunId: claimed[0].GetRunId()})
	if failed.GetRun().GetStatus() != "failed" || failed.GetRun().GetMessage() != "worker 失联" {
		t.Fatalf("run = %v", failed.GetRun())
	}
	list, _ = h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{})
	if list.GetTasks()[0].GetHealth() != HealthWarning || !strings.Contains(list.GetTasks()[0].GetHealthReason(), "最近一次运行失败") {
		t.Fatalf("task health = %s %s", list.GetTasks()[0].GetHealth(), list.GetTasks()[0].GetHealthReason())
	}
}

func TestMaintainRefreshesDatasetInfoOncePerHour(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.datasets.mu.Lock()
	ds := h.datasets.datasets["crypto/ds_1m"]
	ds.Retention, ds.DataNodeID = "336h", "node-7"
	h.datasets.datasets["crypto/ds_1m"] = ds
	h.datasets.mu.Unlock()
	callsBefore := h.datasets.calls
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	task, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if task.Retention != "336h" || task.DataNodeID != "node-7" || h.datasets.calls != callsBefore+1 {
		t.Fatalf("task = %+v calls=%d", task, h.datasets.calls)
	}
	if err := h.svc.Maintain(ctx); err != nil || h.datasets.calls != callsBefore+1 {
		t.Fatalf("一小时内不重复刷新: calls=%d err=%v", h.datasets.calls, err)
	}
	h.now = h.now.Add(61 * time.Minute)
	delete(h.datasets.datasets, "crypto/ds_1m")
	if err := h.svc.Maintain(ctx); err == nil {
		t.Fatal("数据集读取失败要返回错误，下一轮重试")
	}
}

func TestHealthLevelsFollowTheDesignTable(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	base := store.Task{SpaceID: "crypto", DatasetID: "ds", Freq: "1m", PartitionUnit: "day", Retention: "168h", Status: store.StatusEnabled,
		IntervalSeconds: 300, CreatedAt: now.Add(-24 * time.Hour), LastSuccessAt: now.Add(-10 * time.Minute)}
	eval := func(mutate func(*healthInput)) (string, string) {
		in := healthInput{Now: now, Task: base, WorkerActive: true, Lookback: 15 * time.Minute, SpaceTZ: "UTC"}
		if mutate != nil {
			mutate(&in)
		}
		return evaluateHealth(in)
	}
	if level, _ := eval(nil); level != HealthOK {
		t.Fatalf("healthy = %s", level)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.Status = store.StatusPaused }); level != HealthPaused {
		t.Fatalf("paused = %s", level)
	}
	// 168h 保留期：最近成功超过 120h（保留期 − 2 天）→ 异常。
	if level, reason := eval(func(in *healthInput) { in.Task.LastSuccessAt = now.Add(-121 * time.Hour) }); level != HealthError || !strings.Contains(reason, "保留期") {
		t.Fatalf("stale success = %s %s", level, reason)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.LastSuccessAt = now.Add(-119 * time.Hour) }); level != HealthOK {
		t.Fatalf("just inside = %s", level)
	}
	// 从未成功过：按创建时间算。
	if level, _ := eval(func(in *healthInput) {
		in.Task.LastSuccessAt = time.Time{}
		in.Task.CreatedAt = now.Add(-130 * time.Hour)
	}); level != HealthError {
		t.Fatalf("never succeeded = %s", level)
	}
	// 保留期不限的任务不按保留期判断。
	if level, _ := eval(func(in *healthInput) {
		in.Task.Retention = "forever"
		in.Task.LastSuccessAt = now.Add(-1000 * time.Hour)
	}); level != HealthOK {
		t.Fatalf("forever = %s", level)
	}
	unrecoverable := &store.Run{Unrecoverable: []store.Unrecoverable{{StartTime: "a", EndTime: "b"}}}
	if level, _ := eval(func(in *healthInput) { in.LastRun = unrecoverable }); level != HealthError {
		t.Fatalf("unrecoverable = %s", level)
	}
	// 有到期运行时活动 worker 离线超过 30 分钟 → 异常；30 分钟内只是排队。
	queuedLong := now.Add(-45 * time.Minute)
	if level, _ := eval(func(in *healthInput) {
		in.WorkerActive, in.OldestQueued, in.WorkerLastSeen = false, queuedLong, now.Add(-40*time.Minute)
	}); level != HealthError {
		t.Fatalf("worker offline = %s", level)
	}
	if level, _ := eval(func(in *healthInput) {
		in.WorkerActive, in.OldestQueued, in.WorkerLastSeen = false, now.Add(-10*time.Minute), now.Add(-9*time.Minute)
	}); level != HealthOK {
		t.Fatalf("recently offline = %s", level)
	}
	// 需关注。
	failedRun := &store.Run{Status: store.RunFailed, Message: "boom"}
	if level, reason := eval(func(in *healthInput) { in.LastRun = failedRun }); level != HealthWarning || !strings.Contains(reason, "boom") {
		t.Fatalf("failed = %s %s", level, reason)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.ConsecutiveSkips = 3 }); level != HealthWarning {
		t.Fatalf("skips = %s", level)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.ConsecutiveSkips = 2 }); level != HealthOK {
		t.Fatalf("skips 2 = %s", level)
	}
	slow := &store.Run{Status: store.RunSucceeded, StartedAt: now.Add(-20 * time.Minute), FinishedAt: now.Add(-10 * time.Minute)}
	if level, reason := eval(func(in *healthInput) { in.LastRun = slow }); level != HealthWarning || !strings.Contains(reason, "一半") {
		t.Fatalf("slow = %s %s", level, reason)
	}
	// 分区到时间（日末 + 回看 + 2 个执行间隔 = 00:25）仍未导出。
	open := store.Partition{PartitionKey: "2026-10-16", RangeStart: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), RangeEnd: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
	if level, reason := eval(func(in *healthInput) { in.OpenParts = []store.Partition{open} }); level != HealthWarning || !strings.Contains(reason, "未导出") {
		t.Fatalf("overdue partition = %s %s", level, reason)
	}
	if level, _ := eval(func(in *healthInput) {
		in.Now = time.Date(2026, 10, 17, 0, 20, 0, 0, time.UTC)
		in.OpenParts = []store.Partition{open}
	}); level != HealthOK {
		t.Fatalf("not yet overdue = %s", level)
	}
	// 异常优先，原因合并。
	if level, reason := eval(func(in *healthInput) {
		in.LastRun = &store.Run{Status: store.RunFailed, Unrecoverable: unrecoverable.Unrecoverable}
	}); level != HealthError || !strings.Contains(reason, "最近一次运行失败") {
		t.Fatalf("combined = %s %s", level, reason)
	}
}

func TestOverviewReportsWorkersRunsAndAlerts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	empty, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	requireOK(t, empty.GetRetInfo())
	if empty.GetWorker() != nil || empty.GetTaskCount() != 1 || empty.GetEnabledTaskCount() != 1 || empty.GetQueuedRunCount() != 1 {
		t.Fatalf("overview = %v", empty)
	}
	if len(empty.GetAlerts()) != 1 || !strings.Contains(empty.GetAlerts()[0], "没有活动") {
		t.Fatalf("alerts = %v", empty.GetAlerts())
	}
	h.heartbeat("w1", "i1")
	h.pull("w1", "i1", 1)
	full, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if full.GetWorker().GetWorkerId() != "w1" || !full.GetWorker().GetActive() || full.GetWorker().GetRootDir() != "/data/archive" || len(full.GetRunningRuns()) != 1 || full.GetQueuedRunCount() != 0 {
		t.Fatalf("overview = %v", full)
	}
	if len(full.GetAlerts()) != 0 {
		t.Fatalf("alerts = %v", full.GetAlerts())
	}
	h.now = h.now.Add(10 * time.Minute)
	stale, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if stale.GetWorker() == nil || stale.GetWorker().GetActive() {
		t.Fatalf("租约过期的 worker 仍应展示但标为不活动: %v", stale.GetWorker())
	}
}

func TestRecreatingATaskCancelsThePendingDeletionNotice(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	deleted, _ := h.mgr.DeleteArchiveTask(ctx, &archivepb.DeleteArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, deleted.GetRetInfo())
	h.heartbeat("w1", "i1")
	if hb := h.heartbeat("w1", "i1"); len(hb.GetDeletedTasks()) != 1 {
		t.Fatalf("删除后应通知 worker: %v", hb.GetDeletedTasks())
	}
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	if hb := h.heartbeat("w1", "i1"); len(hb.GetDeletedTasks()) != 0 {
		t.Fatalf("重新创建后不能再通知 worker 清理: %v", hb.GetDeletedTasks())
	}
}
