package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type recordingSyncer struct{ filters chan subjectsync.Filter }

// recordingTradingStatus 记下被触发检测的空间与市场类型。
type recordingTradingStatus struct{ calls chan string }

func (r *recordingTradingStatus) Run(_ context.Context, spaceID, marketType string) error {
	r.calls <- spaceID + "/" + marketType
	return nil
}

func (s *recordingSyncer) SupportedListers() map[string][]string { return nil }

func (s *recordingSyncer) SyncNow(_ context.Context, filter subjectsync.Filter, _ bool) (subjectsync.Report, error) {
	s.filters <- filter
	return subjectsync.Report{}, nil
}

func streamPlanContent(t *testing.T) string {
	t.Helper()
	plan := &marketfetch.Plan{V: 1, Executor: marketfetch.PlanExecutorStreaming, Fetch: marketfetch.PlanFetch{Concurrency: 1},
		Market:   marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "spot_http", MarketType: "spot"},
		Datasets: []marketfetch.PlanDataset{{DatasetID: "ds1", Frequency: "1m", LatenessS: 5, Copies: []marketfetch.PlanCopy{{From: 1000, Hash: "h", Count: 1, Subjects: []marketfetch.PlanSubject{{ID: "BTC-USDT", Index: 0}}}}}}}
	content := fetchplan.FetchPlanContent{
		Datasets: map[string]*fetchplan.PlanDataset{"ds1|1m": {PlanDatasetState: fetchplan.PlanDatasetState{DatasetID: "ds1", Frequency: "1m", Hash: "h", Count: 1, LatenessS: 5, Ingest: "stream"},
			Copies: []fetchplan.DatasetCopy{{From: 1000, Hash: "h", Count: 1, LatenessS: 5, Ingest: "stream"}}, Action: fetchplan.PlanActionAppend, P: 1000}},
		Functions: map[string]*fetchplan.PlanFunctionChange{"stream-crypto-spot": {Name: "stream-crypto-spot", Executor: "stream", Market: "m", New: plan, Target: plan}},
	}
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	return string(raw)
}

func TestStreamCollectorHeartbeatPullAndConfirm(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	syncer := &recordingSyncer{filters: make(chan subjectsync.Filter, 1)}
	stream := NewStreamService(db, StreamSettings{}, fetchplan.NewStreamMetrics(prometheus.NewRegistry()), syncer)
	trading := &recordingTradingStatus{calls: make(chan string, 1)}
	stream.SetTradingStatus(trading)
	service.SetStreamService(stream)

	rsp, err := stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1", SentAt: time.Now().UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Zero(t, rsp.GetPlanId(), "没有计划时版本为空")

	applied := applyPlan(t, db, "crypto", streamPlanContent(t))
	rsp, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1"})
	require.Equal(t, applied.ID, rsp.GetPlanId())
	pull, err := stream.PullStreamPlan(ctx, &pb.PullStreamPlanReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Len(t, pull.GetItems(), 1)
	require.EqualValues(t, DefaultStreamCommitReserveMS, pull.GetCommitReserveMs())
	var plan marketfetch.Plan
	require.NoError(t, json.Unmarshal([]byte(pull.GetItems()[0].GetPlanJson()), &plan))
	require.NoError(t, plan.Validate(), "下发的计划通过 Plan.Validate")
	require.Equal(t, marketfetch.PlanExecutorStreaming, pull.GetItems()[0].GetExecutor())

	// 心跳回报已应用的版本，Collector 据此确认。
	_, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1", AppliedPlanId: applied.ID,
		Conns: []*pb.StreamConnStatus{{Market: "spot", Frequency: "1m", Streams: 1, Reconnects: 2}}})
	worker, err := db.StreamWorkers().Get(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, applied.ID, worker.AppliedPlanID)

	// 手工暂停与系统生命周期暂停：都下发空计划。
	require.NoError(t, db.FetchPlans().SetPaused(ctx, "crypto", true))
	rsp, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1"})
	require.Zero(t, rsp.GetPlanId(), "手工暂停时计划号为空")
	pull, _ = stream.PullStreamPlan(ctx, &pb.PullStreamPlanReq{SpaceId: "crypto"})
	require.Empty(t, pull.GetItems())
	require.NoError(t, db.FetchPlans().SetPaused(ctx, "crypto", false))

	sysPaused := true
	stream.SetSystemPaused(func() bool { return sysPaused })
	rsp, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1"})
	require.Zero(t, rsp.GetPlanId(), "系统暂停时计划号为空")
	pull, _ = stream.PullStreamPlan(ctx, &pb.PullStreamPlanReq{SpaceId: "crypto"})
	require.Empty(t, pull.GetItems())
	sysPaused = false

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
	select {
	case call := <-trading.calls:
		require.Equal(t, "crypto/swap", call, "同步之后对这个市场再检测一次交易状态")
	case <-time.After(time.Second):
		t.Fatal("suspect did not trigger a trading status check")
	}
	status, err := service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.True(t, status.GetStream().GetOnline())
	require.Len(t, status.GetStream().GetRecent(), 1)
	require.Equal(t, "stream", status.GetFunctions()[0].GetExecutor())
}

func TestSetIngestModeSwitchesKlineTasksAndRequiresAnOnlineStream(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	stream := NewStreamService(db, StreamSettings{HeartbeatTimeout: time.Minute}, nil, nil)
	service.SetStreamService(stream)
	for _, id := range []string{"ds1", "ds2"} {
		require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task_" + id, TaskName: id, DataType: domain.DataTypeKline, Ingest: domain.IngestSCF,
			Enabled: true, TagIDs: []string{"binance_spot"}, CollectParams: `{"frequency":"1m"}`, ResultDatasetID: id}))
	}
	rsp, err := service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", Ingest: "stream"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode(), "stream 离线时拒绝切到 stream")
	rsp, err = service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", Ingest: "local"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode(), "stream 离线时拒绝切到 local")
	rsp, err = service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", Ingest: "pull"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "旧取值 pull 不再接受")

	_, _ = stream.StreamHeartbeat(ctx, &pb.StreamHeartbeatReq{SpaceId: "crypto", InstanceId: "hk-1"})
	rsp, err = service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", Ingest: "stream"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetModes(), 2, "--space-id 表示该空间全部 K 线任务")
	require.Equal(t, "stream", rsp.GetModes()[0].GetIngest())
	require.Equal(t, "task_ds1", rsp.GetModes()[0].GetTaskId())

	// 切回 scf 不要求在线；只改写这个数据集的任务，已打开计划的进度不动。
	_, err = db.FetchPlans().Create(ctx, "crypto", streamPlanContent(t), "{}")
	require.NoError(t, err)
	rsp, err = service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", DatasetId: "ds1", Ingest: "scf"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetModes(), 1)
	require.Equal(t, "scf", rsp.GetModes()[0].GetIngest())
	require.True(t, rsp.GetModes()[0].GetEnabled())
	record, _ := db.FetchPlans().Open(ctx, "crypto")
	require.Equal(t, "{}", record.Progress)

	status, err := service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Len(t, status.GetIngestModes(), 2)
	byDS := map[string]*pb.IngestMode{}
	for _, m := range status.GetIngestModes() {
		byDS[m.GetDatasetId()] = m
	}
	require.Equal(t, "stream", byDS["ds2"].GetIngest())
	require.True(t, status.GetStream().GetOnline())

	rsp, err = service.SetIngestMode(ctx, &pb.SetIngestModeReq{SpaceId: "crypto", DatasetId: "ds_missing", Ingest: "scf"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

// GetCollectorStatus 返回全部 K 线任务（含停用）的采集方式与交接生效点；停用任务不触发 stream 离线告警。
func TestCollectorStatusReadsHandoffPeriodAndIncludesDisabledTasks(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task_ds1", TaskName: "ds1", DataType: domain.DataTypeKline, Ingest: domain.IngestStreaming,
		Enabled: true, TagIDs: []string{"binance_spot"}, CollectParams: `{"frequency":"1m"}`, ResultDatasetID: "ds1"}))
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task_disabled", TaskName: "ds_off", DataType: domain.DataTypeKline, Ingest: domain.IngestStreaming,
		Enabled: false, TagIDs: []string{"binance_spot"}, CollectParams: `{"frequency":"5m"}`, ResultDatasetID: "ds_off"}))
	content := fetchplan.FetchPlanContent{Datasets: map[string]*fetchplan.PlanDataset{"ds1|1m": {
		PlanDatasetState: fetchplan.PlanDatasetState{DatasetID: "ds1", Frequency: "1m", Hash: "h", Count: 1, LatenessS: 5, Ingest: "stream"},
		Copies:           []fetchplan.DatasetCopy{{From: 1000, Hash: "h", Count: 1, LatenessS: 15, Ingest: "scf"}, {From: 1800, Hash: "h", Count: 1, LatenessS: 5, Ingest: "stream"}},
		Action:           fetchplan.PlanActionAppend, P: 1800,
	}}, Functions: map[string]*fetchplan.PlanFunctionChange{}}
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	applyPlan(t, db, "crypto", string(raw))
	got, err := service.GetCollectorStatus(ctx, &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Len(t, got.GetIngestModes(), 2)
	byDS := map[string]*pb.IngestMode{}
	for _, m := range got.GetIngestModes() {
		byDS[m.GetDatasetId()] = m
	}
	require.True(t, byDS["ds1"].GetEnabled())
	require.False(t, byDS["ds_off"].GetEnabled())
	require.Equal(t, map[string]string{"1m": time.Unix(1800, 0).UTC().Format(time.RFC3339)}, byDS["ds1"].GetEffectivePeriods())
}
