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
	hash        string
	notModified bool
	sets        []domain.EngineSet
	identity    domain.EngineIdentity
	status      domain.EngineStatus
	job         store.RecalcJob
	pulledSet   domain.EngineSet
	found       bool
	progress    time.Time
	reportedErr string
	err         error
}

func (f *hubFake) Snapshot(_ context.Context, knownHash string) (string, bool, []domain.EngineSet, error) {
	if knownHash == f.hash {
		return f.hash, true, nil, f.err
	}
	return f.hash, f.notModified, f.sets, f.err
}

func (f *hubFake) Heartbeat(_ context.Context, id domain.EngineIdentity, status domain.EngineStatus) (time.Duration, error) {
	f.identity, f.status = id, status
	return 45 * time.Second, f.err
}

func (f *hubFake) Pull(context.Context, domain.EngineIdentity) (store.RecalcJob, domain.EngineSet, bool, error) {
	return f.job, f.pulledSet, f.found, f.err
}

func (f *hubFake) Report(_ context.Context, _ domain.EngineIdentity, _, _ string, progress time.Time, status, errText string) (store.RecalcJob, error) {
	f.progress, f.reportedErr = progress, errText
	job := f.job
	job.Status = status
	return job, f.err
}

var testEngine = &factorpb.EngineIdentity{EngineId: "factor-engine@mac", BootId: "boot", Version: "v1"}

func engineSetFixture() domain.EngineSet {
	return domain.EngineSet{
		Set:         domain.FactorSet{SetID: "fset_a", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m", Status: domain.SetStatusEnabled},
		Factors:     []domain.FactorDef{domain.MustDefinition("bias", "bias", "outputs:\n  bias: close / ts_mean(close, 5)\n")},
		ResultReady: true,
	}
}

func TestSyncEngineCatalogReturnsSetsWithSpec(t *testing.T) {
	hub := &hubFake{hash: "hash-1", sets: []domain.EngineSet{engineSetFixture()}}
	svc := NewEngineService(hub, nil)

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "hash-1", rsp.GetCatalogHash())
	require.False(t, rsp.GetNotModified())
	require.Len(t, rsp.GetSets(), 1)
	require.True(t, rsp.GetSets()[0].GetResultReady())
	require.Contains(t, rsp.GetSets()[0].GetFactors()[0].GetSpec(), "ts_mean(close, 5)")
}

func TestSyncEngineCatalogNotModified(t *testing.T) {
	svc := NewEngineService(&hubFake{hash: "hash-1", sets: []domain.EngineSet{engineSetFixture()}}, nil)

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{Engine: testEngine, KnownHash: "hash-1"})

	require.NoError(t, err)
	require.True(t, rsp.GetNotModified())
	require.Empty(t, rsp.GetSets())
}

func TestSyncEngineCatalogRequiresEngineID(t *testing.T) {
	svc := NewEngineService(&hubFake{}, nil)

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestEngineHeartbeatPassesStatus(t *testing.T) {
	hub := &hubFake{}
	svc := NewEngineService(hub, nil)
	syncedAt := time.Unix(1000, 0).UTC()

	rsp, err := svc.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{
		ConsumerRunning: true, ComputeWorkers: 8, ExprVersion: "expr/1", CatalogHash: "hash-1",
		CatalogSyncedAt: syncedAt.Format(time.RFC3339Nano),
		Lanes:           []*factorpb.FactorLaneStatus{{SetId: "fset_a", Queued: 1, Active: true}},
		RecentRuns:      []*factorpb.SetRunSummary{{SetId: "fset_a", LastPeriodTime: 900, LastStatus: "complete"}},
	}})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, int64(45), rsp.GetLeaseTtlSeconds())
	require.Equal(t, "factor-engine@mac", hub.identity.EngineID)
	require.True(t, hub.status.ConsumerRunning)
	require.Equal(t, int32(8), hub.status.ComputeWorkers)
	require.Equal(t, "expr/1", hub.status.ExprVersion)
	require.Equal(t, syncedAt, hub.status.CatalogSyncedAt)
	require.Equal(t, []domain.LaneStatus{{SetID: "fset_a", Queued: 1, Active: true}}, hub.status.Lanes)
	require.Equal(t, "complete", hub.status.RecentRuns[0].LastStatus)
}

func TestEngineHeartbeatConflictMapsToRetCode(t *testing.T) {
	svc := NewEngineService(&hubFake{err: fmt.Errorf("%w: factor-engine@other", enginehub.ErrLeaseConflict)}, nil)

	rsp, err := svc.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
}

func TestPullRecalcJobReturnsLeaseAndSet(t *testing.T) {
	hub := &hubFake{found: true, pulledSet: engineSetFixture(), job: store.RecalcJob{
		JobID: "job-1", SetID: "fset_a", StartTime: 60, EndTime: 600, ProgressTime: 120,
		Status: store.RecalcStatusRunning, EngineID: "factor-engine@mac", LeaseToken: "token-1",
	}}
	svc := NewEngineService(hub, nil)

	rsp, err := svc.PullRecalcJob(context.Background(), &factorpb.PullRecalcJobReq{Engine: testEngine})

	require.NoError(t, err)
	require.True(t, rsp.GetFound())
	require.Equal(t, "token-1", rsp.GetLeaseToken())
	require.Equal(t, "job-1", rsp.GetJob().GetJobId())
	require.Equal(t, "factor-engine@mac", rsp.GetJob().GetEngineId())
	require.Equal(t, time.Unix(120, 0).UTC().Format(time.RFC3339Nano), rsp.GetJob().GetProgressTime())
	require.Equal(t, "fset_a", rsp.GetSet().GetFactorSet().GetSetId())
}

func TestPullRecalcJobNotFound(t *testing.T) {
	svc := NewEngineService(&hubFake{}, nil)

	rsp, err := svc.PullRecalcJob(context.Background(), &factorpb.PullRecalcJobReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.False(t, rsp.GetFound())
}

func TestReportRecalcProgressConvertsRFC3339(t *testing.T) {
	hub := &hubFake{job: store.RecalcJob{JobID: "job-1"}}
	svc := NewEngineService(hub, nil)
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
	svc := NewEngineService(&hubFake{}, nil)

	rsp, err := svc.ReportRecalcProgress(context.Background(), &factorpb.ReportRecalcProgressReq{
		JobId: "job-1", LeaseToken: "token-1", ProgressTime: "yesterday",
	})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestReportRecalcProgressStaleTokenIsConflict(t *testing.T) {
	svc := NewEngineService(&hubFake{err: fmt.Errorf("%w: stale", store.ErrConflict)}, nil)

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
	runs   map[string]domain.SetRunSummary
}

func (f *engineAPIFake) Engine(context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error) {
	return f.info, f.status, f.seen, nil
}

func (f *engineAPIFake) LatestRun(setID string) domain.SetRunSummary {
	if run, ok := f.runs[setID]; ok {
		return run
	}
	return domain.SetRunSummary{SetID: setID}
}

func TestGetStatusReadsEngineHeartbeat(t *testing.T) {
	heartbeat := time.Unix(2000, 0).UTC()
	svc := NewService(&catalogFake{}, &recalcFake{}, WithEngineAPI(&engineAPIFake{seen: true,
		info: domain.EngineInfo{
			EngineIdentity: domain.EngineIdentity{EngineID: "factor-engine@mac", BootID: "boot", Version: "v1"},
			Online:         true, LastHeartbeatAt: heartbeat, CatalogHash: "hash-1", CatalogInSync: true,
		},
		status: domain.EngineStatus{ConsumerRunning: true, ComputeWorkers: 8, ExprVersion: "expr/0",
			Lanes:      []domain.LaneStatus{{SetID: "fset_a", Queued: 2}},
			RecentRuns: []domain.SetRunSummary{{SetID: "fset_a", LastPeriodTime: 1900, LastStatus: "complete", LagSeconds: 100}}},
	}))

	rsp, err := svc.GetStatus(context.Background(), &factorpb.GetStatusReq{})

	require.NoError(t, err)
	require.True(t, rsp.GetConsumerRunning())
	require.Equal(t, int32(8), rsp.GetComputeWorkers())
	require.Equal(t, "expr/0", rsp.GetEngineExprVersion())
	require.Equal(t, factorexpr.ExprVersion, rsp.GetManagerExprVersion(), "版本不一致时由控制台提示")
	require.Len(t, rsp.GetLanes(), 1)
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

func TestGetFactorSetLastRunFromHeartbeat(t *testing.T) {
	catalog := &catalogFake{set: domain.FactorSet{SetID: "fset_a", Status: domain.SetStatusEnabled}}
	svc := NewService(catalog, &recalcFake{}, WithEngineAPI(&engineAPIFake{seen: true, runs: map[string]domain.SetRunSummary{
		"fset_a": {SetID: "fset_a", LastPeriodTime: 1900, LastStatus: "complete", LagSeconds: 60},
	}}))

	rsp, err := svc.GetFactorSet(context.Background(), &factorpb.GetFactorSetReq{SetId: "fset_a"})

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

	conflict := NewEngineService(&hubFake{err: fmt.Errorf("%w: other", enginehub.ErrLeaseConflict)}, inbox)
	beat, _ := conflict.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{}})
	require.Empty(t, beat.GetFilled(), "不持有租约的引擎拿不到通知")
	holder := NewEngineService(&hubFake{}, inbox)
	beat, _ = holder.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{}})
	require.Len(t, beat.GetFilled(), 1)
	require.Equal(t, []string{"A"}, beat.GetFilled()[0].GetAbsentSubjects())
	beat, _ = holder.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{}})
	require.Empty(t, beat.GetFilled())
}
