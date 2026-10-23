package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

type paramFake struct {
	frozenSets []string
	period     time.Time
	offline    []domain.ParamFreeze
	conflicts  []domain.ParamFreeze
	intervals  []domain.ParamInterval
}

func (f *paramFake) FreezeParams(_ context.Context, setIDs []string, period time.Time) (map[string][]domain.ParamFreeze, error) {
	f.frozenSets, f.period = setIDs, period
	snapshot := domain.ParamSnapshot{"window": {Default: 30}}
	return map[string][]domain.ParamFreeze{setIDs[0]: {{SetID: setIDs[0], MemberAlias: "fast", PeriodTime: period,
		Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: domain.ParamsRequested, RequestID: "r1"}}}, nil
}

func (f *paramFake) ApplyOfflineFreezes(_ context.Context, freezes []domain.ParamFreeze) ([]domain.ParamFreeze, error) {
	f.offline = freezes
	return f.conflicts, nil
}

func (f *paramFake) ListParamIntervals(context.Context, string, string, time.Time, time.Time) ([]domain.ParamInterval, error) {
	return f.intervals, nil
}

// 冻结、离线补报、区间查询都只允许持有引擎租约的进程调用。
func TestEngineParamCallsRequireLease(t *testing.T) {
	svc := NewEngineService(&hubFake{noLease: true}, &paramFake{}, nil)
	freeze, err := svc.FreezeFactorParams(context.Background(), &factorpb.FreezeFactorParamsReq{Engine: testEngine, SetIds: []string{"s"}, PeriodTime: 60})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, freeze.GetRetInfo().GetCode())
	report, err := svc.ReportOfflineFreezes(context.Background(), &factorpb.ReportOfflineFreezesReq{Engine: testEngine})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, report.GetRetInfo().GetCode())
	list, err := svc.ListParamIntervals(context.Background(), &factorpb.ListParamIntervalsReq{Engine: testEngine, SetId: "s", From: 0, To: 60})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, list.GetRetInfo().GetCode())
}

func TestFreezeFactorParamsReturnsPerSetFreezes(t *testing.T) {
	params := &paramFake{}
	svc := NewEngineService(&hubFake{}, params, nil)
	rsp, err := svc.FreezeFactorParams(context.Background(), &factorpb.FreezeFactorParamsReq{Engine: testEngine, SetIds: []string{"s"}, PeriodTime: 120})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"s"}, params.frozenSets)
	require.Equal(t, time.Unix(120, 0).UTC(), params.period)
	require.Len(t, rsp.GetSets(), 1)
	member := rsp.GetSets()[0].GetMembers()[0]
	require.Equal(t, "fast", member.GetMemberAlias())
	require.Equal(t, domain.ParamsRequested, member.GetSource())
	require.Equal(t, 30.0, member.GetParams()["window"].GetDefaultValue())

	missing, err := svc.FreezeFactorParams(context.Background(), &factorpb.FreezeFactorParamsReq{Engine: testEngine})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())
}

// 离线补报统一按 carried_offline 记录，并把管理端已有不同记录的期次原样返回。
func TestReportOfflineFreezesMarksSourceAndReturnsConflicts(t *testing.T) {
	conflict := domain.ParamFreeze{SetID: "s", MemberAlias: "fast", PeriodTime: time.Unix(60, 0).UTC(),
		Snapshot: domain.ParamSnapshot{"window": {Default: 40}}, Source: domain.ParamsRequested}
	params := &paramFake{conflicts: []domain.ParamFreeze{conflict}}
	svc := NewEngineService(&hubFake{}, params, nil)
	rsp, err := svc.ReportOfflineFreezes(context.Background(), &factorpb.ReportOfflineFreezesReq{Engine: testEngine, Freezes: []*factorpb.OfflineFreeze{{
		SetId: "s", MemberAlias: "fast", PeriodTime: 60, Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: 20}},
	}}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, params.offline, 1)
	require.Equal(t, domain.ParamsCarriedOffline, params.offline[0].Source)
	require.Equal(t, params.offline[0].Snapshot.Hash(), params.offline[0].SnapshotHash)
	require.Len(t, rsp.GetConflicts(), 1)
	require.Equal(t, 40.0, rsp.GetConflicts()[0].GetParams()["window"].GetDefaultValue())
}
