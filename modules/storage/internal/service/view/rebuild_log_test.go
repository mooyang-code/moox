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
	logs    []*pb.ViewRebuildLog
	updated []*pb.ViewRebuildLog
}

func (m *supersededLogMetadata) ListViewRebuildLogs(_ context.Context, req *pb.ListViewRebuildLogsReq, _ ...client.Option) (*pb.ListViewRebuildLogsRsp, error) {
	var logs []*pb.ViewRebuildLog
	for _, item := range m.logs {
		if req.GetResult() == 0 || item.GetResult() == req.GetResult() {
			logs = append(logs, proto.Clone(item).(*pb.ViewRebuildLog))
		}
	}
	return &pb.ListViewRebuildLogsRsp{RetInfo: successRetInfo(), Logs: logs, PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
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
	view := &pb.View{SpaceId: "space", ViewId: "prices"}
	metadata := &supersededLogMetadata{logs: []*pb.ViewRebuildLog{
		logOf(1, "build-old", running),
		logOf(2, "build-new", running),
		logOf(3, "build-queued", running),
		logOf(4, "build-done", pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED),
	}}
	opts := MaintenanceOptions{Metadata: metadata}
	// build-queued 的结束写入失败过、已在重试队列里，要留给重试写真实结果。
	svc.queueRebuildLogRetry(pendingRebuildLog{opts: opts, item: logOf(3, "build-queued", running), result: pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SUCCEEDED, entries: 30})

	svc.closeSupersededRebuildLogs(context.Background(), opts, &pb.AuthInfo{}, view, "build-new")

	if len(metadata.updated) != 1 {
		t.Fatalf("结束了 %d 条日志，只应结束被取代的 build-old：%v", len(metadata.updated), metadata.updated)
	}
	got := metadata.updated[0]
	if got.GetLogId() != 1 || got.GetResult() != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED {
		t.Fatalf("结束的日志 = %v，应为日志 1 且结果为失败", got)
	}
	if got.GetEntriesWritten() != 10 || got.GetFinishedAt() == "" || !strings.Contains(got.GetErrorSummary(), "取代") {
		t.Fatalf("结束的日志应保留已写行数、带结束时间并说明被取代：%v", got)
	}
	if !strings.Contains(got.GetDetailsJson(), `"phase":"failed"`) {
		t.Fatalf("结束的日志阶段应为 failed：%s", got.GetDetailsJson())
	}
}
