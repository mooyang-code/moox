package view

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestAppendRebuildPhasePreservesHistoryAndTerminal(t *testing.T) {
	details := ""
	for _, phase := range []string{"maintenance", "prepare", "backfill", "catch_up", "activate"} {
		details = appendRebuildPhase(details, phase)
	}
	details = appendRebuildPhase(details, "completed")
	var payload struct {
		Phase        string   `json:"phase"`
		PhaseHistory []string `json:"phase_history"`
	}
	if err := json.Unmarshal([]byte(details), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Phase != "completed" {
		t.Fatalf("terminal phase = %q, want completed", payload.Phase)
	}
	want := []string{"maintenance", "prepare", "backfill", "catch_up", "activate", "completed"}
	if len(payload.PhaseHistory) != len(want) {
		t.Fatalf("phase history = %#v, want %#v", payload.PhaseHistory, want)
	}
	for i := range want {
		if payload.PhaseHistory[i] != want[i] {
			t.Fatalf("phase history = %#v, want %#v", payload.PhaseHistory, want)
		}
	}
}

func TestRebuildErrorSummaryRedactsAndBoundsDetails(t *testing.T) {
	message := `password=secret token=abc {"api_key":"sekret","token":"json-token"} Authorization: Basic dXNlcjpwYXNz ` + strings.Repeat("x", 3000)
	got := rebuildErrorSummary(errors.New(message))
	for _, value := range []string{"secret", "abc", "sekret", "json-token", "dXNlcjpwYXNz"} {
		if strings.Contains(got, value) {
			t.Fatalf("sensitive values were not redacted: %q", got)
		}
	}
	if len(got) > 2048 {
		t.Fatalf("summary length = %d, want <= 2048", len(got))
	}
}

func TestRebuildTriggerReasonRecognizesRevisionScopedManualRequest(t *testing.T) {
	view := &pb.View{
		ActiveIndexId:       "index-a",
		DesiredViewRevision: 8,
		ActiveViewRevision:  7,
		Attributes:          map[string]string{coremetadata.ManualRebuildRevisionAttribute: "8"},
	}
	if got := rebuildTriggerReason(view, viewindex.ViewIndexStats{Exists: true}, false, false); got != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_MANUAL_REPAIR {
		t.Fatalf("trigger reason = %v, want manual repair", got)
	}
	view.Attributes[coremetadata.ManualRebuildRevisionAttribute] = "7"
	if got := rebuildTriggerReason(view, viewindex.ViewIndexStats{Exists: true}, false, false); got != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_DEFINITION_CHANGE {
		t.Fatalf("stale marker trigger reason = %v, want definition change", got)
	}
}

func TestRebuildTriggerReasonPrioritizesSingleSeriesCapacity(t *testing.T) {
	view := &pb.View{SpaceId: "crypto", ViewId: "view_binance_kline_1m", ActiveIndexId: "index-a", DesiredViewRevision: 1, ActiveViewRevision: 1}
	if got := rebuildTriggerReason(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1}, true, true); got != pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SERIES_CAPACITY {
		t.Fatalf("trigger reason=%v, want series capacity", got)
	}
}

// supersededLogMetadata 保存一组重建日志，按结果过滤列出，并记录每次结束日志的写入。
type supersededLogMetadata struct {
	maintenanceMetadata
	logs     []*pb.ViewRebuildLog
	updated  []*pb.ViewRebuildLog
	pageSize int
}

func (m *supersededLogMetadata) ListViewRebuildLogs(_ context.Context, req *pb.ListViewRebuildLogsReq, _ ...client.Option) (*pb.ListViewRebuildLogsRsp, error) {
	var logs []*pb.ViewRebuildLog
	for _, item := range m.logs {
		if req.GetResult() == 0 || item.GetResult() == req.GetResult() {
			logs = append(logs, proto.Clone(item).(*pb.ViewRebuildLog))
		}
	}
	// pageSize 大于 0 时按页切片，检验调用方翻页收集。
	hasMore := false
	if m.pageSize > 0 {
		start := min(int(req.GetPage().GetPage()-1)*m.pageSize, len(logs))
		end := min(start+m.pageSize, len(logs))
		hasMore = end < len(logs)
		logs = logs[start:end]
	}
	return &pb.ListViewRebuildLogsRsp{RetInfo: successRetInfo(), Logs: logs, PageResult: &pb.PageResult{Page: req.GetPage().GetPage(), Size: 100, HasMore: hasMore}}, nil
}

func (m *supersededLogMetadata) CreateViewRebuildLog(_ context.Context, req *pb.CreateViewRebuildLogReq, _ ...client.Option) (*pb.CreateViewRebuildLogRsp, error) {
	return &pb.CreateViewRebuildLogRsp{RetInfo: successRetInfo(), Log: req.GetLog()}, nil
}

func (m *supersededLogMetadata) UpdateViewRebuildLog(_ context.Context, req *pb.UpdateViewRebuildLogReq, _ ...client.Option) (*pb.UpdateViewRebuildLogRsp, error) {
	updated := proto.Clone(req.GetLog()).(*pb.ViewRebuildLog)
	m.updated = append(m.updated, updated)
	return &pb.UpdateViewRebuildLogRsp{RetInfo: successRetInfo(), Log: updated}, nil
}

func (m *supersededLogMetadata) UpsertSkippedViewRebuildLog(_ context.Context, req *pb.UpsertSkippedViewRebuildLogReq, _ ...client.Option) (*pb.UpsertSkippedViewRebuildLogRsp, error) {
	return &pb.UpsertSkippedViewRebuildLogRsp{RetInfo: successRetInfo(), Log: req.GetLog()}, nil
}

func TestCloseSupersededRebuildLogsFinishesOnlyAbandonedBuilds(t *testing.T) {
	svc, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	running := pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING
	logOf := func(id int64, buildID string, result pb.ViewRebuildResult) *pb.ViewRebuildLog {
		return &pb.ViewRebuildLog{
			LogId: id, SpaceId: "space", ViewId: "prices", BuildId: buildID, IndexId: "prices-b",
			TriggerReason: pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_DEFINITION_CHANGE, Result: result,
			EntriesWritten: uint64(id * 10), DetailsJson: `{"phase":"activate"}`,
		}
	}
	// build-active 已被元数据激活，只是激活后没来得及写结束日志。
	view := &pb.View{SpaceId: "space", ViewId: "prices", Attributes: map[string]string{coremetadata.ActiveBuildIDAttribute: "build-active"}}
	metadata := &supersededLogMetadata{pageSize: 2, logs: []*pb.ViewRebuildLog{
		logOf(1, "build-old", running),
		logOf(2, "build-new", running),
		logOf(3, "build-queued", running),
		logOf(4, "build-done", pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED),
		logOf(5, "build-lookup", running),
		logOf(6, "build-active", running),
		logOf(7, "build-older", running),
	}}
	opts := MaintenanceOptions{Metadata: metadata}
	// build-queued 的结束写入失败过、已在重试队列里；build-lookup 有按构建查找日志的占位项。两者都要留给重试写真实结果。
	svc.queueRebuildLogRetry(pendingRebuildLog{opts: opts, item: logOf(3, "build-queued", running), result: pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED, entries: 30})
	svc.queueRebuildLogRetry(pendingRebuildLog{opts: opts, view: view, buildID: "build-lookup", result: pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED})

	svc.closeSupersededRebuildLogs(context.Background(), opts, &pb.AuthInfo{}, view, "build-new")

	got := map[int64]*pb.ViewRebuildLog{}
	for _, item := range metadata.updated {
		got[item.GetLogId()] = item
	}
	if len(metadata.updated) != 3 || got[1] == nil || got[6] == nil || got[7] == nil {
		t.Fatalf("结束的日志 = %v，应只结束日志 1、6、7（跨页收集，跳过当前构建、重试队列中的和已结束的）", metadata.updated)
	}
	for _, id := range []int64{1, 7} {
		item := got[id]
		if item.GetResult() != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED || item.GetEntriesWritten() != uint64(id*10) || item.GetFinishedAt() == "" || !strings.Contains(item.GetErrorSummary(), "取代") {
			t.Fatalf("被取代的日志 %d 应记为失败、保留已写行数、带结束时间并说明被取代：%v", id, item)
		}
		if !strings.Contains(item.GetDetailsJson(), `"phase":"failed"`) {
			t.Fatalf("被取代的日志 %d 阶段应为 failed：%s", id, item.GetDetailsJson())
		}
	}
	if active := got[6]; active.GetResult() != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED || active.GetErrorSummary() != "" || active.GetEntriesWritten() != 60 {
		t.Fatalf("已激活构建的日志应补记为成功：%v", active)
	}
}
