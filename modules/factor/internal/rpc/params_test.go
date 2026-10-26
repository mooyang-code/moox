package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// 参数只由参数写入方（内部服务）提交：带空间元数据的控制台调用被拒绝。
func TestSubmitFactorParamsRejectsSpaceScopedCaller(t *testing.T) {
	fake := &catalogFake{}
	svc := NewService(fake, &recalcFake{})
	rsp, err := svc.SubmitFactorParams(spaceCtx("crypto"), &factorpb.SubmitFactorParamsReq{
		Writer: "inst", TaskId: "s", RequestId: "r", PeriodTime: 60,
		Members: []*factorpb.MemberParams{{MemberAlias: "fast", Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: 20}}}},
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Nil(t, fake.submissions)
}

func TestSubmitFactorParamsPassesEveryMember(t *testing.T) {
	fake := &catalogFake{}
	svc := NewService(fake, &recalcFake{})
	rsp, err := svc.SubmitFactorParams(context.Background(), &factorpb.SubmitFactorParamsReq{
		Writer: "inst", TaskId: "s", RequestId: "r", PeriodTime: 120, BasedOnPeriod: 60,
		Members: []*factorpb.MemberParams{
			{MemberAlias: "fast", Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: 10, Overrides: map[string]float64{"BTC": 12}}}},
			{MemberAlias: "slow", Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: 60}}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "inst", fake.writer)
	require.Len(t, fake.submissions, 2)
	first := fake.submissions[0]
	require.Equal(t, catalog.ParamSubmission{RequestID: "r", TaskID: "s", MemberAlias: "fast", PeriodTime: time.Unix(120, 0).UTC(),
		BasedOnPeriod: time.Unix(60, 0).UTC(), Snapshot: domain.ParamSnapshot{"window": {Default: 10, Overrides: map[string]float64{"BTC": 12}}}}, first)
	require.Len(t, rsp.GetResults(), 2)
	require.Equal(t, catalog.SubmitAccepted, rsp.GetResults()[1].GetResult())

	missing, err := svc.SubmitFactorParams(context.Background(), &factorpb.SubmitFactorParamsReq{Writer: "inst", TaskId: "s"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())
}

// 实例因子任务由实例所在空间建立：带空间元数据时必须与请求的空间一致。
func TestEnsureInstanceFactorTaskChecksCallerSpace(t *testing.T) {
	fake := &catalogFake{}
	svc := NewService(fake, &recalcFake{})
	req := &factorpb.EnsureInstanceFactorTaskReq{InstanceId: "inst", SpaceId: "crypto", SourceDatasetId: "dataset_bars", Freq: "1m",
		Members: []*factorpb.InstanceTaskMember{{MemberAlias: "fast", FactorId: "bias"}}}
	other, err := svc.EnsureInstanceFactorTask(spaceCtx("stock"), req)
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, other.GetRetInfo().GetCode())

	rsp, err := svc.EnsureInstanceFactorTask(spaceCtx("crypto"), req)
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []catalog.InstanceMember{{Alias: "fast", FactorID: "bias"}}, fake.instanceRequest.Members)
	require.Equal(t, domain.InstanceTaskID("inst"), rsp.GetFactorTask().GetTaskId())
	require.Equal(t, domain.ResultViewID(domain.InstanceTaskID("inst")), rsp.GetViewId())
}

func TestGetFactorParamsReturnsIntervalsAndFrozenThrough(t *testing.T) {
	snapshot := domain.ParamSnapshot{"window": {Default: 30}}
	fake := &catalogFake{set: domain.FactorTask{TaskID: "s", SpaceID: "crypto"}, frozenThrough: time.Unix(180, 0).UTC(),
		intervals: []domain.ParamInterval{{MemberAlias: "fast", Start: time.Unix(60, 0).UTC(), End: time.Unix(180, 0).UTC(),
			Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: domain.ParamsRequested}}}
	svc := NewService(fake, &recalcFake{})
	rsp, err := svc.GetFactorParams(spaceCtx("crypto"), &factorpb.GetFactorParamsReq{TaskId: "s", From: 0, To: 600})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, int64(180), rsp.GetFrozenThrough())
	require.Len(t, rsp.GetIntervals(), 1)
	require.Equal(t, int64(60), rsp.GetIntervals()[0].GetStartPeriod())
	require.Equal(t, 30.0, rsp.GetIntervals()[0].GetParams()["window"].GetDefaultValue())
}

// 策略实例的私有因子任务不能手工维护成员与状态，也不能手工创建；共享因子任务不受影响。
func TestOwnedTasksRejectManualMaintenance(t *testing.T) {
	fake := &catalogFake{set: domain.FactorTask{TaskID: "ftask_i1", SpaceID: "crypto", OwnerInstanceID: "inst-1"}}
	svc := NewService(fake, &recalcFake{})
	ctx := context.Background()
	add, _ := svc.AddTaskMember(ctx, &factorpb.AddTaskMemberReq{TaskId: "ftask_i1", FactorId: "bias"})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, add.GetRetInfo().GetCode())
	require.Contains(t, add.GetRetInfo().GetMsg(), "私有因子任务")
	remove, _ := svc.RemoveTaskMember(ctx, &factorpb.RemoveTaskMemberReq{TaskId: "ftask_i1", MemberAlias: "bias"})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, remove.GetRetInfo().GetCode())
	status, _ := svc.SetTaskMemberStatus(ctx, &factorpb.SetTaskMemberStatusReq{TaskId: "ftask_i1", MemberAlias: "bias", Status: domain.MemberStatusEnabled})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, status.GetRetInfo().GetCode())
	setStatus, _ := svc.SetFactorTaskStatus(ctx, &factorpb.SetFactorTaskStatusReq{TaskId: "ftask_i1", Status: domain.TaskStatusDisabled})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, setStatus.GetRetInfo().GetCode())
	create, _ := svc.CreateFactorTask(ctx, &factorpb.CreateFactorTaskReq{FactorTask: &factorpb.FactorTask{TaskId: "ftask_x", SpaceId: "crypto", SourceDatasetId: "ds", Freq: "1m", OwnerInstanceId: "inst-2"}})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, create.GetRetInfo().GetCode())

	shared := NewService(&catalogFake{set: domain.FactorTask{TaskID: "ftask_shared", SpaceID: "crypto"}}, &recalcFake{})
	ok, _ := shared.AddTaskMember(ctx, &factorpb.AddTaskMemberReq{TaskId: "ftask_shared", FactorId: "bias"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, ok.GetRetInfo().GetCode())
}
