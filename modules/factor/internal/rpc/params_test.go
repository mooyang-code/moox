package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
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
		Members: []*factorpb.MemberParams{{MemberKey: "fast__var", Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: 20}}}},
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Nil(t, fake.submissions)
}

func TestSubmitFactorParamsPassesEveryMember(t *testing.T) {
	fake := &catalogFake{}
	svc := NewService(fake, &recalcFake{})
	rsp, err := svc.SubmitFactorParams(context.Background(), &factorpb.SubmitFactorParamsReq{
		Writer: "inst", TaskId: "s", RequestId: "r", PeriodTime: 120,
		Members: []*factorpb.MemberParams{
			{MemberKey: "fast__var", Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: 10, Overrides: map[string]float64{"BTC": 12}}}},
			{MemberKey: "slow__var", Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: 60}}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "inst", fake.writer)
	require.Equal(t, "s", fake.submitTask)
	require.Equal(t, "r", fake.submitRequest)
	require.Equal(t, time.Unix(120, 0).UTC(), fake.submitPeriod)
	require.Len(t, fake.submissions, 2)
	require.Equal(t, catalog.MemberSubmission{MemberKey: "fast__var", Snapshot: domain.ParamSnapshot{"window": {Default: 10, Overrides: map[string]float64{"BTC": 12}}}}, fake.submissions[0])
	require.Len(t, rsp.GetResults(), 2)
	require.Equal(t, store.SubmitAccepted, rsp.GetResults()[1].GetResult())
	require.Equal(t, "slow__var", rsp.GetResults()[1].GetMemberKey())

	missing, err := svc.SubmitFactorParams(context.Background(), &factorpb.SubmitFactorParamsReq{Writer: "inst", TaskId: "s"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())
}

func TestGetFactorParamsReturnsLastSubmittedPerVarMember(t *testing.T) {
	submittedAt := time.Unix(200, 0).UTC()
	fake := &catalogFake{set: domain.FactorTask{TaskID: "s", SpaceID: "crypto"}, paramStates: []domain.FactorTaskMember{{
		TaskID: "s", MemberKey: "fast__var", Kind: domain.MemberKindVar, LastSubmittedPeriod: 180, LastRequestID: "r-3",
		LastSubmittedAt: &submittedAt, LastSubmittedParams: domain.ParamSnapshot{"window": {Default: 30, Overrides: map[string]float64{"BTC": 20}}},
	}}}
	svc := NewService(fake, &recalcFake{})
	rsp, err := svc.GetFactorParams(spaceCtx("crypto"), &factorpb.GetFactorParamsReq{TaskId: "s"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetMembers(), 1)
	member := rsp.GetMembers()[0]
	require.Equal(t, "fast__var", member.GetMemberKey())
	require.Equal(t, int64(180), member.GetLastSubmittedPeriod())
	require.Equal(t, "r-3", member.GetLastRequestId())
	require.Equal(t, 30.0, member.GetParams()["window"].GetDefaultValue())
	require.Equal(t, 20.0, member.GetParams()["window"].GetOverrides()["BTC"])
	require.NotEmpty(t, member.GetLastSubmittedAt())

	other, err := svc.GetFactorParams(spaceCtx("stock"), &factorpb.GetFactorParamsReq{TaskId: "s"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, other.GetRetInfo().GetCode(), "其他空间读不到")
}

func TestBindFactorTask(t *testing.T) {
	fake := &catalogFake{set: domain.FactorTask{TaskID: "s", SpaceID: "crypto"}}
	svc := NewService(fake, &recalcFake{})
	rsp, err := svc.BindFactorTask(context.Background(), &factorpb.BindFactorTaskReq{TaskId: "s", InstanceId: "inst-1"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "inst-1", rsp.GetFactorTask().GetBoundInstanceId())
	require.Equal(t, "s", fake.boundTask)
	missing, err := svc.BindFactorTask(context.Background(), &factorpb.BindFactorTaskReq{TaskId: "s"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())
}

// 加成员时类型原样传给目录服务，响应带成员键和类型。
func TestAddTaskMemberPassesKind(t *testing.T) {
	fake := &catalogFake{set: domain.FactorTask{TaskID: "s", SpaceID: "crypto"}}
	svc := NewService(fake, &recalcFake{})
	rsp, err := svc.AddTaskMember(context.Background(), &factorpb.AddTaskMemberReq{TaskId: "s", FactorId: "ma", Kind: domain.MemberKindVar})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, domain.MemberKindVar, fake.addKind)
	require.Equal(t, "ma__var", rsp.GetMember().GetMemberKey())
	require.Equal(t, domain.MemberKindVar, rsp.GetMember().GetKind())
}
