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

func (f *hubFake) Report(_ context.Context, _, _ string, progress time.Time, status, errText string) (store.RecalcJob, error) {
	f.progress, f.reportedErr = progress, errText
	job := f.job
	job.Status = status
	return job, f.err
}

var testEngine = &factorpb.EngineIdentity{EngineId: "factor-engine@mac", BootId: "boot", Version: "v1"}

func engineSetFixture() domain.EngineSet {
	return domain.EngineSet{
		Set: domain.FactorSet{SetID: "fset_a", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m", Status: domain.SetStatusEnabled},
		Factors: []domain.FactorDef{{
			FactorID: "bias", Name: "bias", FactorType: domain.FactorTypeTimeSeries,
			SourceCode: "def compute(): pass", SourceHash: "h", InputColumns: []string{"close"}, Outputs: []string{"bias"},
			ParamsJSON: "{}", LookbackPeriods: 5,
		}},
		ResultReady: true,
	}
}

func TestSyncEngineCatalogReturnsSetsWithSource(t *testing.T) {
	hub := &hubFake{hash: "hash-1", sets: []domain.EngineSet{engineSetFixture()}}
	svc := NewEngineService(hub)

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "hash-1", rsp.GetCatalogHash())
	require.False(t, rsp.GetNotModified())
	require.Len(t, rsp.GetSets(), 1)
	require.True(t, rsp.GetSets()[0].GetResultReady())
	require.Equal(t, "def compute(): pass", rsp.GetSets()[0].GetFactors()[0].GetSourceCode())
}

func TestSyncEngineCatalogNotModified(t *testing.T) {
	svc := NewEngineService(&hubFake{hash: "hash-1", sets: []domain.EngineSet{engineSetFixture()}})

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{Engine: testEngine, KnownHash: "hash-1"})

	require.NoError(t, err)
	require.True(t, rsp.GetNotModified())
	require.Empty(t, rsp.GetSets())
}

func TestSyncEngineCatalogRequiresEngineID(t *testing.T) {
	svc := NewEngineService(&hubFake{})

	rsp, err := svc.SyncEngineCatalog(context.Background(), &factorpb.SyncEngineCatalogReq{})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestEngineHeartbeatPassesStatus(t *testing.T) {
	hub := &hubFake{}
	svc := NewEngineService(hub)
	syncedAt := time.Unix(1000, 0).UTC()

	rsp, err := svc.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine, Status: &factorpb.EngineRuntimeStatus{
		ConsumerRunning: true, PythonWorkers: 8, PythonBusy: 2, CatalogHash: "hash-1",
		CatalogSyncedAt: syncedAt.Format(time.RFC3339Nano),
		Lanes:           []*factorpb.FactorLaneStatus{{SetId: "fset_a", Queued: 1, Active: true}},
		RecentRuns:      []*factorpb.SetRunSummary{{SetId: "fset_a", LastPeriodTime: 900, LastStatus: "complete"}},
	}})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, int64(45), rsp.GetLeaseTtlSeconds())
	require.Equal(t, "factor-engine@mac", hub.identity.EngineID)
	require.True(t, hub.status.ConsumerRunning)
	require.Equal(t, syncedAt, hub.status.CatalogSyncedAt)
	require.Equal(t, []domain.LaneStatus{{SetID: "fset_a", Queued: 1, Active: true}}, hub.status.Lanes)
	require.Equal(t, "complete", hub.status.RecentRuns[0].LastStatus)
}

func TestEngineHeartbeatConflictMapsToRetCode(t *testing.T) {
	svc := NewEngineService(&hubFake{err: fmt.Errorf("%w: factor-engine@other", enginehub.ErrLeaseConflict)})

	rsp, err := svc.EngineHeartbeat(context.Background(), &factorpb.EngineHeartbeatReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
}

func TestPullRecalcJobReturnsLeaseAndSet(t *testing.T) {
	hub := &hubFake{found: true, pulledSet: engineSetFixture(), job: store.RecalcJob{
		JobID: "job-1", SetID: "fset_a", StartTime: 60, EndTime: 600, ProgressTime: 120,
		Status: store.RecalcStatusRunning, EngineID: "factor-engine@mac", LeaseToken: "token-1",
	}}
	svc := NewEngineService(hub)

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
	svc := NewEngineService(&hubFake{})

	rsp, err := svc.PullRecalcJob(context.Background(), &factorpb.PullRecalcJobReq{Engine: testEngine})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.False(t, rsp.GetFound())
}

func TestReportRecalcProgressConvertsRFC3339(t *testing.T) {
	hub := &hubFake{job: store.RecalcJob{JobID: "job-1"}}
	svc := NewEngineService(hub)
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
	svc := NewEngineService(&hubFake{})

	rsp, err := svc.ReportRecalcProgress(context.Background(), &factorpb.ReportRecalcProgressReq{
		JobId: "job-1", LeaseToken: "token-1", ProgressTime: "yesterday",
	})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestReportRecalcProgressStaleTokenIsConflict(t *testing.T) {
	svc := NewEngineService(&hubFake{err: fmt.Errorf("%w: stale", store.ErrConflict)})

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
		status: domain.EngineStatus{ConsumerRunning: true, PythonWorkers: 8, PythonBusy: 3,
			Lanes:      []domain.LaneStatus{{SetID: "fset_a", Queued: 2}},
			RecentRuns: []domain.SetRunSummary{{SetID: "fset_a", LastPeriodTime: 1900, LastStatus: "complete", LagSeconds: 100}}},
	}))

	rsp, err := svc.GetStatus(context.Background(), &factorpb.GetStatusReq{})

	require.NoError(t, err)
	require.True(t, rsp.GetConsumerRunning())
	require.Equal(t, int32(8), rsp.GetPythonWorkers())
	require.Equal(t, int32(3), rsp.GetPythonBusy())
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
