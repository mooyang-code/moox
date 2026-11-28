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
	mu         sync.Mutex
	datasets   map[string]Dataset
	spaces     map[string]string
	calls      int
	listCalls  int
	spaceCalls int
	datasetErr error
	listErr    error
	spaceErr   error
	spaceErrs  map[string]error
}

func (f *fakeDatasets) GetDataset(_ context.Context, space, dataset string) (Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.datasetErr != nil {
		return Dataset{}, f.datasetErr
	}
	ds, ok := f.datasets[space+"/"+dataset]
	if !ok {
		return Dataset{}, fmt.Errorf("%w: %s/%s", ErrDatasetNotFound, space, dataset)
	}
	return ds, nil
}

func (f *fakeDatasets) ListDatasets(_ context.Context) ([]Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.datasetErr != nil {
		return nil, f.datasetErr
	}
	out := make([]Dataset, 0, len(f.datasets))
	for _, ds := range f.datasets {
		out = append(out, ds)
	}
	return out, nil
}

func (f *fakeDatasets) GetSpaceTimezone(_ context.Context, space string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spaceCalls++
	if err, ok := f.spaceErrs[space]; ok && err != nil {
		return "", err
	}
	if f.spaceErr != nil {
		return "", f.spaceErr
	}
	if tz, ok := f.spaces[space]; ok {
		return tz, nil
	}
	switch space {
	case "stockcn":
		return "Asia/Shanghai", nil
	case "stockhk":
		return "Asia/Hong_Kong", nil
	case "stockus":
		return "America/New_York", nil
	default:
		return "UTC", nil
	}
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
		"crypto/ds_1m":  {SpaceID: "crypto", DatasetID: "ds_1m", DataKind: "time_series", Freq: "1m", Status: "active", Retention: "168h"},
		"crypto/ds_1h":  {SpaceID: "crypto", DatasetID: "ds_1h", DataKind: "time_series", Freq: "1h", Status: "active", Retention: "forever"},
		"crypto/rec":    {SpaceID: "crypto", DatasetID: "rec", DataKind: "record", Freq: "", Status: "active", Retention: "forever"},
		"crypto/off":    {SpaceID: "crypto", DatasetID: "off", DataKind: "time_series", Freq: "1m", Status: "building", Retention: "168h"},
		"stockcn/cn_1m": {SpaceID: "stockcn", DatasetID: "cn_1m", DataKind: "time_series", Freq: "1m", Status: "active", Retention: "1080h"},
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

// createOnly 只创建任务（不生成运行；启用的任务在下一次检查时生成 follow）。
func (h *harness) createOnly(dataset string, mutate func(*archivepb.CreateArchiveTaskReq)) *archivepb.CreateArchiveTaskRsp {
	h.t.Helper()
	req := &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: dataset}
	if mutate != nil {
		mutate(req)
	}
	rsp, err := h.mgr.CreateArchiveTask(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

// create 创建任务，启用的任务再立即运行一次（生成一条排队的 manual 运行），方便后续测试直接领取。
func (h *harness) create(dataset string, mutate func(*archivepb.CreateArchiveTaskReq)) *archivepb.CreateArchiveTaskRsp {
	h.t.Helper()
	rsp := h.createOnly(dataset, mutate)
	if rsp.GetRetInfo().GetCode() == commonpb.ErrorCode_SUCCESS && rsp.GetTask().GetStatus() == store.StatusEnabled {
		run, _ := h.mgr.RunArchiveTaskNow(context.Background(), &archivepb.RunArchiveTaskNowReq{SpaceId: rsp.GetTask().GetSpaceId(), DatasetId: rsp.GetTask().GetDatasetId()})
		requireOK(h.t, run.GetRetInfo())
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
		{"inactive dataset", "off", nil, commonpb.ErrorCode_INVALID_PARAM, "状态不是 active"},
		{"forever needs backfill", "ds_1h", nil, commonpb.ErrorCode_INVALID_PARAM, "forever"},
		{"backfill in future", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-18T00:00:00Z" }, commonpb.ErrorCode_INVALID_PARAM, "早于当前时间"},
		{"backfill beyond retention", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-01T00:00:00Z" }, commonpb.ErrorCode_INVALID_PARAM, "不能早于"},
		{"missing space", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.SpaceId = "" }, commonpb.ErrorCode_INVALID_PARAM, "space_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rsp := h.createOnly(tc.ds, tc.mutate)
			requireCode(t, rsp.GetRetInfo(), tc.code, tc.text)
		})
	}
	if rsp, _ := h.mgr.ListArchiveTasks(context.Background(), &archivepb.ListArchiveTasksReq{}); len(rsp.GetTasks()) != 0 {
		t.Fatalf("失败的创建不应留下任务: %v", rsp.GetTasks())
	}
}

// D8：回补起点没填时取「现在 − 线上保留期 + 1 天」（首次运行整段读线上保留期）；保留期为 forever 时必须填写。
func TestCreateArchiveTaskDefaultsTheBackfillToTheRetentionStart(t *testing.T) {
	h := newHarness(t)
	rsp := h.createOnly("ds_1m", nil)
	requireOK(t, rsp.GetRetInfo())
	task := rsp.GetTask()
	if task.GetFreq() != "1m" || task.GetPartitionUnit() != "day" || task.GetStatus() != "enabled" || task.GetRetention() != "168h" ||
		task.GetBackfillFrom() != "2026-10-11T01:00:00Z" {
		t.Fatalf("task = %v", task)
	}
	if runs, _ := h.mgr.ListArchiveRuns(context.Background(), &archivepb.ListArchiveRunsReq{IncludeFollow: true}); len(runs.GetRuns()) != 0 {
		t.Fatalf("创建任务不立即生成运行: %v", runs.GetRuns())
	}
	dup := h.createOnly("ds_1m", nil)
	requireCode(t, dup.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "已经有归档任务")

	// 填了的回补起点原样保存（在保留期内）。
	cn := h.createOnly("cn_1m", func(r *archivepb.CreateArchiveTaskReq) { r.SpaceId, r.BackfillFrom = "stockcn", "2026-10-15T00:00:00Z" })
	requireOK(t, cn.GetRetInfo())
	if cn.GetTask().GetBackfillFrom() != "2026-10-15T00:00:00Z" {
		t.Fatalf("stockcn task = %v", cn.GetTask())
	}
	// 保留期为 forever：必须填，填了任意过去的时刻都行。
	missing := h.createOnly("ds_1h", nil)
	requireCode(t, missing.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "forever")
	forever := h.createOnly("ds_1h", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom, r.Paused = "2020-01-01T00:00:00Z", true })
	requireOK(t, forever.GetRetInfo())
	if forever.GetTask().GetStatus() != "paused" || forever.GetTask().GetBackfillFrom() != "2020-01-01T00:00:00Z" || forever.GetTask().GetRetention() != "forever" {
		t.Fatalf("forever task = %v", forever.GetTask())
	}
	h.datasets.datasets["crypto/ds_1d"] = Dataset{SpaceID: "crypto", DatasetID: "ds_1d", DataKind: "time_series", Freq: "1d", Status: "active", Retention: "forever"}
	daily := h.createOnly("ds_1d", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2020-01-01T00:00:00Z" })
	requireOK(t, daily.GetRetInfo())
	if daily.GetTask().GetPartitionUnit() != "month" {
		t.Fatalf("1d 任务按月分区: %v", daily.GetTask())
	}
}

func TestSpaceHeaderScopesTheConsoleAPI(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	rsp, _ := h.mgr.CreateArchiveTask(withSpace("stockcn"), &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1h"})
	requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "不一致")
	// 请求头里的空间足够，请求体可以不带 space_id。
	cn, _ := h.mgr.CreateArchiveTask(withSpace("stockcn"), &archivepb.CreateArchiveTaskReq{DatasetId: "cn_1m"})
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

func TestUpdateArchiveTaskOnlyChangesTheStatus(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	ctx := context.Background()
	kept, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, kept.GetRetInfo())
	if kept.GetTask().GetStatus() != "enabled" {
		t.Fatalf("空 status 不修改: %v", kept.GetTask())
	}
	badStatus, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "weird"})
	requireCode(t, badStatus.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "status")

	h.trigger(at(1, 10, 25))
	paused, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "paused"})
	requireOK(t, paused.GetRetInfo())
	if paused.GetTask().GetStatus() != "paused" {
		t.Fatalf("paused = %v", paused.GetTask())
	}
	if queued, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued"}); len(queued.GetRuns()) != 0 {
		t.Fatalf("停用取消排队中的 follow: %v", queued.GetRuns())
	}
	h.trigger(at(1, 11, 25))
	if queued, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued"}); len(queued.GetRuns()) != 0 {
		t.Fatalf("停用的任务不生成 follow: %v", queued.GetRuns())
	}
	resumed, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "enabled"})
	requireOK(t, resumed.GetRetInfo())
	h.trigger(at(1, 12, 25))
	queued, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued"})
	if len(queued.GetRuns()) != 1 || queued.GetRuns()[0].GetKind() != "follow" {
		t.Fatalf("重新启用后下一分钟生成 follow: %v", queued.GetRuns())
	}
	missing, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "nope"})
	requireCode(t, missing.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
}

func TestPrepareArchiveTaskReportsTheEarliestBackfill(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	prep, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, prep.GetRetInfo())
	if prep.GetFreq() != "1m" || prep.GetPartitionUnit() != "day" || prep.GetEarliestBackfillFrom() != "2026-10-11T01:00:00Z" || prep.GetExists() {
		t.Fatalf("prepare = %v", prep)
	}
	forever, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1h"})
	if forever.GetEarliestBackfillFrom() != "" {
		t.Fatalf("forever prepare = %v", forever)
	}
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	again, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	if !again.GetExists() {
		t.Fatal("已有任务时 exists 应为真")
	}
}

func TestBackfillAndRunNowRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.Paused = true }).GetRetInfo())
	run, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, run.GetRetInfo())
	again, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireCode(t, again.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "排队中的 manual 运行")
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
		"too old":    {"2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z", "线上保留期 + 1 天"},
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

func (h *harness) heartbeat(worker string, running ...string) *archivepb.ArchiveWorkerHeartbeatRsp {
	h.t.Helper()
	return h.heartbeatOpts(worker, false, running...)
}

func (h *harness) heartbeatWithTasks(worker string, running ...string) *archivepb.ArchiveWorkerHeartbeatRsp {
	h.t.Helper()
	return h.heartbeatOpts(worker, true, running...)
}

func (h *harness) heartbeatOpts(worker string, includeTasks bool, running ...string) *archivepb.ArchiveWorkerHeartbeatRsp {
	h.t.Helper()
	rsp, err := h.worker.ArchiveWorkerHeartbeat(context.Background(), &archivepb.ArchiveWorkerHeartbeatReq{
		WorkerId: worker, Version: "test", RootDir: "/data/archive", DiskTotalBytes: 100, DiskFreeBytes: 60, DiskUsedBytes: 35,
		MinFreeBytes: 20, RunningRunIds: running, IncludeTasks: includeTasks,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

func (h *harness) pull(slots uint32) []*archivepb.RunAssignment {
	h.t.Helper()
	rsp, err := h.worker.PullArchiveRun(context.Background(), &archivepb.PullArchiveRunReq{FreeSlots: slots, MaxStorageRuns: 2})
	if err != nil {
		h.t.Fatal(err)
	}
	requireOK(h.t, rsp.GetRetInfo())
	return rsp.GetRuns()
}

func TestWorkerLeaseAndPullFlow(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-15T00:00:00Z" }).GetRetInfo())
	hb := h.heartbeat("w1")
	requireOK(t, hb.GetRetInfo())
	// worker 重启（新进程、同一身份）后再心跳照常成功，不用等旧心跳过期就能领取。
	requireOK(t, h.heartbeat("w1").GetRetInfo())

	runs := h.pull(2)
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	run := runs[0]
	if run.GetLeaseToken() == "" || run.GetKind() != "manual" || run.GetFreq() != "1m" || run.GetPartitionUnit() != "day" ||
		run.GetAllowedLatenessSeconds() != 900 || run.GetStagingRetentionSeconds() != 72*3600 || run.GetSpaceTz() != "UTC" || run.GetLocalClose() != "" ||
		run.GetBackfillFrom() != "2026-10-15T00:00:00Z" || run.GetStartedAt() != rfcMillis("2026-10-17T01:00:00Z") || run.GetLeaseTtlSeconds() != 600 ||
		run.GetPurpose() != "data" || run.GetDirName() != "ds-1m" {
		t.Fatalf("assignment = %v", run)
	}
	if more := h.pull(2); len(more) != 0 {
		t.Fatalf("同一任务同时只有一个运行: %v", more)
	}

	progress, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{
		RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), SubjectsDone: 2, RequestCount: 3, BytesRead: 100, RowsRead: 40,
	})
	requireOK(t, progress.GetRetInfo())
	if progress.GetCanceled() {
		t.Fatal("不应取消")
	}
	wrong, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{RunId: run.GetRunId(), LeaseToken: "x"})
	if !wrong.GetCanceled() {
		t.Fatal("租约不对应视为取消")
	}
	empty, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{})
	requireCode(t, empty.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "不能为空")

	// 用户取消：下一次心跳让 worker 停止这条运行。
	cancel, _ := h.mgr.CancelArchiveRun(context.Background(), &archivepb.CancelArchiveRunReq{RunId: run.GetRunId()})
	requireOK(t, cancel.GetRetInfo())
	hb = h.heartbeat("w1", run.GetRunId())
	if len(hb.GetCancelRunIds()) != 1 || hb.GetCancelRunIds()[0] != run.GetRunId() {
		t.Fatalf("heartbeat = %v", hb)
	}
	gone, _ := h.worker.ReportArchiveRunProgress(context.Background(), &archivepb.ReportArchiveRunProgressReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken()})
	if !gone.GetCanceled() {
		t.Fatal("取消后进度上报应返回 canceled")
	}
}

func TestCompleteArchiveRunRegistersPartitionsAndRejectsBadReports(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1")
	run := h.pull(1)[0]
	ctx := context.Background()
	base := func() *archivepb.CompleteArchiveRunReq {
		return &archivepb.CompleteArchiveRunReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded"}
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
	good.SubjectsDone, good.RowsRead, good.RowsAdded, good.PartitionsExported = 2, 100, 90, 1
	good.FinalUntil = "2026-10-14T00:00:00Z"
	good.Unrecoverable = []*archivepb.UnrecoverableRange{{SubjectId: "BTC-USDT", StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}}
	good.Partitions = []*archivepb.PartitionReport{{
		PartitionKey: "2026-10-16", Status: "exported", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z", RowCount: 100, SubjectCount: 2,
		FilePath: "crypto/ds_1m/2026-10-16.csv.gz", FileBytes: 10, FileSha256: "abc", Columns: []string{"open", "close"}, ExportCount: 1, ExportedAt: rfcMillis("2026-10-17T01:05:00Z"),
	}}
	rsp, _ := h.worker.CompleteArchiveRun(ctx, good)
	requireOK(t, rsp.GetRetInfo())
	again, _ := h.worker.CompleteArchiveRun(ctx, good)
	requireCode(t, again.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "")

	parts, _ := h.mgr.ListArchivePartitions(context.Background(), &archivepb.ListArchivePartitionsReq{SpaceId: "crypto", DatasetId: "ds_1m", FromMonth: "2026-10", ToMonth: "2026-10"})
	requireOK(t, parts.GetRetInfo())
	if len(parts.GetPartitions()) != 1 || parts.GetPartitions()[0].GetFileSha256() != "abc" {
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
	// 换了 worker_id（换机器）之后，任务照样能领取，下发的登记带着已有分区，上报被接受。
	h.now = h.now.Add(2 * time.Minute)
	h.heartbeat("w2")
	runs := h.pull(1)
	if len(runs) != 1 {
		t.Fatalf("换了 worker_id 之后任务也应能领取: %v", runs)
	}
	if regs := runs[0].GetRegisteredPartitions(); len(regs) != 1 || regs[0].GetFileSha256() != "abc" {
		t.Fatalf("下发的登记 = %v", regs)
	}
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: runs[0].GetRunId(), LeaseToken: runs[0].GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{{PartitionKey: "2026-10-16", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z", Status: "exported", RowCount: 100, SubjectCount: 2,
			FilePath: "crypto/ds_1m/2026-10-16.csv.gz", FileSha256: "abc", ExportedAt: rfcMillis("2026-10-17T01:05:00Z")}}})
	requireOK(t, done.GetRetInfo())
	part, _ := h.mgr.GetArchivePartition(context.Background(), &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "2026-10-16"})
	if part.GetPartition().GetFileBytes() != 10 {
		t.Fatalf("大小不应被冲成 0: %v", part.GetPartition())
	}
}

func TestDeleteArchiveTaskNotifiesTheWorkerAndKeepsPartitionedTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	requireOK(t, h.create("ds_1h", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-01-01T00:00:00Z" }).GetRetInfo())
	h.heartbeat("w1")
	run := h.pull(1)[0]
	done := &archivepb.CompleteArchiveRunReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded", Partitions: []*archivepb.PartitionReport{{
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
	// 常规心跳不查全量任务列表；对账心跳（include_tasks=true）带回当前全部任务，worker 据此清理已删除任务的暂存状态。
	if plain := h.heartbeat("w1"); len(plain.GetTasks()) != 0 {
		t.Fatalf("常规心跳不应返回任务列表: %v", plain.GetTasks())
	}
	hb := h.heartbeatWithTasks("w1")
	if len(hb.GetTasks()) != 1 || hb.GetTasks()[0].GetDatasetId() != partitioned || hb.GetTasks()[0].GetTaskId() == "" {
		t.Fatalf("heartbeat = %v", hb)
	}
}

func TestMaintainRecoversLostLeases(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	// 领取后 worker 失联（心跳也停了）：租约过期，运行被标为失败，任务健康提示失败。
	h.heartbeat("w1")
	claimed := h.pull(1)
	if len(claimed) != 1 {
		t.Fatalf("claimed = %v", claimed)
	}
	h.now = h.now.Add(11 * time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	failed, _ := h.mgr.GetArchiveRun(ctx, &archivepb.GetArchiveRunReq{RunId: claimed[0].GetRunId()})
	if failed.GetRun().GetStatus() != "failed" || failed.GetRun().GetMessage() != store.LeaseExpiredWorkerOffline {
		t.Fatalf("run = %v", failed.GetRun())
	}
	list, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{})
	if list.GetTasks()[0].GetHealth() != HealthWarning || !strings.Contains(list.GetTasks()[0].GetHealthReason(), "最近一次运行失败") {
		t.Fatalf("task health = %s %s", list.GetTasks()[0].GetHealth(), list.GetTasks()[0].GetHealthReason())
	}
}

// 领取的响应没送到 worker：mgr 已记开始，worker 照常心跳却不知道这条运行，租约过期时不能说成“worker 失联”。
func TestMaintainTellsALostClaimFromALostWorker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1")
	claimed := h.pull(1)
	if len(claimed) != 1 {
		t.Fatalf("claimed = %v", claimed)
	}
	h.now = h.now.Add(11 * time.Minute)
	h.heartbeat("w1") // worker 一直在心跳，心跳里没有这条运行
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	failed, _ := h.mgr.GetArchiveRun(ctx, &archivepb.GetArchiveRunReq{RunId: claimed[0].GetRunId()})
	if failed.GetRun().GetStatus() != "failed" || failed.GetRun().GetMessage() != store.LeaseExpiredWorkerOnline {
		t.Fatalf("run = %v", failed.GetRun())
	}
}

// at 返回 2026-10-17 的 UTC 时刻。
func at(h, m, sec int) time.Time { return time.Date(2026, 10, 17, h, m, sec, 0, time.UTC) }

// finishQueued 让 worker 领取并成功完成全部排队中的运行，返回下发的参数。
func (h *harness) finishQueued() []*archivepb.RunAssignment {
	h.t.Helper()
	h.heartbeat("w1")
	runs := h.pull(10)
	for _, run := range runs {
		rsp, _ := h.worker.CompleteArchiveRun(context.Background(), &archivepb.CompleteArchiveRunReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded"})
		requireOK(h.t, rsp.GetRetInfo())
	}
	return runs
}

func (h *harness) trigger(now time.Time) {
	h.t.Helper()
	h.now = now
	if err := h.svc.Trigger(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

// allRuns 按计划时刻倒序列出全部运行（含成功的 follow）。
func (h *harness) allRuns() []*archivepb.ArchiveRun {
	h.t.Helper()
	rsp, _ := h.mgr.ListArchiveRuns(context.Background(), &archivepb.ListArchiveRunsReq{IncludeFollow: true, Page: &commonpb.Page{Size: 200}})
	return rsp.GetRuns()
}

func (h *harness) task(space, dataset string) store.Task {
	h.t.Helper()
	task, err := h.store.GetTask(context.Background(), space, dataset)
	if err != nil {
		h.t.Fatal(err)
	}
	return task
}

// 每分钟给启用、空闲的任务生成一条 follow；整点窗口内照常生成；follow 成功推进最近成功时间，成功的只保留 24 小时。
func TestTriggerGeneratesOneFollowRunPerIdleTaskEveryMinute(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.createOnly("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-15T00:00:00Z" }).GetRetInfo())
	h.datasets.datasets["crypto/ds_1d"] = Dataset{SpaceID: "crypto", DatasetID: "ds_1d", DataKind: "time_series", Freq: "1d", Status: "active", Retention: "forever"}
	requireOK(t, h.createOnly("ds_1d", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-01-01T00:00:00Z" }).GetRetInfo())

	// 整点窗口内（01:59:25）照常生成，日线任务同样每分钟一条。
	h.trigger(at(1, 59, 25))
	runs := h.allRuns()
	if len(runs) != 2 || runs[0].GetKind() != "follow" || runs[1].GetKind() != "follow" || runs[0].GetScheduledAt() != rfcMillis("2026-10-17T01:59:25Z") || runs[0].GetRangeStart() != "" {
		t.Fatalf("首次检查 = %v", runs)
	}
	// 上一条还在排队：同一分钟或下一分钟都不再生成。
	h.trigger(at(2, 0, 25))
	if len(h.allRuns()) != 2 {
		t.Fatal("有排队中的运行时不应生成 follow")
	}
	h.heartbeat("w1")
	follow := h.pull(10)
	if len(follow) != 2 || follow[0].GetKind() != "follow" || follow[0].GetAllowedLatenessSeconds() != 900 || follow[0].GetRangeStart() != "" {
		t.Fatalf("follow 下发 = %v", follow)
	}
	// 运行中也不生成。
	h.trigger(at(2, 1, 25))
	if len(h.allRuns()) != 2 {
		t.Fatal("有运行中的运行时不应生成 follow")
	}
	for _, run := range follow {
		rsp, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded"})
		requireOK(t, rsp.GetRetInfo())
	}
	if task := h.task("crypto", "ds_1m"); !task.LastSuccessAt.Equal(at(2, 0, 25)) || !task.BackfillFrom.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("follow 成功推进最近成功时间、不动回补起点: %+v", task)
	}
	h.trigger(at(2, 2, 25))
	h.finishQueued()
	if len(h.allRuns()) != 4 {
		t.Fatalf("下一分钟再生成: %v", h.allRuns())
	}
	// 运行列表默认不列成功的 follow。
	if listed, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{}); len(listed.GetRuns()) != 0 {
		t.Fatalf("默认列表 = %v", listed.GetRuns())
	}
	// 成功的 follow 只保留 24 小时。
	h.now = at(2, 10, 0).Add(24 * time.Hour)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if left := h.allRuns(); len(left) != 0 {
		t.Fatalf("超过 24 小时的成功 follow 应被清理: %v", left)
	}
}

func TestMaintainRefreshesDatasetInfoOncePerHour(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.datasets.mu.Lock()
	ds := h.datasets.datasets["crypto/ds_1m"]
	ds.Retention = "336h"
	h.datasets.datasets["crypto/ds_1m"] = ds
	h.datasets.mu.Unlock()
	callsBefore := h.datasets.calls
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	task, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if task.Retention != "336h" || h.datasets.calls != callsBefore+1 {
		t.Fatalf("task = %+v calls=%d", task, h.datasets.calls)
	}
	if err := h.svc.Maintain(ctx); err != nil || h.datasets.calls != callsBefore+1 {
		t.Fatalf("一小时内不重复刷新: calls=%d err=%v", h.datasets.calls, err)
	}
	h.now = h.now.Add(61 * time.Minute)
	h.datasets.datasetErr = fmt.Errorf("storage unavailable")
	if err := h.svc.Maintain(ctx); err == nil {
		t.Fatal("存储临时错误要返回错误，下一轮重试")
	}
	task, _ = h.store.GetTask(ctx, "crypto", "ds_1m")
	if task.SourceMissing || task.BlockedReason != "" {
		t.Fatalf("存储临时错误不能把任务标成源数据集不存在: %+v", task)
	}
}

// 源数据集不存在的完整生命周期：网络错误不动任务；Storage 明确返回不存在时记“源数据集不存在”、停生成 follow、不停用任务、不退回每分钟重试；
// 同名数据集重现后自动恢复跟随，人工停用的任务保持停用。
func TestMaintainMarksSourceDatasetMissingAndRecoversWhenDatasetReappears(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// 本用例只聚焦 ds_1m 与 ds_1h 的源数据集缺失生命周期，去掉其他无关夹具数据集以免自动补齐干扰计数。
	delete(h.datasets.datasets, "stockcn/cn_1m")
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	requireOK(t, h.create("ds_1h", func(r *archivepb.CreateArchiveTaskReq) {
		r.Paused = true
		r.BackfillFrom = "2026-10-01T00:00:00Z"
	}).GetRetInfo())
	h.heartbeat("w1")
	h.finishQueued()

	// 先完成一轮正常刷新，并排入一条待领的 follow。
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	h.trigger(h.now.Add(time.Minute))
	if active, _ := h.store.ListActiveRuns(ctx, "crypto"); len(active) != 1 || active[0].Kind != store.KindFollow {
		t.Fatalf("应有一条排队中的 follow: %+v", active)
	}

	// 1. Storage 临时错误：不标记“源数据集不存在”，不取消 follow。
	h.now = h.now.Add(61 * time.Minute)
	h.datasets.datasetErr = fmt.Errorf("dial tcp: connection refused")
	if err := h.svc.Maintain(ctx); err == nil {
		t.Fatal("网络错误应返回失败并等待重试")
	}
	if task, _ := h.store.GetTask(ctx, "crypto", "ds_1m"); task.SourceMissing || task.BlockedReason != "" {
		t.Fatalf("网络错误不能标源数据集不存在: %+v", task)
	}
	if active, _ := h.store.ListActiveRuns(ctx, "crypto"); len(active) != 1 {
		t.Fatalf("网络错误不能取消排队中的 follow: %+v", active)
	}
	h.datasets.datasetErr = nil

	// 2. Storage 明确返回“不存在”：标记任务、取消排队 follow、不再生成 follow，且不停用任务、不算刷新失败。
	orig1m := h.datasets.datasets["crypto/ds_1m"]
	orig1h := h.datasets.datasets["crypto/ds_1h"]
	delete(h.datasets.datasets, "crypto/ds_1m")
	delete(h.datasets.datasets, "crypto/ds_1h")
	callsBefore := h.datasets.calls
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatalf("明确数据集不存在不应触发刷新报错与每分钟重试: %v", err)
	}
	if h.datasets.calls != callsBefore+2 {
		t.Fatalf("calls = %d, want %d", h.datasets.calls, callsBefore+2)
	}
	// 1 分钟后再 Maintain：不会每分钟重刷 Storage。
	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if h.datasets.calls != callsBefore+2 {
		t.Fatalf("一小时内不应重复刷新 Storage: calls=%d", h.datasets.calls)
	}
	// 原来排队中的 follow 已被取消，之后 Trigger 也不再为它生成 follow。
	if active, _ := h.store.ListActiveRuns(ctx, "crypto"); len(active) != 0 {
		t.Fatalf("排队中的 follow 应已取消: %+v", active)
	}
	h.trigger(h.now.Add(time.Minute))
	if active, _ := h.store.ListActiveRuns(ctx, "crypto"); len(active) != 0 {
		t.Fatalf("源数据集不存在时不应生成 follow: %+v", active)
	}
	// 任务启停状态不变，健康等级为 error（“源数据集不存在”），人工停用的任务保持 paused；接口 blocked_reason 为空。
	task1m, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	task1h, _ := h.store.GetTask(ctx, "crypto", "ds_1h")
	if task1m.Status != store.StatusEnabled || !task1m.SourceMissing || task1m.BlockedReason != "" {
		t.Fatalf("ds_1m 应保持 enabled 并标记源数据集不存在: %+v", task1m)
	}
	if task1h.Status != store.StatusPaused || !task1h.SourceMissing || task1h.BlockedReason != "" {
		t.Fatalf("ds_1h 应保持 paused 并标记源数据集不存在: %+v", task1h)
	}
	list, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{SpaceId: "crypto"})
	byID := map[string]*archivepb.ArchiveTask{}
	for _, item := range list.GetTasks() {
		byID[item.GetDatasetId()] = item
	}
	if got := byID["ds_1m"]; got.GetStatus() != store.StatusEnabled || got.GetHealth() != HealthError ||
		got.GetHealthReason() != "源数据集不存在" || got.GetBlockedReason() != "" {
		t.Fatalf("ds_1m 视图 = %v", got)
	}
	if got := byID["ds_1h"]; got.GetStatus() != store.StatusPaused || got.GetHealth() != HealthPaused {
		t.Fatalf("ds_1h 视图 = %v", got)
	}
	h.heartbeat("w1")
	overview, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if len(overview.GetAlerts()) != 1 || overview.GetAlerts()[0] != "ds_1m：源数据集不存在" {
		t.Fatalf("overview alerts = %v", overview.GetAlerts())
	}

	// 3. 数据集重新出现：1 小时后刷新自动清掉标记、恢复生成 follow，原本人工停用的任务仍保持停用。
	orig1m.Retention = "336h"
	h.datasets.datasets["crypto/ds_1m"] = orig1m
	h.datasets.datasets["crypto/ds_1h"] = orig1h
	h.now = h.now.Add(61 * time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	task1m, _ = h.store.GetTask(ctx, "crypto", "ds_1m")
	task1h, _ = h.store.GetTask(ctx, "crypto", "ds_1h")
	if task1m.Status != store.StatusEnabled || task1m.SourceMissing || task1m.Retention != "336h" {
		t.Fatalf("ds_1m 重现后应清掉标记并刷新保留期: %+v", task1m)
	}
	if task1h.Status != store.StatusPaused || task1h.SourceMissing {
		t.Fatalf("ds_1h 重现后应清掉标记但保持人工停用: %+v", task1h)
	}
	h.trigger(h.now.Add(time.Minute))
	active, _ := h.store.ListActiveRuns(ctx, "crypto")
	if len(active) != 1 || active[0].DatasetID != "ds_1m" || active[0].Kind != store.KindFollow {
		t.Fatalf("ds_1m 应恢复生成 follow，人工停用的 ds_1h 不生成: %+v", active)
	}
}

// 维护定时器自动为活跃、有限保留期的时序数据集补齐归档任务；永久保留、记录表、非 active 不补齐；
// 人工停用的任务保持停用、不重建；手工删除的任务会被重新补齐。
func TestMaintainAutoEnsuresEligibleDatasetsAndRespectsPausedTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// 初始没有任何任务：Maintain 应只补齐 crypto/ds_1m 与 stockcn/cn_1m（ds_1h 为 forever、rec 为 record、off 为 building）。
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if issues := h.svc.EnsureIssues(); len(issues) != 0 {
		t.Fatalf("不应有自动补齐关注项: %v", issues)
	}
	tasks, total, err := h.store.ListTasks(ctx, "", 100, 0)
	if err != nil || total != 2 {
		t.Fatalf("tasks = %+v total=%d err=%v", tasks, total, err)
	}
	if tasks[0].SpaceID != "crypto" || tasks[0].DatasetID != "ds_1m" || tasks[0].Status != store.StatusEnabled ||
		tasks[1].SpaceID != "stockcn" || tasks[1].DatasetID != "cn_1m" || tasks[1].Status != store.StatusEnabled {
		t.Fatalf("自动补齐的任务不符: %+v", tasks)
	}

	// 停用 crypto/ds_1m：下一轮 Maintain 不重建，也不改回启用。
	upd, err := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: store.StatusPaused})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, upd.GetRetInfo())
	pausedID := h.task("crypto", "ds_1m").ID

	// 删除 stockcn/cn_1m：下一轮 Maintain 应重新补齐。
	del, err := h.mgr.DeleteArchiveTask(ctx, &archivepb.DeleteArchiveTaskReq{SpaceId: "stockcn", DatasetId: "cn_1m"})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, del.GetRetInfo())

	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	ds1m := h.task("crypto", "ds_1m")
	if ds1m.Status != store.StatusPaused || ds1m.ID != pausedID {
		t.Fatalf("停用的任务不应重建或恢复启用: %+v", ds1m)
	}
	cn1m := h.task("stockcn", "cn_1m")
	if cn1m.Status != store.StatusEnabled {
		t.Fatalf("手工删除的任务应自动重建并启用: %+v", cn1m)
	}
}

// 自动补齐遇到目录名冲突、不支持的频率或 Storage 清单不可用时记录关注项；不影响其他符合条件的数据集，修复后关注项清空。
func TestMaintainAutoEnsureReportsAttentionOnConflictAndStorageFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.datasets.datasets = map[string]Dataset{
		"crypto/ds_a":   {SpaceID: "crypto", DatasetID: "ds_a", DataKind: "time_series", Freq: "1m", Status: "active", Retention: "168h", DataSourceID: "binance", Attributes: map[string]string{"dataset_role": "raw_collection", "market_type": "spot"}},
		"crypto/ds_b":   {SpaceID: "crypto", DatasetID: "ds_b", DataKind: "time_series", Freq: "1m", Status: "active", Retention: "168h", DataSourceID: "binance", Attributes: map[string]string{"dataset_role": "raw_collection", "market_type": "spot"}},
		"crypto/ds_bad": {SpaceID: "crypto", DatasetID: "ds_bad", DataKind: "time_series", Freq: "badfreq", Status: "active", Retention: "168h"},
	}

	// 1. ds_a 成功占用目录名 binance-spot-kline-1m；ds_b 撞目录名、ds_bad 频率不合法，两者进关注项。
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.GetTask(ctx, "crypto", "ds_a"); err != nil {
		t.Fatalf("无冲突的 ds_a 应已补齐: %v", err)
	}
	issues := h.svc.EnsureIssues()
	if len(issues) != 2 {
		t.Fatalf("期望 2 条自动补齐问题，实际: %v", issues)
	}
	joined := strings.Join(issues, "；")
	if !strings.Contains(joined, "crypto/ds_b：目录名 binance-spot-kline-1m 已被 ds_a 使用") ||
		!strings.Contains(joined, "crypto/ds_bad：") {
		t.Fatalf("关注项文案不对: %s", joined)
	}

	// 2. 人工为 ds_b 指定不冲突的目录名建好任务，并修正 ds_bad 频率后，下一轮自动补齐成功并清空关注项。
	requireOK(t, h.createOnly("ds_b", func(r *archivepb.CreateArchiveTaskReq) { r.DirName = "binance-spot-kline-1m-v2" }).GetRetInfo())
	bad := h.datasets.datasets["crypto/ds_bad"]
	bad.Freq = "5m"
	h.datasets.datasets["crypto/ds_bad"] = bad

	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.GetTask(ctx, "crypto", "ds_bad"); err != nil {
		t.Fatalf("修正频率后的 ds_bad 应已补齐: %v", err)
	}
	if issues := h.svc.EnsureIssues(); len(issues) != 0 {
		t.Fatalf("全部补齐后关注项应清空: %v", issues)
	}

	// 3. Storage ListDatasets 单次或短时失败不立刻报关注项，连续失败达到阈值（3 次）后上报，恢复后清空并重置计数。
	h.datasets.listErr = fmt.Errorf("storage dial timeout")
	for round := 1; round < listDatasetsFailAlertThreshold; round++ {
		h.now = h.now.Add(time.Minute)
		if err := h.svc.Maintain(ctx); err != nil {
			t.Fatal(err)
		}
		if issues := h.svc.EnsureIssues(); len(issues) != 0 {
			t.Fatalf("清单读取第 %d 次瞬时失败不应立刻报关注项: %v", round, issues)
		}
	}
	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if issues := h.svc.EnsureIssues(); len(issues) != 1 || !strings.Contains(issues[0], "读取 Storage 数据集清单失败") {
		t.Fatalf("清单连续失败达阈值后应记录关注项: %v", issues)
	}
	h.datasets.listErr = nil
	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if issues := h.svc.EnsureIssues(); len(issues) != 0 {
		t.Fatalf("Storage 恢复后关注项应清空: %v", issues)
	}
	// 恢复后再发生单次失败仍不立刻告警。
	h.datasets.listErr = fmt.Errorf("transient error")
	h.now = h.now.Add(time.Minute)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if issues := h.svc.EnsureIssues(); len(issues) != 0 {
		t.Fatalf("恢复后再次单次失败不应立刻报关注项: %v", issues)
	}
}

func TestHealthLevelsFollowTheDesignTable(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	base := store.Task{SpaceID: "crypto", DatasetID: "ds", Freq: "1m", PartitionUnit: "day", Retention: "168h", Status: store.StatusEnabled,
		CreatedAt: now.Add(-24 * time.Hour), LastSuccessAt: now.Add(-10 * time.Minute)}
	eval := func(mutate func(*healthInput)) (string, string) {
		in := healthInput{Now: now, Task: base, Lateness: 15 * time.Minute, SpaceTZ: "UTC"}
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
	// 源数据集不存在：直接报异常“源数据集不存在”，不叠加因停 follow 产生的保留期与跟随超时噪音，不带“分区登记被拒”前缀。
	if level, reason := eval(func(in *healthInput) {
		in.Task.SourceMissing = true
		in.Task.LastSuccessAt = now.Add(-200 * time.Hour)
	}); level != HealthError || reason != "源数据集不存在" {
		t.Fatalf("source missing = %s %q", level, reason)
	}
	// 同时存在源数据集不存在与分区登记被拒时，两条独立异常原因都应呈现。
	if level, reason := eval(func(in *healthInput) {
		in.Task.SourceMissing = true
		in.Task.BlockedReason = "行数倒退"
	}); level != HealthError || !strings.Contains(reason, "源数据集不存在") || !strings.Contains(reason, "分区登记被拒：行数倒退") {
		t.Fatalf("source missing + blocked = %s %q", level, reason)
	}
	// 168h 保留期：最近成功超过 120h（保留期 − 2 天）→ 异常。
	if level, reason := eval(func(in *healthInput) { in.Task.LastSuccessAt = now.Add(-121 * time.Hour) }); level != HealthError || !strings.Contains(reason, "保留期") {
		t.Fatalf("stale success = %s %s", level, reason)
	}
	// 从未成功过：按创建时间算。
	if level, _ := eval(func(in *healthInput) {
		in.Task.LastSuccessAt = time.Time{}
		in.Task.CreatedAt = now.Add(-130 * time.Hour)
	}); level != HealthError {
		t.Fatalf("never succeeded = %s", level)
	}
	// 保留期不限的任务不按保留期判断（仍有“最近成功跟随超过 30 分钟”的需关注）。
	if level, reason := eval(func(in *healthInput) {
		in.Task.Retention = "forever"
		in.Task.LastSuccessAt = now.Add(-1000 * time.Hour)
	}); level != HealthWarning || strings.Contains(reason, "保留期") {
		t.Fatalf("forever = %s %s", level, reason)
	}
	// 无法补齐的区间按任务累积：不论最近一次运行有没有，都一直显示异常。
	if level, _ := eval(func(in *healthInput) { in.Task.Unrecoverable = []store.Unrecoverable{{StartTime: "a", EndTime: "b"}} }); level != HealthError {
		t.Fatalf("unrecoverable = %s", level)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.Retention = "" }); level != HealthError {
		t.Fatalf("读不到保留期时应显示异常，不能当永久: %s", level)
	}
	// 有到期运行时 worker 离线超过 30 分钟 → 异常；30 分钟内只是排队。
	if level, _ := eval(func(in *healthInput) {
		in.OldestQueued, in.WorkerLastSeen = now.Add(-45*time.Minute), now.Add(-40*time.Minute)
	}); level != HealthError {
		t.Fatalf("worker offline = %s", level)
	}
	if level, _ := eval(func(in *healthInput) {
		in.OldestQueued, in.WorkerLastSeen = now.Add(-10*time.Minute), now.Add(-9*time.Minute)
	}); level != HealthOK {
		t.Fatalf("recently offline = %s", level)
	}
	// 需关注：最近一次运行（任何种类）失败。
	if level, reason := eval(func(in *healthInput) {
		in.LastRun = &store.Run{Kind: store.KindRange, Status: store.RunFailed, Message: "boom"}
	}); level != HealthWarning || !strings.Contains(reason, "最近一次运行失败：boom") {
		t.Fatalf("failed = %s %s", level, reason)
	}
	// 需关注：最近一次成功的跟随距今超过 30 分钟；从未成功时按创建时间。
	if level, reason := eval(func(in *healthInput) { in.Task.LastSuccessAt = now.Add(-31 * time.Minute) }); level != HealthWarning || !strings.Contains(reason, "跟随") {
		t.Fatalf("follow stale = %s %s", level, reason)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.LastSuccessAt = now.Add(-29 * time.Minute) }); level != HealthOK {
		t.Fatalf("follow fresh = %s", level)
	}
	if level, _ := eval(func(in *healthInput) {
		in.Task.LastSuccessAt, in.Task.CreatedAt = time.Time{}, now.Add(-10*time.Minute)
	}); level != HealthOK {
		t.Fatalf("刚创建的任务 = %s", level)
	}
	// 需关注：分区到时间（日末 + 允许迟到 15 分钟）超过 1 小时（01:15）仍未导出。
	open := store.Partition{PartitionKey: "2026-10-16", RangeStart: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), RangeEnd: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
	if level, reason := eval(func(in *healthInput) { in.OpenParts = []store.Partition{open} }); level != HealthWarning || !strings.Contains(reason, "超过 1 小时仍未导出") {
		t.Fatalf("overdue partition = %s %s", level, reason)
	}
	if level, _ := eval(func(in *healthInput) {
		in.Now = time.Date(2026, 10, 17, 1, 10, 0, 0, time.UTC)
		in.Task.LastSuccessAt = in.Now.Add(-time.Minute)
		in.OpenParts = []store.Partition{open}
	}); level != HealthOK {
		t.Fatalf("not yet overdue = %s", level)
	}
	// 整段重读进行中（最近一次成功的 follow 消息带重读进度）不判到期未导出；最近一次运行是排队中的 follow 也一样。
	if level, reason := eval(func(in *healthInput) {
		in.OpenParts = []store.Partition{open}
		in.LastRun = &store.Run{Kind: store.KindFollow, Status: store.RunQueued}
		in.LastFollow = &store.Run{Kind: store.KindFollow, Status: store.RunSucceeded, Message: "整段重读进度：已读到 2026-10-12T00:00:00Z，截止 2026-10-17T11:00:00Z"}
	}); level != HealthOK {
		t.Fatalf("重读进行中不判未导出 = %s %s", level, reason)
	}
	// 异常优先，原因合并。
	if level, reason := eval(func(in *healthInput) {
		in.LastRun = &store.Run{Status: store.RunFailed}
		in.Task.Unrecoverable = []store.Unrecoverable{{StartTime: "a", EndTime: "b"}}
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
	if len(empty.GetAlerts()) != 1 || !strings.Contains(empty.GetAlerts()[0], "不在线") {
		t.Fatalf("alerts = %v", empty.GetAlerts())
	}
	h.heartbeat("w1")
	h.pull(1)
	full, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if full.GetWorker().GetWorkerId() != "w1" || !full.GetWorker().GetActive() || full.GetWorker().GetRootDir() != "/data/archive" || len(full.GetRunningRuns()) != 1 || full.GetQueuedRunCount() != 0 ||
		full.GetWorker().GetDiskTotalBytes() != 100 || full.GetWorker().GetDiskFreeBytes() != 60 || full.GetWorker().GetDiskUsedBytes() != 35 || full.GetWorker().GetMinFreeBytes() != 20 {
		t.Fatalf("overview = %v", full)
	}
	if len(full.GetAlerts()) != 0 {
		t.Fatalf("alerts = %v", full.GetAlerts())
	}
	h.now = h.now.Add(10 * time.Minute)
	stale, _ := h.mgr.GetArchiveOverview(ctx, &archivepb.GetArchiveOverviewReq{})
	if stale.GetWorker() == nil || stale.GetWorker().GetActive() {
		t.Fatalf("停止心跳的 worker 仍应展示但标为不在线: %v", stale.GetWorker())
	}
}

func TestRecreatedTaskGetsANewTaskID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1")
	first := h.heartbeatWithTasks("w1").GetTasks()[0].GetTaskId()
	deleted, _ := h.mgr.DeleteArchiveTask(ctx, &archivepb.DeleteArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, deleted.GetRetInfo())
	if hb := h.heartbeatWithTasks("w1"); len(hb.GetTasks()) != 0 {
		t.Fatalf("删除后任务列表应为空: %v", hb)
	}
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	hb := h.heartbeatWithTasks("w1")
	if len(hb.GetTasks()) != 1 || hb.GetTasks()[0].GetTaskId() == first {
		t.Fatalf("重建的任务必须换新的 task_id，worker 才会丢弃旧状态: %v（旧 %s）", hb.GetTasks(), first)
	}
}

func TestShrinkingPartitionReportIsRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1")
	complete := func(rows, subjects uint64) *commonpb.RetInfo {
		run := h.pull(1)[0]
		r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded",
			Partitions: []*archivepb.PartitionReport{{PartitionKey: "2026-10-16", Status: "exported", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z",
				FilePath: "f", FileSha256: "abc", RowCount: rows, SubjectCount: subjects}}})
		return r.GetRetInfo()
	}
	requireOK(t, complete(1000, 10))
	again, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, again.GetRetInfo())
	requireCode(t, complete(400, 10), commonpb.ErrorCode_CONFLICT, "")
	p, _ := h.store.GetPartition(ctx, "crypto", "ds_1m", "2026-10-16")
	if p.RowCount != 1000 {
		t.Fatalf("被拒的上报不能改动分区登记: %+v", p)
	}
	runs, _, _ := h.store.ListRuns(ctx, "crypto", "ds_1m", "failed", true, 10, 0)
	if len(runs) != 1 || !strings.Contains(runs[0].Message, "从 1000 降到 400") {
		t.Fatalf("被拒的运行应记为失败并写明原因: %+v", runs)
	}
	list, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{SpaceId: "crypto"})
	if got := list.GetTasks()[0]; got.GetHealth() != HealthError || !strings.Contains(got.GetBlockedReason(), "从 1000 降到 400") {
		t.Fatalf("登记被拒应直接算异常: %v", got)
	}
	// 不带分区的上报（例如排队时被取消）不能清掉被拒原因。
	empty, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, empty.GetRetInfo())
	emptyRun := h.pull(1)[0]
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: emptyRun.GetRunId(), LeaseToken: emptyRun.GetLeaseToken(), Status: "canceled"})
	requireOK(t, r.GetRetInfo())
	if task, _ := h.store.GetTask(ctx, "crypto", "ds_1m"); task.BlockedReason == "" {
		t.Fatal("不带分区的上报清掉了被拒原因")
	}
	// 读回合并后行数不变或变多：照常登记，并清掉被拒原因。
	third, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, third.GetRetInfo())
	requireOK(t, complete(1200, 11))
	if task, _ := h.store.GetTask(ctx, "crypto", "ds_1m"); task.BlockedReason != "" {
		t.Fatalf("被接受的上报应清掉被拒原因: %q", task.BlockedReason)
	}
}

func TestSuccessfulRunKeepsBackfillAndAssignmentCarriesRegistry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	backfill := h.now.Add(-48 * time.Hour).Format(time.RFC3339)
	requireOK(t, h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = backfill }).GetRetInfo())
	h.heartbeat("w1")
	run := h.pull(1)[0]
	if run.GetBackfillFrom() != backfill || run.GetLastSuccessAt() != 0 || run.GetTaskId() == "" {
		t.Fatalf("首次运行应带回补起点、没有最近成功时间: %v", run)
	}
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{{PartitionKey: "2026-10-16", Status: "final", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z",
			FilePath: "f", FileSha256: "abc", RowCount: 10, SubjectCount: 1, FinalizedAt: rfcMillis("2026-10-20T00:00:00Z")}},
		Unrecoverable: []*archivepb.UnrecoverableRange{{StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}}})
	requireOK(t, r.GetRetInfo())
	task, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if fmtRFC(task.BackfillFrom) != backfill || task.LastSuccessAt.IsZero() || len(task.Unrecoverable) != 1 {
		t.Fatalf("成功后保留回补起点、记下最近成功并累积无法补齐区间: %+v", task)
	}
	now, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, now.GetRetInfo())
	next := h.pull(1)[0]
	if next.GetBackfillFrom() != backfill || next.GetLastSuccessAt() == 0 || len(next.GetRegisteredPartitions()) != 1 || next.GetRegisteredPartitions()[0].GetStatus() != "final" {
		t.Fatalf("下发应带回补起点、最近成功时间与分区登记: %v", next)
	}
	list, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{SpaceId: "crypto"})
	if got := list.GetTasks()[0]; len(got.GetUnrecoverable()) != 1 || got.GetHealth() != HealthError {
		t.Fatalf("累积的无法补齐区间应让任务一直显示异常: %v", got)
	}
	cleared, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", ClearUnrecoverable: true})
	requireOK(t, cleared.GetRetInfo())
	if len(cleared.GetTask().GetUnrecoverable()) != 0 {
		t.Fatalf("clear_unrecoverable 应清空累积记录: %v", cleared.GetTask())
	}
}

func TestTaskCarriesDatasetMetadataToTheWorkerAndRefreshesIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.datasets.datasets["crypto/ds_1m"] = Dataset{SpaceID: "crypto", DatasetID: "ds_1m", Name: "现货分钟K线", Description: "币安现货 1 分钟 K 线", DataSourceID: "binance_spot",
		DataKind: "time_series", Freq: "1m", Status: "active", Retention: "168h", SubjectTags: []string{"crypto_spot"}, Revision: 3}
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	task, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	for _, want := range []string{`"name":"现货分钟K线"`, `"description":"币安现货 1 分钟 K 线"`, `"data_source_id":"binance_spot"`, `"subject_tags":["crypto_spot"]`, `"online_retention":"168h"`} {
		if !strings.Contains(task.MetaJSON, want) {
			t.Fatalf("meta_json 缺少 %s: %s", want, task.MetaJSON)
		}
	}
	if got := h.svc.assignment(store.Claimed{Task: task}, nil, "UTC").GetDatasetMetaJson(); got != task.MetaJSON {
		t.Fatalf("assignment meta = %q", got)
	}
	// 数据集简介改了：每小时维护刷新任务里的元数据。
	changed := h.datasets.datasets["crypto/ds_1m"]
	changed.Description = "新的简介"
	h.datasets.datasets["crypto/ds_1m"] = changed
	if err := h.svc.refreshDatasets(ctx); err != nil {
		t.Fatal(err)
	}
	task, _ = h.store.GetTask(ctx, "crypto", "ds_1m")
	if !strings.Contains(task.MetaJSON, "新的简介") {
		t.Fatalf("meta_json 应被刷新: %s", task.MetaJSON)
	}
}

func TestStartArchiveImportNeedsATaskAnOnlineWorkerAndNoBlockingRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	req := &archivepb.StartArchiveImportReq{SpaceId: "crypto", DatasetId: "ds_1m"}
	noTask, _ := h.mgr.StartArchiveImport(ctx, req)
	requireCode(t, noTask.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	offline, _ := h.mgr.StartArchiveImport(ctx, req)
	requireCode(t, offline.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "不在线")
	h.heartbeat("archive-worker@host-a")
	// 排队中的 follow 让位给导入。
	h.trigger(h.now)
	ok, _ := h.mgr.StartArchiveImport(ctx, req)
	requireOK(t, ok.GetRetInfo())
	if ok.GetWorkerId() != "archive-worker@host-a" || ok.GetRun().GetKind() != store.KindImport || ok.GetRun().GetStatus() != store.RunQueued {
		t.Fatalf("start = %v", ok)
	}
	queued, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued", IncludeFollow: true})
	if len(queued.GetRuns()) != 1 || queued.GetRuns()[0].GetKind() != store.KindImport {
		t.Fatalf("排队中的 follow 应被取消: %v", queued.GetRuns())
	}
	// 已有排队中的导入：冲突并写明挡住它的运行。
	again, _ := h.mgr.StartArchiveImport(ctx, req)
	requireCode(t, again.GetRetInfo(), commonpb.ErrorCode_CONFLICT, ok.GetRun().GetRunId())
	// 导入占住任务：期间不生成 follow。
	h.trigger(h.now.Add(time.Minute))
	if all := h.allRuns(); len(all) != 2 {
		t.Fatalf("导入排队期间不应生成 follow: %v", all)
	}
}

func TestImportRunCarriesAllPartitionsAndDoesNotAdvanceLastSuccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo()) // 保留期 168h
	h.heartbeat("w1")
	first := h.pull(1)[0]
	old := &archivepb.PartitionReport{PartitionKey: "2026-01-05", Status: "final", RangeStart: "2026-01-05T00:00:00Z", RangeEnd: "2026-01-06T00:00:00Z",
		FilePath: "crypto/collect/x/2026/20260105.csv.gz", FileSha256: "old", RowCount: 5, SubjectCount: 1, FinalizedAt: rfcMillis("2026-02-01T00:00:00Z")}
	recent := &archivepb.PartitionReport{PartitionKey: "2026-10-16", Status: "final", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z",
		FilePath: "crypto/collect/x/2026/20261016.csv.gz", FileSha256: "new", RowCount: 5, SubjectCount: 1, FinalizedAt: rfcMillis("2026-10-17T00:30:00Z")}
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: first.GetRunId(), LeaseToken: first.GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{old, recent}})
	requireOK(t, r.GetRetInfo())
	before, _ := h.store.GetTask(ctx, "crypto", "ds_1m")

	// 未同步（如暂存库新建/丢失）时 follow 一次性下发全部已登记分区（含超保留期的历史分区），避免重建后旧分区下载 404。
	h.trigger(h.now.Add(time.Minute))
	follow := h.pull(1)[0]
	if follow.GetKind() != store.KindFollow || len(follow.GetRegisteredPartitions()) != 2 || follow.GetRegisteredPartitions()[0].GetPartitionKey() != "2026-01-05" {
		t.Fatalf("未同步 follow 应下发全部历史登记: %v", follow.GetRegisteredPartitions())
	}
	r, _ = h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: follow.GetRunId(), LeaseToken: follow.GetLeaseToken(), Status: "canceled"})
	requireOK(t, r.GetRetInfo())

	h.now = h.now.Add(time.Hour)
	h.heartbeat("w1")
	imp, _ := h.mgr.StartArchiveImport(ctx, &archivepb.StartArchiveImportReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, imp.GetRetInfo())
	// 导入即使在 synced_tasks 匹配时也始终下发全部已登记分区。
	impRsp, err := h.worker.PullArchiveRun(ctx, &archivepb.PullArchiveRunReq{
		FreeSlots: 1, MaxStorageRuns: 2,
		SyncedTasks: []*archivepb.TaskRef{{SpaceId: "crypto", DatasetId: "ds_1m", TaskId: first.GetTaskId()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, impRsp.GetRetInfo())
	assigned := impRsp.GetRuns()
	if len(assigned) != 1 || assigned[0].GetKind() != store.KindImport {
		t.Fatalf("assignment = %v", assigned)
	}
	if regs := assigned[0].GetRegisteredPartitions(); len(regs) != 2 || regs[0].GetPartitionKey() != "2026-01-05" {
		t.Fatalf("导入应下发全部登记: %v", regs)
	}
	r, _ = h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: assigned[0].GetRunId(), LeaseToken: assigned[0].GetLeaseToken(),
		Status: "succeeded", Message: "导入 3 行", RowsAdded: 3})
	requireOK(t, r.GetRetInfo())
	after, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if !after.LastSuccessAt.Equal(before.LastSuccessAt) {
		t.Fatalf("导入不应更新最近成功时间: %s → %s", before.LastSuccessAt, after.LastSuccessAt)
	}
	run, _ := h.mgr.GetArchiveRun(ctx, &archivepb.GetArchiveRunReq{RunId: assigned[0].GetRunId()})
	if run.GetRun().GetStatus() != store.RunSucceeded || run.GetRun().GetRowsAdded() != 3 {
		t.Fatalf("run = %v", run.GetRun())
	}
}

func TestClearArchiveUnrecoverableRemovesOnlyWholeRangesInside(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1")
	run := h.pull(1)[0]
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded",
		Unrecoverable: []*archivepb.UnrecoverableRange{
			{StartTime: "2026-10-02T00:00:00Z", EndTime: "2026-10-02T12:00:00Z"},
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

// rfcMillis 把测试里写的 RFC3339 时刻换成 Unix 毫秒。
func rfcMillis(text string) int64 {
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		panic(err)
	}
	return at.UnixMilli()
}

func TestPullArchiveRunSendsRegisteredPartitionsOnlyOnDemand(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1")
	first := h.pull(1)[0]
	taskID := first.GetTaskId()
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{
		RunId: first.GetRunId(), LeaseToken: first.GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{{
			PartitionKey: "2026-10-16", Status: "final", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z",
			FilePath: "f", FileSha256: "abc", RowCount: 10, SubjectCount: 1, FinalizedAt: rfcMillis("2026-10-17T00:30:00Z"),
		}},
	})
	requireOK(t, r.GetRetInfo())

	// 1. 暂存库已同步且 task_id 匹配：follow 不再重复下发 registered_partitions。
	h.trigger(h.now.Add(time.Minute))
	syncedPull, err := h.worker.PullArchiveRun(ctx, &archivepb.PullArchiveRunReq{
		FreeSlots: 1, MaxStorageRuns: 2,
		SyncedTasks: []*archivepb.TaskRef{{SpaceId: "crypto", DatasetId: "ds_1m", TaskId: taskID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, syncedPull.GetRetInfo())
	if len(syncedPull.GetRuns()) != 1 || len(syncedPull.GetRuns()[0].GetRegisteredPartitions()) != 0 {
		t.Fatalf("已同步任务不应下发 registered_partitions: %v", syncedPull.GetRuns())
	}
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{
		RunId: syncedPull.GetRuns()[0].GetRunId(), LeaseToken: syncedPull.GetRuns()[0].GetLeaseToken(), Status: "succeeded",
	})
	requireOK(t, done.GetRetInfo())

	// 2. 暂存库丢失（未报 synced_tasks）或 task_id 不匹配：按需补发 registered_partitions。
	h.trigger(h.now.Add(time.Minute))
	stalePull, err := h.worker.PullArchiveRun(ctx, &archivepb.PullArchiveRunReq{
		FreeSlots: 1, MaxStorageRuns: 2,
		SyncedTasks: []*archivepb.TaskRef{{SpaceId: "crypto", DatasetId: "ds_1m", TaskId: "old-task-id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, stalePull.GetRetInfo())
	if regs := stalePull.GetRuns()[0].GetRegisteredPartitions(); len(regs) != 1 || regs[0].GetPartitionKey() != "2026-10-16" {
		t.Fatalf("task_id 不匹配应补发 registered_partitions: %v", regs)
	}
	done, _ = h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{
		RunId: stalePull.GetRuns()[0].GetRunId(), LeaseToken: stalePull.GetRuns()[0].GetLeaseToken(), Status: "succeeded",
	})
	requireOK(t, done.GetRetInfo())

	// 3. 导入运行始终下发全部已登记分区，即使 synced_tasks 匹配。
	h.heartbeat("w1")
	imp, _ := h.mgr.StartArchiveImport(ctx, &archivepb.StartArchiveImportReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, imp.GetRetInfo())
	impPull, err := h.worker.PullArchiveRun(ctx, &archivepb.PullArchiveRunReq{
		FreeSlots: 1, MaxStorageRuns: 2,
		SyncedTasks: []*archivepb.TaskRef{{SpaceId: "crypto", DatasetId: "ds_1m", TaskId: taskID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, impPull.GetRetInfo())
	if regs := impPull.GetRuns()[0].GetRegisteredPartitions(); len(regs) != 1 {
		t.Fatalf("导入运行应始终下发全部登记: %v", regs)
	}
}

func TestSpaceTimezoneReadsFromStorageAndCaches(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.datasets.datasets["stockjp/jp_1m"] = Dataset{SpaceID: "stockjp", DatasetID: "jp_1m", DataKind: "time_series", Freq: "1m", Status: "active", Retention: "168h"}
	h.datasets.spaces = map[string]string{"stockjp": "Asia/Tokyo"}
	requireOK(t, h.create("jp_1m", func(r *archivepb.CreateArchiveTaskReq) { r.SpaceId = "stockjp" }).GetRetInfo())
	h.heartbeat("w1")

	runs := h.pull(1)
	if len(runs) != 1 || runs[0].GetSpaceTz() != "Asia/Tokyo" {
		t.Fatalf("assignment space_tz = %q, want Asia/Tokyo", runs[0].GetSpaceTz())
	}
	if h.datasets.spaceCalls != 1 {
		t.Fatalf("spaceCalls = %d, want 1", h.datasets.spaceCalls)
	}

	// 5 分钟内 ListArchiveTasks 命中缓存，不重复查 Storage。
	list, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{SpaceId: "stockjp"})
	requireOK(t, list.GetRetInfo())
	if h.datasets.spaceCalls != 1 {
		t.Fatalf("缓存有效期内不应重复查 Storage: spaceCalls=%d", h.datasets.spaceCalls)
	}

	// 超过 5 分钟且 Storage 临时报错：沿用旧缓存，不中断下发。
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{
		RunId: runs[0].GetRunId(), LeaseToken: runs[0].GetLeaseToken(), Status: "succeeded",
	})
	requireOK(t, done.GetRetInfo())
	h.now = h.now.Add(6 * time.Minute)
	h.datasets.spaceErr = fmt.Errorf("storage temporarily unavailable")
	h.trigger(h.now)
	runs = h.pull(1)
	if len(runs) != 1 || runs[0].GetSpaceTz() != "Asia/Tokyo" {
		t.Fatalf("Storage 抖动时应沿用缓存时区: %v", runs)
	}
	if h.datasets.spaceCalls != 2 {
		t.Fatalf("缓存过期后应尝试刷新一次: spaceCalls=%d", h.datasets.spaceCalls)
	}
}

func TestPullArchiveRunSyncsAllHistoricalPartitionsAfterStagingLoss(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo()) // 保留期 168h
	h.heartbeat("w1")                                 // 纯本地模式：未配置对象存储
	first := h.pull(1)[0]
	old := &archivepb.PartitionReport{
		PartitionKey: "2026-01-05", Status: "final", RangeStart: "2026-01-05T00:00:00Z", RangeEnd: "2026-01-06T00:00:00Z",
		FilePath: "crypto/collect/x/2026/20260105.csv.gz", FileSha256: "oldsha", RowCount: 5, SubjectCount: 1,
		ExportedAt: rfcMillis("2026-01-06T00:30:00Z"), FinalizedAt: rfcMillis("2026-01-09T00:30:00Z"),
	}
	recent := &archivepb.PartitionReport{
		PartitionKey: "2026-10-16", Status: "final", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z",
		FilePath: "crypto/collect/x/2026/20261016.csv.gz", FileSha256: "newsha", RowCount: 8, SubjectCount: 2,
		ExportedAt: rfcMillis("2026-10-17T00:30:00Z"), FinalizedAt: rfcMillis("2026-10-17T00:45:00Z"),
	}
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{
		RunId: first.GetRunId(), LeaseToken: first.GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{old, recent},
	})
	requireOK(t, done.GetRetInfo())

	// 1. 纯本地模式下暂存库重建（SyncedTasks 为空）：超出 168h 保留期的 2026-01-05 与近期分区都完整下发。
	h.trigger(h.now.Add(time.Minute))
	rebuiltLocal := h.pull(1)[0]
	if regs := rebuiltLocal.GetRegisteredPartitions(); len(regs) != 2 || regs[0].GetPartitionKey() != "2026-01-05" || regs[1].GetPartitionKey() != "2026-10-16" {
		t.Fatalf("纯本地重建暂存库应完整同步超保留期历史分区: %v", regs)
	}
	done, _ = h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{
		RunId: rebuiltLocal.GetRunId(), LeaseToken: rebuiltLocal.GetLeaseToken(), Status: "succeeded",
	})
	requireOK(t, done.GetRetInfo())

	// 2. 日后启用外部 S3 且再次重建暂存库：历史未上传分区 2026-01-05 同样完整同步，不会漏传。
	hbRsp, err := h.worker.ArchiveWorkerHeartbeat(ctx, &archivepb.ArchiveWorkerHeartbeatReq{
		WorkerId: "w1", RootDir: "/data/archive",
		ObjectStore: &archivepb.ObjectStoreInfo{Endpoint: "cos.ap-nanjing.myqcloud.com", Bucket: "b-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, hbRsp.GetRetInfo())
	h.trigger(h.now.Add(2 * time.Minute))
	rebuiltRemote := h.pull(1)[0]
	if regs := rebuiltRemote.GetRegisteredPartitions(); len(regs) != 2 || regs[0].GetPartitionKey() != "2026-01-05" {
		t.Fatalf("启用外部 S3 后重建暂存库应同步全部历史未上传分区: %v", regs)
	}
}

func TestPullArchiveRunDoesNotLockLeaseWhenSpaceTimezoneFails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.datasets.datasets["stockjp/jp_1m"] = Dataset{SpaceID: "stockjp", DatasetID: "jp_1m", DataKind: "time_series", Freq: "1m", Status: "active", Retention: "168h"}
	h.datasets.spaces = map[string]string{"stockjp": "Asia/Tokyo"}
	requireOK(t, h.create("jp_1m", func(r *archivepb.CreateArchiveTaskReq) { r.SpaceId = "stockjp" }).GetRetInfo())
	h.heartbeat("w1")

	// 1. 冷缓存下 Storage 不可用：PullArchiveRun 返回错误，且待领运行保持 queued，不会锁成 running。
	h.datasets.spaceErr = fmt.Errorf("storage down")
	rsp, err := h.worker.PullArchiveRun(ctx, &archivepb.PullArchiveRunReq{FreeSlots: 2, MaxStorageRuns: 2})
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_INNER_ERR, "storage down")
	active, err := h.store.ListActiveRuns(ctx, "stockjp")
	if err != nil || len(active) != 1 || active[0].Status != store.RunQueued || active[0].LeaseToken != "" {
		t.Fatalf("时区解析失败不能锁定租约，实际 active = %+v err=%v", active, err)
	}

	// Storage 恢复后可立即领取，无需等待 10 分钟租约过期。
	h.datasets.spaceErr = nil
	runs := h.pull(1)
	if len(runs) != 1 || runs[0].GetSpaceTz() != "Asia/Tokyo" {
		t.Fatalf("Storage 恢复后应立即领到运行: %v", runs)
	}
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{
		RunId: runs[0].GetRunId(), LeaseToken: runs[0].GetLeaseToken(), Status: "succeeded",
	})
	requireOK(t, done.GetRetInfo())

	// 2. 缓存过期后仅 stockjp 被删（ErrSpaceNotFound）：清除 stockjp 失效缓存项、不锁 stockjp 租约，
	// 同时健康空间 crypto 仍能正常被领取，不被失效空间阻塞。
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.now = h.now.Add(6 * time.Minute)
	h.trigger(h.now)
	h.datasets.spaceErrs = map[string]error{"stockjp": fmt.Errorf("%w: stockjp", ErrSpaceNotFound)}

	mixed := h.pull(2)
	if len(mixed) != 1 || mixed[0].GetSpaceId() != "crypto" {
		t.Fatalf("健康空间 crypto 应被领取且跳过已删空间 stockjp: %v", mixed)
	}
	h.svc.mu.Lock()
	_, stillCached := h.svc.spaceTZ["stockjp"]
	h.svc.mu.Unlock()
	if stillCached {
		t.Fatal("ErrSpaceNotFound 时必须从缓存中删除失效空间")
	}
	active, _ = h.store.ListActiveRuns(ctx, "stockjp")
	if len(active) != 1 || active[0].Status != store.RunQueued || active[0].LeaseToken != "" {
		t.Fatalf("已删空间 stockjp 不能锁定租约: %+v", active)
	}

	// 3. 仅剩已删空间 stockjp 排队时：PullArchiveRun 返回错误且仍保持 queued。
	rsp, _ = h.worker.PullArchiveRun(ctx, &archivepb.PullArchiveRunReq{FreeSlots: 2, MaxStorageRuns: 2})
	requireCode(t, rsp.GetRetInfo(), commonpb.ErrorCode_INNER_ERR, "space not found")
	active, _ = h.store.ListActiveRuns(ctx, "stockjp")
	if len(active) != 1 || active[0].Status != store.RunQueued {
		t.Fatalf("仅剩已删空间时不能锁定租约: %+v", active)
	}
}

// 下发的本地日终时刻取自 packages/barclock：港股 16:10（等收市竞价）、A 股 15:00、美股 16:00，加密货币与未登记空间为空。
func TestAssignmentLocalCloseComesFromBarclock(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		space string
		want  string
	}{
		{"crypto", ""},
		{"stockcn", "15:00"},
		{"stockhk", "16:10"},
		{"stockus", "16:00"},
		{"stockjp", ""},
	}
	for _, tc := range cases {
		got := h.svc.assignment(store.Claimed{Task: store.Task{SpaceID: tc.space, DatasetID: "ds_1m"}}, nil, "UTC").GetLocalClose()
		if got != tc.want {
			t.Errorf("space %s local_close = %q，期望 %q", tc.space, got, tc.want)
		}
	}
}
