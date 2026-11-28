package rpc

import (
	"context"
	"testing"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

func TestLogicalAccountRPCRejectsMissingSpace(t *testing.T) {
	response, err := (&LogicalAccountServer{}).GetLogicalAccount(
		context.Background(),
		&tradepb.GetLogicalAccountReq{LogicalAccountId: "logical-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_NO_PERMISSION {
		t.Fatalf("ret_info = %+v", response.GetRetInfo())
	}
}

func TestLogicalAccountRPCCreatesPausedAccount(t *testing.T) {
	tradeStore := openRPCStore(t)
	service := &logicalapp.Service{Store: tradeStore}
	handler := &LogicalAccountServer{
		LogicalAccounts: service,
		Store:           tradeStore,
		NewID:           func() string { return "logical-1" },
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	created, err := handler.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name: "main", Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER,
		SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		created.GetLogicalAccount().GetLogicalAccountId() != "logical-1" ||
		created.GetLogicalAccount().GetAutomationState() != "PAUSED" {
		t.Fatalf("created = %+v", created)
	}
}

// CloseTargetSession：请求要带组合、实例、会话和正数的开始时刻；成功时返回关闭之后的 c_closed_through，重复调用结果不变。
func TestCloseTargetSessionRPCValidatesAndReturnsClosedThrough(t *testing.T) {
	tradeStore := openRPCStore(t)
	woke := 0
	handler := &LogicalAccountServer{
		LogicalAccounts: &logicalapp.Service{Store: tradeStore, WakeTarget: func(string, string) { woke++ }},
		Store:           tradeStore,
		NewID:           func() string { return "logical-1" },
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	created, err := handler.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name: "main", Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER, SettlementAsset: "USDT",
	})
	if err != nil || created.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
		t.Fatalf("created = %+v, err = %v", created, err)
	}
	valid := &tradepb.CloseTargetSessionReq{LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionId: "session-1", SessionStartedAt: 1_000}
	for name, request := range map[string]*tradepb.CloseTargetSessionReq{
		"缺组合":    {InstanceId: "instance-1", SessionId: "session-1", SessionStartedAt: 1_000},
		"缺实例":    {LogicalAccountId: "logical-1", SessionId: "session-1", SessionStartedAt: 1_000},
		"缺会话":    {LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionStartedAt: 1_000},
		"开始时刻为零": {LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionId: "session-1"},
	} {
		response, err := handler.CloseTargetSession(ctx, request)
		if err != nil || response.GetRetInfo().GetCode() != tradepb.ErrorCode_INVALID_PARAM {
			t.Fatalf("%s: response = %+v, err = %v", name, response, err)
		}
	}
	if missing, err := (&LogicalAccountServer{}).CloseTargetSession(context.Background(), valid); err != nil || missing.GetRetInfo().GetCode() != tradepb.ErrorCode_NO_PERMISSION {
		t.Fatalf("没有空间：response = %+v, err = %v", missing, err)
	}
	for range 2 {
		response, err := handler.CloseTargetSession(ctx, valid)
		if err != nil || response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || response.GetClosedThrough() != 1_000 {
			t.Fatalf("response = %+v, err = %v", response, err)
		}
	}
	if woke != 2 {
		t.Fatalf("woke = %d, want 2", woke)
	}
	// 组合不存在（例如 Trade 重置过）回 NOT_FOUND：Strategy 据此视为已关闭，重复停用、删除实例不会一直卡住。
	unknown := &tradepb.CloseTargetSessionReq{LogicalAccountId: "logical-gone", InstanceId: "instance-1", SessionId: "session-1", SessionStartedAt: 1_000}
	if response, err := handler.CloseTargetSession(ctx, unknown); err != nil || response.GetRetInfo().GetCode() != tradepb.ErrorCode_NOT_FOUND {
		t.Fatalf("组合不存在：response = %+v, err = %v", response, err)
	}
}

func TestFlattenLogicalAccountDelegatesTrustedSpace(t *testing.T) {
	var got []string
	handler := &LogicalAccountServer{
		Flatten: func(
			_ context.Context,
			spaceID string,
			actionID string,
			logicalAccountID string,
			reason string,
		) (store.OperatorActionRecord, error) {
			got = []string{spaceID, actionID, logicalAccountID, reason}
			return store.OperatorActionRecord{
				ActionID: actionID, LogicalAccountID: logicalAccountID,
				ActionType: "FLATTEN", Status: "RUNNING",
			}, nil
		},
	}
	response, err := handler.FlattenLogicalAccount(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.FlattenLogicalAccountReq{
			ActionId: "action-1", LogicalAccountId: "logical-1",
			Reason: "close positions",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		response.GetAction().GetActionId() != "action-1" ||
		len(got) != 4 || got[0] != "space-1" {
		t.Fatalf("response = %+v, got = %v", response, got)
	}
}

func TestFlattenLogicalAccountMapsInvalidRequest(t *testing.T) {
	response, err := (&LogicalAccountServer{}).FlattenLogicalAccount(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.FlattenLogicalAccountReq{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret_info = %+v", response.GetRetInfo())
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
