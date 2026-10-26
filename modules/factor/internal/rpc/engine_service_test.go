package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/enginehub"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/stretchr/testify/require"
)

type hubFake struct {
	hash          string
	notModified   bool
	sets          []domain.EngineTask
	identity      domain.EngineIdentity
	status        domain.EngineStatus
	job           store.RecalcJob
	pulledTask    domain.EngineTask
	found         bool
	progress      time.Time
	reportedErr   string
	defaultFilled map[string][]store.TimeRange
	err           error
	// noLease 让 HoldsLease 返回 false（调用方不是持有租约的引擎）。
	noLease bool
}

func (f *hubFake) HoldsLease(domain.EngineIdentity) bool { return !f.noLease }

func (f *hubFake) Snapshot(_ context.Context, knownHash string) (string, bool, []domain.EngineTask, error) {
	if knownHash == f.hash {
		return f.hash, true, nil, f.err
	}
	return f.hash, f.notModified, f.sets, f.err
}

func (f *hubFake) Heartbeat(_ context.Context, id domain.EngineIdentity, status domain.EngineStatus) (time.Duration, error) {
	f.identity, f.status = id, status
	return 45 * time.Second, f.err
}

func (f *hubFake) Pull(context.Context, domain.EngineIdentity) (store.RecalcJob, domain.EngineTask, bool, error) {
	return f.job, f.pulledTask, f.found, f.err
}

func (f *hubFake) Report(_ context.Context, _ domain.EngineIdentity, _, _ string, progress time.Time, status, errText string, defaultFilled map[string][]store.TimeRange) (store.RecalcJob, error) {
	f.defaultFilled = defaultFilled
	f.progress, f.reportedErr = progress, errText
	job := f.job
	job.Status = status
	return job, f.err
}

var testEngine = &factorpb.EngineIdentity{EngineId: "factor-engine@mac", BootId: "boot", Version: "v1"}

func engineTaskFixture() domain.EngineTask {
	return domain.EngineTask{
		Set:         domain.FactorTask{TaskID: "ftask_a", SpaceID: "crypto", Name: "行情因子", SourceDatasetID: "dataset_a", Freq: "1m", Status: domain.TaskStatusEnabled},
		Members:     domain.AsMembers(domain.MustDefinition("bias", "bias", "output: close / ts_mean(close, 5)\n")),
		ResultReady: true,
	}
}

func TestSyncEngineCatalogReturnsTasksWithSpec(t *testing.T) {
	hub := &hubFake{hash: "hash-1", sets: []domain.EngineTask{engineTaskFixture()}}
	svc := NewEngineService(hub, nil, nil)

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "hash-1", rsp.GetCatalogHash())
	require.False(t, rsp.GetNotModified())
	require.Len(t, rsp.GetTasks(), 1)
	require.True(t, rsp.GetTasks()[0].GetResultReady())
	require.Contains(t, rsp.GetTasks()[0].GetMembers()[0].GetFactor().GetSpec(), "ts_mean(close, 5)")
}

func TestSyncEngineCatalogNotModified(t *testing.T) {
	svc := NewEngineService(&hubFake{hash: "hash-1", sets: []domain.EngineTask{engineTaskFixture()}}, nil, nil)

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{Engine: testEngine, KnownHash: "hash-1"})

	require.NoError(t, err)
	require.True(t, rsp.GetNotModified())
	require.Empty(t, rsp.GetTasks())
}

func TestSyncEngineCatalogRequiresEngineID(t *testing.T) {
	svc := NewEngineService(&hubFake{}, nil, nil)

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestEngineHeartbeatPassesStatus(t *testing.T) {
	hub := &hubFake{}
	svc := NewEngineService(hub, nil, nil)
	syncedAt := time.Unix(1000, 0).UTC()

	rsp, err := svc.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{
		ConsumerRunning: true, ComputeWorkers: 8, ExprVersion: "expr/1", CatalogHash: "hash-1",
		CatalogSyncedAt: syncedAt.Format(time.RFC3339Nano),
		Runners:         []*factorpb.FactorRunnerStatus{{TaskId: "ftask_a", Queued: 1, Active: true}},
		RecentRuns:      []*factorpb.TaskRunSummary{{TaskId: "ftask_a", LastPeriodTime: 900, LastStatus: "complete"}},
	}})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, int64(45), rsp.GetLeaseTtlSeconds())
	require.Equal(t, "factor-engine@mac", hub.identity.EngineID)
	require.True(t, hub.status.ConsumerRunning)
	require.Equal(t, int32(8), hub.status.ComputeWorkers)
	require.Equal(t, "expr/1", hub.status.ExprVersion)
	require.Equal(t, syncedAt, hub.status.CatalogSyncedAt)
	require.Equal(t, []domain.RunnerStatus{{TaskID: "ftask_a", Queued: 1, Active: true}}, hub.status.Runners)
	require.Equal(t, "complete", hub.status.RecentRuns[0].LastStatus)
}

func TestEngineHeartbeatConflictMapsToRetCode(t *testing.T) {
	svc := NewEngineService(&hubFake{err: fmt.Errorf("%w: factor-engine@other", enginehub.ErrLeaseConflict)}, nil, nil)

	rsp, err := svc.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
}

func TestPullRecalcJobReturnsLeaseAndTask(t *testing.T) {
	hub := &hubFake{found: true, pulledTask: engineTaskFixture(), job: store.RecalcJob{
		JobID: "job-1", TaskID: "ftask_a", StartTime: 60, EndTime: 600, ProgressTime: 120,
		Status: store.RecalcStatusRunning, EngineID: "factor-engine@mac", LeaseToken: "token-1",
	}}
	svc := NewEngineService(hub, nil, nil)

	rsp, err := svc.PullRecalcJob(context.Background(), &factorpb.PullRecalcJobReq{Engine: testEngine})

	require.NoError(t, err)
	require.True(t, rsp.GetFound())
	require.Equal(t, "token-1", rsp.GetLeaseToken())
	require.Equal(t, "job-1", rsp.GetJob().GetJobId())
	require.Equal(t, "factor-engine@mac", rsp.GetJob().GetEngineId())
	require.Equal(t, time.Unix(120, 0).UTC().Format(time.RFC3339Nano), rsp.GetJob().GetProgressTime())
	require.Equal(t, "ftask_a", rsp.GetTask().GetFactorTask().GetTaskId())
}

func TestPullRecalcJobNotFound(t *testing.T) {
	svc := NewEngineService(&hubFake{}, nil, nil)

	rsp, err := svc.PullRecalcJob(context.Background(), &factorpb.PullRecalcJobReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.False(t, rsp.GetFound())
}

func TestReportRecalcProgressConvertsRFC3339(t *testing.T) {
	hub := &hubFake{job: store.RecalcJob{JobID: "job-1"}}
	svc := NewEngineService(hub, nil, nil)
	progress := time.Unix(300, 0).UTC()

	rsp, err := svc.ReportRecalcProgress(context.Background(), &factorpb.ReportRecalcProgressReq{
		Engine: testEngine, JobId: "job-1", LeaseToken: "token-1",
		ProgressTime: progress.Format(time.RFC3339), Status: store.RecalcStatusRunning, Error: "degraded: failed_subjects=1",
	})

	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusRunning, rsp.GetJobStatus())
	require.Equal(t, progress, hub.progress)
	require.Equal(t, "degraded: failed_subjects=1", hub.reportedErr)
}

func TestReportRecalcProgressRejectsBadTime(t *testing.T) {
	svc := NewEngineService(&hubFake{}, nil, nil)

	rsp, err := svc.ReportRecalcProgress(context.Background(), &factorpb.ReportRecalcProgressReq{
		JobId: "job-1", LeaseToken: "token-1", ProgressTime: "yesterday",
	})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestReportRecalcProgressStaleTokenIsConflict(t *testing.T) {
	svc := NewEngineService(&hubFake{err: fmt.Errorf("%w: stale", store.ErrConflict)}, nil, nil)

	rsp, err := svc.ReportRecalcProgress(context.Background(), &factorpb.ReportRecalcProgressReq{
		JobId: "job-1", LeaseToken: "token-1", ProgressTime: time.Unix(300, 0).UTC().Format(time.RFC3339),
	})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
}

type engineAPIFake struct {
	info   domain.EngineInfo
	status domain.EngineStatus
	seen   bool
	runs   map[string]domain.TaskRunSummary
}

func (f *engineAPIFake) Engine(context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error) {
	return f.info, f.status, f.seen, nil
}

func (f *engineAPIFake) LatestRun(taskID string) domain.TaskRunSummary {
	if run, ok := f.runs[taskID]; ok {
		return run
	}
	return domain.TaskRunSummary{TaskID: taskID}
}

func TestGetStatusReadsEngineHeartbeat(t *testing.T) {
	heartbeat := time.Unix(2000, 0).UTC()
	svc := NewService(&catalogFake{}, &recalcFake{}, WithEngineAPI(&engineAPIFake{seen: true,
		info: domain.EngineInfo{
			EngineIdentity: domain.EngineIdentity{EngineID: "factor-engine@mac", BootID: "boot", Version: "v1"},
			Online:         true, LastHeartbeatAt: heartbeat, CatalogHash: "hash-1", CatalogInSync: true,
		},
		status: domain.EngineStatus{ConsumerRunning: true, ComputeWorkers: 8, ExprVersion: "expr/0",
			Runners:    []domain.RunnerStatus{{TaskID: "ftask_a", Queued: 2}},
			RecentRuns: []domain.TaskRunSummary{{TaskID: "ftask_a", LastPeriodTime: 1900, LastStatus: "complete", LagSeconds: 100}}},
	}))

	rsp, err := svc.GetStatus(context.Background(), &factorpb.GetStatusReq{})

	require.NoError(t, err)
	require.True(t, rsp.GetConsumerRunning())
	require.Equal(t, int32(8), rsp.GetComputeWorkers())
	require.Equal(t, "expr/0", rsp.GetEngineExprVersion())
	require.Equal(t, factorexpr.ExprVersion, rsp.GetManagerExprVersion(), "版本不一致时由控制台提示")
	require.Len(t, rsp.GetRunners(), 1)
	require.Equal(t, int64(100), rsp.GetRecentRuns()[0].GetLagSeconds())
	require.Equal(t, "factor-engine@mac", rsp.GetEngine().GetEngineId())
	require.True(t, rsp.GetEngine().GetOnline())
	require.True(t, rsp.GetEngine().GetCatalogInSync())
	require.Equal(t, heartbeat.Format(time.RFC3339Nano), rsp.GetEngine().GetLastHeartbeatAt())
}

func TestGetStatusEngineOfflineWhenNoHeartbeat(t *testing.T) {
	svc := NewService(&catalogFake{}, &recalcFake{}, WithEngineAPI(&engineAPIFake{}))

	rsp, err := svc.GetStatus(context.Background(), &factorpb.GetStatusReq{})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.NotNil(t, rsp.GetEngine())
	require.False(t, rsp.GetEngine().GetOnline())
	require.False(t, rsp.GetConsumerRunning())
}

func TestGetStatusReportsCatalogOutOfSync(t *testing.T) {
	svc := NewService(&catalogFake{}, &recalcFake{}, WithEngineAPI(&engineAPIFake{seen: true,
		info: domain.EngineInfo{EngineIdentity: domain.EngineIdentity{EngineID: "factor-engine@mac"}, Online: true, CatalogInSync: false}}))

	rsp, err := svc.GetStatus(context.Background(), &factorpb.GetStatusReq{})

	require.NoError(t, err)
	require.False(t, rsp.GetEngine().GetCatalogInSync())
}

func TestGetFactorTaskLastRunFromHeartbeat(t *testing.T) {
	catalog := &catalogFake{set: domain.FactorTask{TaskID: "ftask_a", Status: domain.TaskStatusEnabled}}
	svc := NewService(catalog, &recalcFake{}, WithEngineAPI(&engineAPIFake{seen: true, runs: map[string]domain.TaskRunSummary{
		"ftask_a": {TaskID: "ftask_a", LastPeriodTime: 1900, LastStatus: "complete", LagSeconds: 60},
	}}))

	rsp, err := svc.GetFactorTask(context.Background(), &factorpb.GetFactorTaskReq{TaskId: "ftask_a"})

	require.NoError(t, err)
	require.Equal(t, "complete", rsp.GetLastRun().GetLastStatus())
	require.Equal(t, int64(60), rsp.GetLastRun().GetLagSeconds())
}

// 补齐通知暂存在因子管理服务里，只在心跳成功（持有租约）时交给引擎，交出后清空。
func TestPeriodsFilledAreDeliveredOnTheNextHeartbeat(t *testing.T) {
	inbox := NewFilledInbox()
	service := NewService(nil, nil, WithFilledInbox(inbox))
	rsp, err := service.NotifyPeriodsFilled(context.Background(), &factorpb.NotifyPeriodsFilledReq{Filled: []*factorpb.PeriodsFilled{{SpaceId: "crypto", DatasetId: "ds", Frequency: "1m", From: 60, To: 120, AbsentSubjects: []string{"A"}}}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	bad, _ := service.NotifyPeriodsFilled(context.Background(), &factorpb.NotifyPeriodsFilledReq{Filled: []*factorpb.PeriodsFilled{{SpaceId: "crypto", DatasetId: "ds", Frequency: "1m", From: 120, To: 60}}})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())

	conflict := NewEngineService(&hubFake{err: fmt.Errorf("%w: other", enginehub.ErrLeaseConflict)}, nil, inbox)
	beat, _ := conflict.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{}})
	require.Empty(t, beat.GetFilled(), "不持有租约的引擎拿不到通知")
	holder := NewEngineService(&hubFake{}, nil, inbox)
	beat, _ = holder.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{}})
	require.Len(t, beat.GetFilled(), 1)
	require.Equal(t, []string{"A"}, beat.GetFilled()[0].GetAbsentSubjects())
	beat, _ = holder.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{}})
	require.Empty(t, beat.GetFilled())
}
