package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type recordingSyncer struct{ filters chan subjectsync.Filter }

func (s *recordingSyncer) SyncNow(_ context.Context, filter subjectsync.Filter, _ bool) (subjectsync.Report, error) {
	s.filters <- filter
	return subjectsync.Report{}, nil
}

func streamPlanContent(t *testing.T) string {
	t.Helper()
	plan := &marketfetch.Plan{V: 1, Executor: marketfetch.PlanExecutorStreaming, Fetch: marketfetch.PlanFetch{Concurrency: 1},
		Market:   marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "spot_http", MarketType: "spot"},
		Datasets: []marketfetch.PlanDataset{{DatasetID: "ds1", Frequency: "1m", LatenessS: 5, Copies: []marketfetch.PlanCopy{{From: 1000, Hash: "h", Count: 1, Subjects: []marketfetch.PlanSubject{{ID: "BTC-USDT", Index: 0}}}}}}}
	content := marketfetch.FetchPlanContent{
		Datasets:  map[string]*marketfetch.PlanDatasetChange{"ds1|1m": {PlanDatasetState: marketfetch.PlanDatasetState{DatasetID: "ds1", Frequency: "1m", Hash: "h", Count: 1, LatenessS: 5, Ingest: "stream"}, Changed: true, P: 60}},
		Functions: map[string]*marketfetch.PlanFunctionChange{"stream-crypto-spot": {Name: "stream-crypto-spot", Executor: "stream", Market: "m", New: plan}},
	}
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	return string(raw)
}

func TestStreamCollectorHeartbeatPullAndConfirm(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	syncer := &recordingSyncer{filters: make(chan subjectsync.Filter, 1)}
	stream := NewStreamService(db, StreamSettings{}, marketfetch.NewStreamMetrics(prometheus.NewRegistry()), syncer)
	service.SetStreamService(stream)

	rsp, err := stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1", SentAtMs: time.Now().UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Zero(t, rsp.GetPlanId(), "没有计划时版本为空")

	applied := applyPlan(t, db, "crypto", streamPlanContent(t))
	rsp, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1"})
	require.Equal(t, applied.ID, rsp.GetPlanId())
	require.Equal(t, marketfetch.StreamStageFinal, rsp.GetStage())
	pull, err := stream.PullStreamPlan(ctx, &pb.PullStreamPlanReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Len(t, pull.GetItems(), 1)
	require.EqualValues(t, DefaultStreamCommitReserveMS, pull.GetCommitReserveMs())
	var plan marketfetch.Plan
	require.NoError(t, json.Unmarshal([]byte(pull.GetItems()[0].GetPlanJson()), &plan))
	require.NoError(t, plan.Validate(), "下发的计划通过 Plan.Validate")
	require.Equal(t, marketfetch.PlanExecutorStreaming, pull.GetItems()[0].GetExecutor())

	// 心跳回报已应用的版本，Collector 据此确认。
	_, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1", AppliedPlanId: applied.ID, AppliedStage: marketfetch.StreamStageFinal,
		Conns: []*pb.StreamConnStatus{{Market: "spot", Frequency: "1m", Streams: 1, Reconnects: 2}}})
	worker, err := db.StreamWorkers().Get(ctx, "crypto")
	require.NoError(t, err)
	var confirmed marketfetch.StreamApplied
	require.NoError(t, json.Unmarshal([]byte(worker.Applied), &confirmed))
	require.Equal(t, marketfetch.StreamApplied{ID: applied.ID, Stage: marketfetch.StreamStageFinal}, confirmed)

	// 暂停：下发空计划。
	require.NoError(t, db.FetchPlans().SetPaused(ctx, "crypto", true))
	rsp, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1"})
	require.Equal(t, marketfetch.StreamStagePaused, rsp.GetStage())
	pull, _ = stream.PullStreamPlan(ctx, &pb.PullStreamPlanReq{SpaceId: "crypto"})
	require.Empty(t, pull.GetItems())
	require.NoError(t, db.FetchPlans().SetPaused(ctx, "crypto", false))

	// 统计进最近记录；疑似下架立即按市场同步标签。
	_, err = stream.ReportStreamPeriods(ctx, &pb.ReportStreamPeriodsReq{SpaceId: "crypto", Periods: []*pb.StreamPeriodStat{{Market: "spot", DatasetId: "ds1", Frequency: "1m", Period: 60, Expected: 2, Arrived: 1, Missing: 1, Committed: true}},
		Suspects: []*pb.StreamSuspect{{Market: "swap", SubjectId: "ABC-USDT", Reason: "x"}}})
	require.NoError(t, err)
	select {
	case filter := <-syncer.filters:
		require.Equal(t, subjectsync.Filter{SpaceID: "crypto", MarketType: "swap"}, filter)
	case <-time.After(time.Second):
		t.Fatal("suspect did not trigger a subject sync")
	}
	status, err := service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.True(t, status.GetStream().GetOnline())
	require.Len(t, status.GetStream().GetRecent(), 1)
	require.Equal(t, "stream", status.GetFunctions()[0].GetExecutor())
}

func TestSetIngestModeRequiresAnOnlineStreamAndMarksForce(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	stream := NewStreamService(db, StreamSettings{HeartbeatTimeout: time.Minute}, nil, nil)
	service.SetStreamService(stream)
	require.NoError(t, db.IngestModes().Initialize(ctx, []store.IngestMode{
		{SpaceID: "crypto", DatasetID: "ds1", Mode: store.IngestPull, Executor: store.ExecutorSCF},
		{SpaceID: "crypto", DatasetID: "ds2", Mode: store.IngestPull, Executor: store.ExecutorSCF},
	}))
	rsp, err := service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", Mode: "stream"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode(), "stream 离线时拒绝切到 stream")

	_, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1"})
	rsp, err = service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", Mode: "stream"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetModes(), 2, "--space-id 表示该空间全部数据集")
	require.Equal(t, "local", rsp.GetModes()[0].GetExecutor())

	// 切回 pull 不要求在线；--force 标记数据集并让已打开的计划不再等失联端。
	open, err := db.FetchPlans().Create(ctx, "crypto", streamPlanContent(t), "{}")
	require.NoError(t, err)
	rsp, err = service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", DatasetId: "ds1", Mode: "pull", Force: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, open.ID, rsp.GetPlanId())
	modes, _ := db.IngestModes().List(ctx, "crypto")
	require.Equal(t, store.IngestMode{SpaceID: "crypto", DatasetID: "ds1", Mode: "pull", Executor: "scf", Force: true, UpdatedAt: modes["ds1"].UpdatedAt}, modes["ds1"])
	record, _ := db.FetchPlans().Open(ctx, "crypto")
	require.Equal(t, "{}", record.Progress, "--force 只写采集方式表，不改写已打开计划的进度")

	got, err := service.GetIngestMode(ctx, &pb.GetIngestModeReq{SpaceId: "crypto", DatasetId: "ds2"})
	require.NoError(t, err)
	require.Len(t, got.GetModes(), 1)
	require.Equal(t, "stream", got.GetModes()[0].GetMode())
	require.True(t, got.GetStream().GetOnline())
}
