package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newFrontendRPCStore(t *testing.T) (*gorm.DB, *store.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AllSQL()).Error)
	return db, store.New(db)
}

func seedFrontendBinding(t *testing.T, db *gorm.DB, r *store.Store, bindingID string) {
	t.Helper()
	ctx := context.Background()
	def := domain.StrategyDefinition{
		StrategyID: "demo", Version: "1.0.0", API: "moox.strategy/v1",
		SourceHash: "hash-1", ManifestYAML: "id: demo", SourceCode: "def run(): pass", Status: "enabled",
	}
	require.NoError(t, r.SaveDefinition(ctx, def))
	require.NoError(t, db.Create(&domain.Binding{
		BindingID: bindingID, StrategyID: def.StrategyID, StrategyVersion: def.Version,
		SpaceID: "space-a", ViewID: "view-a", Freq: "1h", ParamsJSON: "{}", GroupID: "grp-1", Status: "enabled",
	}).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_strategy_execution_bindings (c_execution_binding_id, c_group_id, c_account_id, c_mode, c_status) VALUES ('exec-1', 'grp-1', 'acct-1', 'observe', 'enabled')`).Error)
	require.NoError(t, db.Create(&domain.State{
		BindingID: bindingID, StrategyVersion: def.Version, Revision: 1, StateJSON: `{"x":1}`, LastRunID: "run-1",
	}).Error)
	now := time.Now().UTC()
	require.NoError(t, r.UpsertHealth(ctx, domain.BindingHealth{
		BindingID: bindingID, Status: "ok", Mode: "observe", LastRunID: "run-1", ObservedAt: now,
	}))
	run := domain.StrategyRun{
		RunID: "run-1", BindingID: bindingID, StrategyVersion: def.Version, TriggerBarTime: now.Format(time.RFC3339Nano),
		DataRevision: "rev-1", Status: "accepted", Action: "rebalance",
		OutputJSON: `{"action":"rebalance","targets":[{"instrument_id":"BTC","target_weight":"0.5"}]}`,
	}
	require.NoError(t, db.Create(&run).Error)
	require.NoError(t, db.Create(&domain.TargetComparison{
		RunID: "run-1", InstrumentID: "BTC", PortfolioTarget: "0.5", ActualPosition: "0.4", Deviation: "0.1",
		SourceTime: now, DataRevision: "rev-1",
	}).Error)
	require.NoError(t, r.WritePerformancePoint(ctx, domain.PerformancePoint{
		BindingID: bindingID, Source: "paper", PointTime: now, NAV: "1.0", CumulativeReturn: "0.01",
		Drawdown: "0.0", CalculatedAt: now,
	}))
}

func TestListRunningStrategies_RejectsUnavailableRepo(t *testing.T) {
	svc := &Service{}
	rsp, err := svc.ListRunningStrategies(context.Background(), &strategypb.ListRunningStrategiesReq{})
	require.NoError(t, err)
	assert.NotEqual(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestFrontendRPC_ReadsBindingOverviewRunsTargetsAndPerformance(t *testing.T) {
	db, r := newFrontendRPCStore(t)
	seedFrontendBinding(t, db, r, "b1")
	svc := &Service{Repo: r}
	ctx := context.Background()

	listRsp, err := svc.ListRunningStrategies(ctx, &strategypb.ListRunningStrategiesReq{SpaceId: "space-a"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, listRsp.GetRetInfo().GetCode())
	assert.Equal(t, int64(1), listRsp.GetTotal())

	overviewRsp, err := svc.GetStrategyOverview(ctx, &strategypb.GetStrategyOverviewReq{BindingId: "b1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, overviewRsp.GetRetInfo().GetCode())
	assert.Equal(t, "b1", overviewRsp.GetSummary().GetBindingId())

	runsRsp, err := svc.ListStrategyRuns(ctx, &strategypb.ListStrategyRunsReq{BindingId: "b1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, runsRsp.GetRetInfo().GetCode())
	assert.Len(t, runsRsp.GetItems(), 1)

	runRsp, err := svc.GetStrategyRun(ctx, &strategypb.GetStrategyRunReq{RunId: "run-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, runRsp.GetRetInfo().GetCode())
	assert.Equal(t, "run-1", runRsp.GetRun().GetRunId())

	targetsRsp, err := svc.ListStrategyTargets(ctx, &strategypb.ListStrategyTargetsReq{RunId: "run-1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, targetsRsp.GetRetInfo().GetCode())
	assert.Len(t, targetsRsp.GetTargets(), 1)

	stateRsp, err := svc.GetStrategyStateSummary(ctx, &strategypb.GetStrategyStateSummaryReq{BindingId: "b1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, stateRsp.GetRetInfo().GetCode())
	assert.Greater(t, stateRsp.GetSizeBytes(), int64(0))

	healthRsp, err := svc.GetStrategyHealth(ctx, &strategypb.GetStrategyHealthReq{BindingId: "b1"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, healthRsp.GetRetInfo().GetCode())
	assert.Equal(t, "ok", healthRsp.GetHealth().GetStatus())

	perfRsp, err := svc.GetStrategyPerformance(ctx, &strategypb.GetStrategyPerformanceReq{
		BindingId: "b1", PerformanceSource: "paper",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, perfRsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, perfRsp.GetPoints())

	dailyRsp, err := svc.GetStrategyPerformance(ctx, &strategypb.GetStrategyPerformanceReq{
		BindingId: "b1", PerformanceSource: "paper", Interval: "daily",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, dailyRsp.GetRetInfo().GetCode())
}

func TestBindingOperations_ValidateInputAndChangeStatus(t *testing.T) {
	db, r := newFrontendRPCStore(t)
	seedFrontendBinding(t, db, r, "b1")
	svc := &Service{Repo: r}
	ctx := context.Background()

	pauseRsp, err := svc.PauseBinding(ctx, &strategypb.BindingOperationReq{
		BindingId: "b1", OperationId: "op-pause", Reason: "maintenance",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, pauseRsp.GetRetInfo().GetCode())
	assert.Equal(t, "disabled", pauseRsp.GetStatus())

	resumeRsp, err := svc.ResumeBinding(ctx, &strategypb.BindingOperationReq{
		BindingId: "b1", OperationId: "op-resume", Reason: "done",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, resumeRsp.GetRetInfo().GetCode())
	assert.Equal(t, "enabled", resumeRsp.GetStatus())

	modeRsp, err := svc.SetExecutionMode(ctx, &strategypb.SetExecutionModeReq{
		BindingId: "b1", Mode: "paper", OperationId: "op-mode", Reason: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, modeRsp.GetRetInfo().GetCode())
	assert.Equal(t, "paper", modeRsp.GetStatus())
}

func TestFrontendRPC_RejectsMissingRequiredFields(t *testing.T) {
	_, r := newFrontendRPCStore(t)
	svc := &Service{Repo: r}
	ctx := context.Background()

	overviewRsp, err := svc.GetStrategyOverview(ctx, &strategypb.GetStrategyOverviewReq{})
	require.NoError(t, err)
	assert.NotEqual(t, commonpb.ErrorCode_SUCCESS, overviewRsp.GetRetInfo().GetCode())

	perfRsp, err := svc.GetStrategyPerformance(ctx, &strategypb.GetStrategyPerformanceReq{BindingId: "b1"})
	require.NoError(t, err)
	assert.NotEqual(t, commonpb.ErrorCode_SUCCESS, perfRsp.GetRetInfo().GetCode())
}

func TestPageFromProtoAndOperatorAllowed(t *testing.T) {
	page := pageFromProto(nil)
	assert.Equal(t, 1, page.Number)
	assert.Equal(t, 20, page.Size)

	page = pageFromProto(&strategypb.PageReq{Page: 2, PageSize: 5})
	assert.Equal(t, 2, page.Number)
	assert.Equal(t, 5, page.Size)

	page = pageFromProto(&strategypb.PageReq{Page: -1, PageSize: 500})
	assert.Equal(t, 1, page.Number)
	assert.Equal(t, 200, page.Size)

	assert.True(t, operatorAllowed(context.Background()))
}

func TestParseStrictTimeRangeRejectsMalformedAndNonIncreasingBounds(t *testing.T) {
	tests := []struct {
		name     string
		rangeReq *strategypb.TimeRange
		wantErr  string
	}{
		{name: "bad from", rangeReq: &strategypb.TimeRange{From: "bad"}, wantErr: "from"},
		{name: "bad to", rangeReq: &strategypb.TimeRange{To: "bad"}, wantErr: "to"},
		{name: "equal", rangeReq: &strategypb.TimeRange{From: "2026-07-17T00:00:00Z", To: "2026-07-17T00:00:00Z"}, wantErr: "before"},
		{name: "reversed", rangeReq: &strategypb.TimeRange{From: "2026-07-18T00:00:00Z", To: "2026-07-17T00:00:00Z"}, wantErr: "before"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseStrictTimeRange(tt.rangeReq)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	from, to, err := parseStrictTimeRange(&strategypb.TimeRange{
		From: "2026-07-17T08:00:00+08:00", To: "2026-07-17T09:00:00+08:00",
	})
	require.NoError(t, err)
	assert.Equal(t, time.UTC, from.Location())
	assert.Equal(t, time.UTC, to.Location())
	from, to, err = parseStrictTimeRange(nil)
	require.NoError(t, err)
	assert.True(t, from.IsZero())
	assert.True(t, to.IsZero())
}

func TestFrontendRPCRejectsInvalidQueryBeforeRepositoryAccess(t *testing.T) {
	svc := &Service{}

	runs, err := svc.ListStrategyRuns(context.Background(), &strategypb.ListStrategyRunsReq{
		BindingId: "b1", Range: &strategypb.TimeRange{From: "not-a-time"},
	})
	require.NoError(t, err)
	assert.Contains(t, runs.GetRetInfo().GetMsg(), "range.from")

	performance, err := svc.GetStrategyPerformance(context.Background(), &strategypb.GetStrategyPerformanceReq{
		BindingId: "b1", PerformanceSource: "invalid", Interval: "hourly",
	})
	require.NoError(t, err)
	assert.Contains(t, performance.GetRetInfo().GetMsg(), "performance_source")
}

func TestGetStrategyPerformanceRejectsUnsupportedInterval(t *testing.T) {
	svc := &Service{}
	rsp, err := svc.GetStrategyPerformance(context.Background(), &strategypb.GetStrategyPerformanceReq{
		BindingId: "b1", PerformanceSource: "paper", Interval: "hourly",
	})
	require.NoError(t, err)
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "interval")
}

func TestSetExecutionModeRejectsLiveWhenCapabilityDisabled(t *testing.T) {
	db, repo := newFrontendRPCStore(t)
	seedFrontendBinding(t, db, repo, "b1")
	svc := &Service{Repo: repo, LiveExecutionEnabled: false}

	rsp, err := svc.SetExecutionMode(context.Background(), &strategypb.SetExecutionModeReq{
		BindingId: "b1", Mode: "live", OperationId: "op-live", Reason: "enable live",
	})
	require.NoError(t, err)
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "live execution is disabled")

	var mode string
	require.NoError(t, db.Raw(`SELECT c_mode FROM t_strategy_execution_bindings WHERE c_execution_binding_id = 'exec-1'`).Scan(&mode).Error)
	assert.Equal(t, "observe", mode)
}

func TestDisabledLiveCapabilityKeepsObservePaperMutationsAndLiveHistoryAvailable(t *testing.T) {
	db, repo := newFrontendRPCStore(t)
	seedFrontendBinding(t, db, repo, "b1")
	svc := &Service{Repo: repo, LiveExecutionEnabled: false}

	for i, mode := range []string{"paper", "observe"} {
		rsp, err := svc.SetExecutionMode(context.Background(), &strategypb.SetExecutionModeReq{
			BindingId: "b1", Mode: mode, OperationId: fmt.Sprintf("op-mode-%d", i), Reason: "capability boundary test",
		})
		require.NoError(t, err)
		assert.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		assert.Equal(t, mode, rsp.GetStatus())
	}

	history, err := svc.GetStrategyPerformance(context.Background(), &strategypb.GetStrategyPerformanceReq{
		BindingId: "b1", PerformanceSource: "live", Interval: "auto",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, history.GetRetInfo().GetCode())
	assert.Equal(t, "live", history.GetPerformanceSource())
}

func TestPerformanceSourceValidationAcceptsEveryDomainSource(t *testing.T) {
	for _, source := range []string{"backtest", "observe", "paper", "live"} {
		t.Run(source, func(t *testing.T) {
			rsp, err := (&Service{}).GetStrategyPerformance(context.Background(), &strategypb.GetStrategyPerformanceReq{
				BindingId: "b1", PerformanceSource: source, Interval: "auto",
			})
			require.NoError(t, err)
			assert.Contains(t, rsp.GetRetInfo().GetMsg(), "repository")
		})
	}
}

func TestGetEngineStatusExposesLiveCapability(t *testing.T) {
	svc := &Service{LiveExecutionEnabled: true}
	rsp, err := svc.GetEngineStatus(context.Background(), &strategypb.GetEngineStatusReq{})
	require.NoError(t, err)
	assert.True(t, rsp.GetLiveExecutionEnabled())
}
