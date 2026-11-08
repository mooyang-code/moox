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

// createOnly 只创建任务（不生成运行；启用的任务在下一次检查时复核）。
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
		{"inactive dataset", "off", nil, commonpb.ErrorCode_INVALID_PARAM, "尚未激活"},
		{"pull every too many bars", "ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.PullEveryBars = 1441 }, commonpb.ErrorCode_INVALID_PARAM, "pull_every_bars"},
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

func TestCreateArchiveTaskFillsTheTimingAndWaitsForTheNextCheck(t *testing.T) {
	h := newHarness(t)
	rsp := h.createOnly("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-15T00:00:00Z" })
	requireOK(t, rsp.GetRetInfo())
	task := rsp.GetTask()
	if task.GetFreq() != "1m" || task.GetPartitionUnit() != "day" || task.GetIntervalSeconds() != 3600 || task.GetPullEveryBars() != 1 || task.GetStatus() != "enabled" ||
		task.GetDataNodeId() != "node-0" || task.GetRetention() != "168h" || task.GetBackfillFrom() != "2026-10-15T00:00:00Z" ||
		task.GetLastBoundary() != "" || task.GetLastReview() != "" {
		t.Fatalf("task = %v", task)
	}
	// 01:00:00 在整点避让区间里，01:00 的边界在 01:04:25 拉取。
	if task.GetNextPullAt() != "2026-10-17T01:04:25Z" {
		t.Fatalf("next_pull_at = %s", task.GetNextPullAt())
	}
	if runs, _ := h.mgr.ListArchiveRuns(context.Background(), &archivepb.ListArchiveRunsReq{IncludeTicks: true}); len(runs.GetRuns()) != 0 {
		t.Fatalf("创建任务不立即生成运行: %v", runs.GetRuns())
	}
	dup := h.createOnly("ds_1m", nil)
	requireCode(t, dup.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "已经有归档任务")

	n4 := h.createOnly("ds_1h", func(r *archivepb.CreateArchiveTaskReq) { r.PullEveryBars, r.Paused = 4, true })
	requireOK(t, n4.GetRetInfo())
	if n4.GetTask().GetStatus() != "paused" || n4.GetTask().GetNextPullAt() != "" || n4.GetTask().GetPullEveryBars() != 4 || n4.GetTask().GetRetention() != "forever" {
		t.Fatalf("paused task = %v", n4.GetTask())
	}
	h.datasets.datasets["crypto/ds_1d"] = Dataset{SpaceID: "crypto", DatasetID: "ds_1d", DataKind: "time_series", Freq: "1d", Status: "active", DataNodeID: "node-0", Retention: "forever"}
	daily := h.createOnly("ds_1d", func(r *archivepb.CreateArchiveTaskReq) { r.PullEveryBars = 7 })
	requireOK(t, daily.GetRetInfo())
	if daily.GetTask().GetIntervalSeconds() != 86400 || daily.GetTask().GetPullEveryBars() != 1 || daily.GetTask().GetPartitionUnit() != "month" ||
		daily.GetTask().GetNextPullAt() != "2026-10-18T00:04:25Z" {
		t.Fatalf("1d 任务每天一次、N 固定为 1: %v", daily.GetTask())
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

func TestUpdateArchiveTaskChangesPullEveryBarsAndResumeClearsBoundaries(t *testing.T) {
	h := newHarness(t)
	requireOK(t, h.createOnly("ds_1m", nil).GetRetInfo())
	ctx := context.Background()
	updated, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", PullEveryBars: 5})
	requireOK(t, updated.GetRetInfo())
	if updated.GetTask().GetPullEveryBars() != 5 || updated.GetTask().GetIntervalSeconds() != 3600 || updated.GetTask().GetStatus() != "enabled" ||
		updated.GetTask().GetNextPullAt() != "2026-10-17T01:04:25Z" {
		t.Fatalf("updated = %v", updated.GetTask())
	}
	// 0 表示不修改。
	kept, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	if kept.GetTask().GetPullEveryBars() != 5 {
		t.Fatalf("kept = %v", kept.GetTask())
	}
	bad, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", PullEveryBars: 2000})
	requireCode(t, bad.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "pull_every_bars")
	badStatus, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "weird"})
	requireCode(t, badStatus.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "status")

	h.now = time.Date(2026, 10, 17, 1, 10, 25, 0, time.UTC)
	if err := h.svc.Trigger(ctx); err != nil {
		t.Fatal(err)
	}
	paused, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "paused"})
	requireOK(t, paused.GetRetInfo())
	if paused.GetTask().GetStatus() != "paused" || paused.GetTask().GetNextPullAt() != "" || paused.GetTask().GetLastBoundary() != "2026-10-17T01:10:00Z" {
		t.Fatalf("paused = %v", paused.GetTask())
	}
	if queued, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued"}); len(queued.GetRuns()) != 0 {
		t.Fatalf("停用取消排队中的复核: %v", queued.GetRuns())
	}
	h.now = h.now.Add(2 * time.Hour)
	resumed, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m", Status: "enabled"})
	requireOK(t, resumed.GetRetInfo())
	if resumed.GetTask().GetLastBoundary() != "" || resumed.GetTask().GetLastReview() != "" {
		t.Fatalf("重新启用清空两个边界: %v", resumed.GetTask())
	}
	// 下一次检查立即复核。
	if err := h.svc.Trigger(ctx); err != nil {
		t.Fatal(err)
	}
	queued, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{Status: "queued"})
	if len(queued.GetRuns()) != 1 || queued.GetRuns()[0].GetKind() != "scheduled" {
		t.Fatalf("重新启用后下一次检查复核: %v", queued.GetRuns())
	}
	missing, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "nope"})
	requireCode(t, missing.GetRetInfo(), commonpb.ErrorCode_NOT_FOUND, "")
}

func TestPrepareAndPreviewReportPullTimesAndReadSpans(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	prep, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, prep.GetRetInfo())
	if prep.GetFreq() != "1m" || prep.GetPartitionUnit() != "day" || prep.GetRecommendedPullEveryBars() != 1 ||
		prep.GetEarliestBackfillFrom() != "2026-10-11T01:00:00Z" || prep.GetExists() || prep.GetDataNodeId() != "node-0" {
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

	// crypto 1m、N=5，现在 01:00：01:00 的边界顺延到 01:04:25，之后每 5 分钟；复核读 1 小时 + 15 分钟 + 1 根。
	preview, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "crypto", DatasetId: "ds_1m", PullEveryBars: 5})
	requireOK(t, preview.GetRetInfo())
	want := []string{"2026-10-17T01:04:25Z", "2026-10-17T01:05:25Z", "2026-10-17T01:10:25Z", "2026-10-17T01:15:25Z", "2026-10-17T01:20:25Z"}
	if preview.GetIntervalSeconds() != 3600 || preview.GetReadSpanSeconds() != 76*60 || strings.Join(preview.GetNextRuns(), ",") != strings.Join(want, ",") {
		t.Fatalf("preview = %v", preview)
	}
	if strings.Join(preview.GetNextReviews(), ",") != "2026-10-17T01:34:25Z,2026-10-17T02:34:25Z,2026-10-17T03:34:25Z" {
		t.Fatalf("reviews = %v", preview.GetNextReviews())
	}
	// 今天的分区明天 00:00 收盘 + 15 分钟，之后第一次是 00:15:25 的到点拉取。
	estimates := strings.Join(preview.GetPartitionExportEstimates(), "|")
	if len(preview.GetPartitionExportEstimates()) != 2 || !strings.Contains(estimates, "分区 2026-10-17：预计 2026-10-18T00:15:25Z") {
		t.Fatalf("estimates = %v", preview.GetPartitionExportEstimates())
	}
	bad, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "crypto", DatasetId: "ds_1m", PullEveryBars: 1441})
	requireCode(t, bad.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "pull_every_bars")
	// stockcn 1m：当日分区 15:00 收盘 + 15 分钟 = 07:15 UTC，07:15:25 的到点拉取导出。
	cnPreview, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "stockcn", DatasetId: "cn_1m"})
	requireOK(t, cnPreview.GetRetInfo())
	if joined := strings.Join(cnPreview.GetPartitionExportEstimates(), "|"); !strings.Contains(joined, "2026-10-17T07:15:25Z") {
		t.Fatalf("stockcn estimates = %v", cnPreview.GetPartitionExportEstimates())
	}
	if cnPreview.GetReadSpanSeconds() != uint32(3600+15*60+60) {
		t.Fatalf("stockcn read span = %d", cnPreview.GetReadSpanSeconds())
	}
	// stockcn 1d：15:00 收盘落在整点避让区间，15:04:25（07:04:25 UTC）拉取，拉取即复核。
	h.datasets.datasets["stockcn/cn_1d"] = Dataset{SpaceID: "stockcn", DatasetID: "cn_1d", DataKind: "time_series", Freq: "1d", Status: "active", DataNodeID: "node-0", Retention: "forever"}
	daily, _ := h.mgr.PreviewArchiveSchedule(ctx, &archivepb.PreviewArchiveScheduleReq{SpaceId: "stockcn", DatasetId: "cn_1d"})
	requireOK(t, daily.GetRetInfo())
	if daily.GetIntervalSeconds() != 86400 || len(daily.GetNextRuns()) != 5 || daily.GetNextRuns()[0] != "2026-10-17T07:04:25Z" || daily.GetNextRuns()[1] != "2026-10-18T07:04:25Z" ||
		strings.Join(daily.GetNextReviews(), ",") != strings.Join(daily.GetNextRuns()[:3], ",") {
		t.Fatalf("stockcn 1d preview = %v", daily)
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
		run.GetBackfillFrom() != "2026-10-15T00:00:00Z" || run.GetStartedAt() != "2026-10-17T01:00:00Z" || run.GetIntervalSeconds() != 3600 || run.GetLeaseTtlSeconds() != 600 {
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
	// M3：分区登记在 w1，w1 的租约过期、w2 接管后，任务照样派给 w2，上报被接受，登记的 worker 变成 w2。
	h.now = h.now.Add(2 * time.Minute)
	if hb := h.heartbeat("w2", "other"); !hb.GetActive() {
		t.Fatal("w1 租约过期后 w2 应接管")
	}
	runs := h.pull("w2", "other", 1)
	if len(runs) != 1 {
		t.Fatalf("分区登记在 w1 的任务也应派给 w2: %v", runs)
	}
	if regs := runs[0].GetRegisteredPartitions(); len(regs) != 1 || regs[0].GetFileSha256() != "abc" {
		t.Fatalf("下发的登记 = %v", regs)
	}
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w2", RunId: runs[0].GetRunId(), LeaseToken: runs[0].GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{{PartitionKey: "2026-10-16", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z", Status: "exported", RowCount: 100, SubjectCount: 2,
			FilePath: "crypto/ds_1m/2026-10-16.csv.gz", FileSha256: "abc", ExportedAt: "2026-10-17T01:05:00Z"}}})
	requireOK(t, done.GetRetInfo())
	part, _ := h.mgr.GetArchivePartition(context.Background(), &archivepb.GetArchivePartitionReq{SpaceId: "crypto", DatasetId: "ds_1m", PartitionKey: "2026-10-16"})
	if part.GetPartition().GetWorkerId() != "w2" || part.GetPartition().GetFileBytes() != 10 {
		t.Fatalf("登记的 worker 应变成 w2、大小不被冲成 0: %v", part.GetPartition())
	}
	overview, _ := h.mgr.GetArchiveOverview(context.Background(), &archivepb.GetArchiveOverviewReq{})
	for _, alert := range overview.GetAlerts() {
		if strings.Contains(alert, "分区登记在 worker") {
			t.Fatalf("总览不应再有分区归属告警: %v", overview.GetAlerts())
		}
	}
}

func TestDeleteArchiveTaskNotifiesTheWorkerAndKeepsPartitionedTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	requireOK(t, h.create("ds_1h", nil).GetRetInfo())
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
	// 心跳带回当前全部任务：被删的任务不在列表里，worker 维护时据此清理它的暂存状态。
	hb := h.heartbeat("w1", "i1")
	if !hb.GetActive() || len(hb.GetTasks()) != 1 || hb.GetTasks()[0].GetDatasetId() != partitioned || hb.GetTasks()[0].GetTaskId() == "" {
		t.Fatalf("heartbeat = %v", hb)
	}
}

func TestMaintainRecoversLostLeases(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
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
	list, _ := h.mgr.ListArchiveTasks(ctx, &archivepb.ListArchiveTasksReq{})
	if list.GetTasks()[0].GetHealth() != HealthWarning || !strings.Contains(list.GetTasks()[0].GetHealthReason(), "最近一次运行失败") {
		t.Fatalf("task health = %s %s", list.GetTasks()[0].GetHealth(), list.GetTasks()[0].GetHealthReason())
	}
}

// at 返回 2026-10-17 的 UTC 时刻。
func at(h, m, sec int) time.Time { return time.Date(2026, 10, 17, h, m, sec, 0, time.UTC) }

// finishQueued 让 worker 领取并成功完成全部排队中的运行，返回下发的参数。
func (h *harness) finishQueued() []*archivepb.RunAssignment {
	h.t.Helper()
	h.heartbeat("w1", "i1")
	runs := h.pull("w1", "i1", 10)
	for _, run := range runs {
		rsp, _ := h.worker.CompleteArchiveRun(context.Background(), &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded"})
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

// allRuns 按计划时刻倒序列出全部运行（含成功的 tick）。
func (h *harness) allRuns() []*archivepb.ArchiveRun {
	h.t.Helper()
	rsp, _ := h.mgr.ListArchiveRuns(context.Background(), &archivepb.ListArchiveRunsReq{IncludeTicks: true, Page: &commonpb.Page{Size: 200}})
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

func TestTriggerGeneratesReviewsAndTicksByBoundary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.createOnly("ds_1m", func(r *archivepb.CreateArchiveTaskReq) { r.BackfillFrom = "2026-10-15T00:00:00Z" }).GetRetInfo())

	// 新任务首次检查即复核（拉取边界一并记下）。
	h.trigger(at(1, 10, 25))
	runs := h.allRuns()
	if len(runs) != 1 || runs[0].GetKind() != "scheduled" || runs[0].GetRangeStart() != "" || runs[0].GetScheduledAt() != "2026-10-17T01:10:25Z" {
		t.Fatalf("首次检查 = %v", runs)
	}
	if task := h.task("crypto", "ds_1m"); !task.LastBoundary.Equal(at(1, 10, 0)) || !task.LastReview.Equal(at(0, 34, 0)) {
		t.Fatalf("边界 = %s %s", task.LastBoundary, task.LastReview)
	}
	// 同一分钟再检查一次：不到点，不写记录。
	h.trigger(at(1, 10, 40))
	if len(h.allRuns()) != 1 {
		t.Fatal("不到点不应写运行记录")
	}
	// 复核还在运行：到点也跳过，累加跳过次数，边界不前进。
	h.heartbeat("w1", "i1")
	review := h.pull("w1", "i1", 1)[0]
	if review.GetKind() != "scheduled" || review.GetBackfillFrom() != "2026-10-15T00:00:00Z" || review.GetIntervalSeconds() != 3600 {
		t.Fatalf("复核下发 = %v", review)
	}
	h.trigger(at(1, 11, 25))
	h.trigger(at(1, 12, 25))
	if task := h.task("crypto", "ds_1m"); task.ConsecutiveSkips != 2 || !task.LastBoundary.Equal(at(1, 10, 0)) || len(h.allRuns()) != 1 {
		t.Fatalf("跳过后 = %+v", task)
	}
	done, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: review.GetRunId(), LeaseToken: review.GetLeaseToken(), Status: "succeeded"})
	requireOK(t, done.GetRetInfo())
	reviewed := h.task("crypto", "ds_1m")
	if !reviewed.LastSuccessAt.Equal(at(1, 10, 40)) || !reviewed.BackfillFrom.IsZero() {
		t.Fatalf("复核成功推进最近成功时间并清回补起点: %+v", reviewed)
	}

	// 运行结束后的到点拉取从上次边界往前 2 根读起，补上跳过的那几分钟。
	h.trigger(at(1, 13, 25))
	tick := h.allRuns()[0]
	if tick.GetKind() != "tick" || tick.GetRangeStart() != "2026-10-17T01:08:00Z" || tick.GetRangeEnd() != "2026-10-17T01:13:25Z" {
		t.Fatalf("tick = %v", tick)
	}
	if task := h.task("crypto", "ds_1m"); task.ConsecutiveSkips != 0 || !task.LastBoundary.Equal(at(1, 13, 0)) || !task.LastReview.Equal(at(0, 34, 0)) {
		t.Fatalf("tick 后 = %+v", task)
	}
	// tick 下发区间；成功后不推进最近成功时间。
	assigned := h.finishQueued()
	if len(assigned) != 1 || assigned[0].GetKind() != "tick" || assigned[0].GetRangeStart() != "2026-10-17T01:08:00Z" || assigned[0].GetRangeEnd() != "2026-10-17T01:13:25Z" {
		t.Fatalf("tick 下发 = %v", assigned)
	}
	if task := h.task("crypto", "ds_1m"); !task.LastSuccessAt.Equal(reviewed.LastSuccessAt) {
		t.Fatalf("tick 推进了最近成功时间: %s → %s", reviewed.LastSuccessAt, task.LastSuccessAt)
	}
	h.trigger(at(1, 14, 25))
	h.finishQueued()

	// 第 34 分：复核与拉取同时到点，只生成复核，两个边界都前进。
	h.trigger(at(1, 34, 25))
	if latest := h.allRuns()[0]; latest.GetKind() != "scheduled" {
		t.Fatalf("复核与拉取同时到点只生成复核: %v", latest)
	}
	if task := h.task("crypto", "ds_1m"); !task.LastBoundary.Equal(at(1, 34, 0)) || !task.LastReview.Equal(at(1, 34, 0)) {
		t.Fatalf("复核后 = %+v", task)
	}
	h.finishQueued()

	// 整点避让区间：:58～:04 整次跳过；出了区间第一次检查补上前进的边界。
	h.trigger(at(1, 57, 25))
	h.finishQueued()
	count := len(h.allRuns())
	for _, now := range []time.Time{at(1, 58, 25), at(1, 59, 25), at(2, 0, 25), at(2, 3, 25)} {
		h.trigger(now)
	}
	if len(h.allRuns()) != count {
		t.Fatal("避让区间里不应生成运行")
	}
	h.trigger(at(2, 4, 25))
	if after := h.allRuns()[0]; after.GetKind() != "tick" || after.GetRangeStart() != "2026-10-17T01:55:00Z" || after.GetRangeEnd() != "2026-10-17T02:04:25Z" {
		t.Fatalf("出避让区间的到点拉取 = %v", after)
	}
	h.finishQueued()

	// 运行列表默认不列成功的 tick。
	listed, _ := h.mgr.ListArchiveRuns(ctx, &archivepb.ListArchiveRunsReq{})
	if len(listed.GetRuns()) != 2 {
		t.Fatalf("默认列表 = %v", listed.GetRuns())
	}
	for _, run := range listed.GetRuns() {
		if run.GetKind() == "tick" {
			t.Fatalf("默认列表不应有成功的 tick: %v", run)
		}
	}

	// 成功的 tick 只保留 24 小时，复核保留 30 天。
	h.now = at(2, 10, 0).Add(24 * time.Hour)
	if err := h.svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	left := h.allRuns()
	if len(left) != 2 {
		t.Fatalf("应只剩两次复核: %v", left)
	}
	for _, run := range left {
		if run.GetKind() != "scheduled" {
			t.Fatalf("超过 24 小时的成功 tick 应被清理: %v", run)
		}
	}
}

func TestTriggerDailyTasksOnlyReviewOncePerDay(t *testing.T) {
	h := newHarness(t)
	h.datasets.datasets["crypto/ds_1d"] = Dataset{SpaceID: "crypto", DatasetID: "ds_1d", DataKind: "time_series", Freq: "1d", Status: "active", DataNodeID: "node-0", Retention: "forever"}
	h.datasets.datasets["stockcn/cn_1d"] = Dataset{SpaceID: "stockcn", DatasetID: "cn_1d", DataKind: "time_series", Freq: "1d", Status: "active", DataNodeID: "node-1", Retention: "forever"}
	requireOK(t, h.createOnly("ds_1d", nil).GetRetInfo())
	requireOK(t, h.createOnly("cn_1d", func(r *archivepb.CreateArchiveTaskReq) { r.SpaceId = "stockcn" }).GetRetInfo())
	h.trigger(at(1, 10, 25))
	if runs := h.allRuns(); len(runs) != 2 || runs[0].GetKind() != "scheduled" || runs[1].GetKind() != "scheduled" {
		t.Fatalf("首次检查都复核: %v", runs)
	}
	h.finishQueued()
	// 当天之后的检查都不到点（包括第 34 分）；A 股 10-17 的收盘在 07:00 UTC，首次复核时已经记下 10-16 的收盘。
	for _, now := range []time.Time{at(1, 34, 25), at(6, 59, 25)} {
		h.trigger(now)
	}
	if runs := h.allRuns(); len(runs) != 2 {
		t.Fatalf("不到点不应再生成运行: %v", runs)
	}
	// A 股 15:00（07:00 UTC）收盘：15:04:25 复核。
	h.trigger(at(7, 4, 25))
	if runs := h.allRuns(); len(runs) != 3 || runs[0].GetSpaceId() != "stockcn" || runs[0].GetKind() != "scheduled" {
		t.Fatalf("stockcn 1d 收盘后复核: %v", runs)
	}
	h.finishQueued()
	h.trigger(at(23, 59, 25))
	// crypto 第二天 00:04:25 复核。
	h.trigger(time.Date(2026, 10, 18, 0, 4, 25, 0, time.UTC))
	if runs := h.allRuns(); len(runs) != 4 || runs[0].GetSpaceId() != "crypto" || runs[0].GetKind() != "scheduled" {
		t.Fatalf("crypto 1d 第二天复核: %v", runs)
	}
	for _, run := range h.allRuns() {
		if run.GetKind() == "tick" {
			t.Fatalf("1d 只生成复核: %v", run)
		}
	}
	if task := h.task("stockcn", "cn_1d"); !task.LastBoundary.Equal(at(7, 0, 0)) || !task.LastReview.Equal(at(7, 0, 0)) {
		t.Fatalf("stockcn 1d 边界 = %+v", task)
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
		IntervalSeconds: 3600, CreatedAt: now.Add(-24 * time.Hour), LastSuccessAt: now.Add(-10 * time.Minute)}
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
	// 复核失败后跟着一次成功的到点拉取：仍要提示复核失败。
	if level, reason := eval(func(in *healthInput) {
		in.LastRun = &store.Run{RunID: "tick", Kind: store.KindTick, Status: store.RunSucceeded}
		in.LastReview = &store.Run{RunID: "review", Kind: store.KindScheduled, Status: store.RunFailed, Message: "boom"}
	}); level != HealthWarning || !strings.Contains(reason, "最近一次复核失败：boom") {
		t.Fatalf("failed review hidden by tick = %s %s", level, reason)
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
	// 无法补齐的区间按任务累积：不论最近一次运行有没有，都一直显示异常。
	if level, _ := eval(func(in *healthInput) { in.Task.Unrecoverable = []store.Unrecoverable{{StartTime: "a", EndTime: "b"}} }); level != HealthError {
		t.Fatalf("unrecoverable = %s", level)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.Retention = "" }); level != HealthError {
		t.Fatalf("读不到保留期时应显示异常，不能当永久: %s", level)
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
	if level, _ := eval(func(in *healthInput) { in.Task.ConsecutiveSkips = 10 }); level != HealthWarning {
		t.Fatalf("skips = %s", level)
	}
	if level, _ := eval(func(in *healthInput) { in.Task.ConsecutiveSkips = 9 }); level != HealthOK {
		t.Fatalf("skips 9 = %s", level)
	}
	// 复核耗时超过复核间隔（1 小时）的一半 → 需关注；只看复核与手动运行。
	slow := &store.Run{Kind: store.KindScheduled, Status: store.RunSucceeded, StartedAt: now.Add(-40 * time.Minute), FinishedAt: now.Add(-5 * time.Minute)}
	if level, reason := eval(func(in *healthInput) { in.LastReview = slow }); level != HealthWarning || !strings.Contains(reason, "一半") {
		t.Fatalf("slow = %s %s", level, reason)
	}
	if level, _ := eval(func(in *healthInput) { in.LastRun = slow }); level != HealthOK {
		t.Fatalf("耗时规则不看最近一次运行（多半是 tick）: %s", level)
	}
	fast := &store.Run{Kind: store.KindScheduled, Status: store.RunSucceeded, StartedAt: now.Add(-20 * time.Minute), FinishedAt: now.Add(-5 * time.Minute)}
	if level, _ := eval(func(in *healthInput) { in.LastReview = fast }); level != HealthOK {
		t.Fatalf("fast = %s", level)
	}
	// 分区到时间（日末 + 回看 + 2 个复核间隔 = 02:15）仍未导出。
	open := store.Partition{PartitionKey: "2026-10-16", RangeStart: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), RangeEnd: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
	if level, reason := eval(func(in *healthInput) { in.OpenParts = []store.Partition{open} }); level != HealthWarning || !strings.Contains(reason, "未导出") {
		t.Fatalf("overdue partition = %s %s", level, reason)
	}
	if level, _ := eval(func(in *healthInput) {
		in.Now = time.Date(2026, 10, 17, 2, 10, 0, 0, time.UTC)
		in.OpenParts = []store.Partition{open}
	}); level != HealthOK {
		t.Fatalf("not yet overdue = %s", level)
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

func TestRecreatedTaskGetsANewTaskID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1", "i1")
	first := h.heartbeat("w1", "i1").GetTasks()[0].GetTaskId()
	deleted, _ := h.mgr.DeleteArchiveTask(ctx, &archivepb.DeleteArchiveTaskReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, deleted.GetRetInfo())
	if hb := h.heartbeat("w1", "i1"); !hb.GetActive() || len(hb.GetTasks()) != 0 {
		t.Fatalf("删除后任务列表应为空: %v", hb)
	}
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	hb := h.heartbeat("w1", "i1")
	if len(hb.GetTasks()) != 1 || hb.GetTasks()[0].GetTaskId() == first {
		t.Fatalf("重建的任务必须换新的 task_id，worker 才会丢弃旧状态: %v（旧 %s）", hb.GetTasks(), first)
	}
}

func TestShrinkingPartitionReportIsRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	h.heartbeat("w1", "i1")
	complete := func(rows, subjects uint64) *commonpb.RetInfo {
		run := h.pull("w1", "i1", 1)[0]
		r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded",
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
	emptyRun := h.pull("w1", "i1", 1)[0]
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: emptyRun.GetRunId(), LeaseToken: emptyRun.GetLeaseToken(), Status: "canceled"})
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

func TestSuccessfulRunClearsBackfillAndAssignmentCarriesRegistry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requireOK(t, h.create("ds_1m", func(r *archivepb.CreateArchiveTaskReq) {
		r.BackfillFrom = h.now.Add(-48 * time.Hour).Format(time.RFC3339)
	}).GetRetInfo())
	h.heartbeat("w1", "i1")
	run := h.pull("w1", "i1", 1)[0]
	if run.GetBackfillFrom() == "" || run.GetLastSuccessAt() != "" || run.GetTaskId() == "" {
		t.Fatalf("首次运行应带回补起点、没有水位: %v", run)
	}
	r, _ := h.worker.CompleteArchiveRun(ctx, &archivepb.CompleteArchiveRunReq{WorkerId: "w1", RunId: run.GetRunId(), LeaseToken: run.GetLeaseToken(), Status: "succeeded",
		Partitions: []*archivepb.PartitionReport{{PartitionKey: "2026-10-16", Status: "final", RangeStart: "2026-10-16T00:00:00Z", RangeEnd: "2026-10-17T00:00:00Z",
			FilePath: "f", FileSha256: "abc", RowCount: 10, SubjectCount: 1, FinalizedAt: "2026-10-20T00:00:00Z"}},
		Unrecoverable: []*archivepb.UnrecoverableRange{{StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}}})
	requireOK(t, r.GetRetInfo())
	task, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	if !task.BackfillFrom.IsZero() || task.LastSuccessAt.IsZero() || len(task.Unrecoverable) != 1 {
		t.Fatalf("成功后应清掉回补起点、记下水位并累积无法补齐区间: %+v", task)
	}
	now, _ := h.mgr.RunArchiveTaskNow(ctx, &archivepb.RunArchiveTaskNowReq{SpaceId: "crypto", DatasetId: "ds_1m"})
	requireOK(t, now.GetRetInfo())
	next := h.pull("w1", "i1", 1)[0]
	if next.GetBackfillFrom() != "" || next.GetLastSuccessAt() == "" || len(next.GetRegisteredPartitions()) != 1 || next.GetRegisteredPartitions()[0].GetStatus() != "final" {
		t.Fatalf("下发应带水位与分区登记、不再带回补起点: %v", next)
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
		DataKind: "time_series", Freq: "1m", Status: "active", DataNodeID: "node-0", Retention: "168h", SubjectTags: []string{"crypto_spot"}, Revision: 3}
	requireOK(t, h.create("ds_1m", nil).GetRetInfo())
	task, _ := h.store.GetTask(ctx, "crypto", "ds_1m")
	for _, want := range []string{`"name":"现货分钟K线"`, `"description":"币安现货 1 分钟 K 线"`, `"data_source_id":"binance_spot"`, `"subject_tags":["crypto_spot"]`, `"online_retention":"168h"`} {
		if !strings.Contains(task.MetaJSON, want) {
			t.Fatalf("meta_json 缺少 %s: %s", want, task.MetaJSON)
		}
	}
	if got := h.svc.assignment(store.Claimed{Task: task}, nil).GetDatasetMetaJson(); got != task.MetaJSON {
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

func TestPullEveryBarsStepIsAtMostOneDay(t *testing.T) {
	if _, err := pullEveryBars("1h", 24, 1); err != nil {
		t.Fatalf("1h × 24 = 1 天应允许: %v", err)
	}
	if _, err := pullEveryBars("4h", 7, 1); err == nil || !strings.Contains(err.Error(), "不能超过 1 天") {
		t.Fatalf("4h × 7 超过 1 天应拒绝: %v", err)
	}
	if n, err := pullEveryBars("1d", 5, 1); err != nil || n != 1 {
		t.Fatalf("1d 的 N 统一为 1: %d %v", n, err)
	}
}
