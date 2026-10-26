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
	taskID  string
	members []domain.FactorTaskMember
}

func (f *paramFake) GetParams(_ context.Context, taskID string) ([]domain.FactorTaskMember, error) {
	f.taskID = taskID
	return f.members, nil
}

// 引擎读提交值只允许持有引擎租约的进程调用。
func TestGetTaskParamsRequiresLease(t *testing.T) {
	svc := NewEngineService(&hubFake{noLease: true}, &paramFake{}, nil)
	rsp, err := svc.GetTaskParams(context.Background(), &factorpb.GetTaskParamsReq{Engine: testEngine, TaskId: "s"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
}

func TestGetTaskParamsReturnsLastSubmitted(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	params := &paramFake{members: []domain.FactorTaskMember{{
		TaskID: "s", MemberKey: "fast__var", LastSubmittedPeriod: 120, LastRequestID: "r1", LastSubmittedAt: &at,
		LastSubmittedParams: domain.ParamSnapshot{"window": {Default: 30}},
	}}}
	svc := NewEngineService(&hubFake{}, params, nil)
	rsp, err := svc.GetTaskParams(context.Background(), &factorpb.GetTaskParamsReq{Engine: testEngine, TaskId: "s"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "s", params.taskID)
	require.Len(t, rsp.GetMembers(), 1)
	member := rsp.GetMembers()[0]
	require.Equal(t, "fast__var", member.GetMemberKey())
	require.Equal(t, int64(120), member.GetLastSubmittedPeriod())
	require.Equal(t, 30.0, member.GetParams()["window"].GetDefaultValue())

	missing, err := svc.GetTaskParams(context.Background(), &factorpb.GetTaskParamsReq{Engine: testEngine})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())
}
