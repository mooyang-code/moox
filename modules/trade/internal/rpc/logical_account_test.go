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

func TestLogicalAccountRPCCreatesPausedAccountAndClaimsOwner(t *testing.T) {
	tradeStore := openRPCStore(t)
	service := &logicalapp.Service{Store: tradeStore}
	handler := &LogicalAccountServer{
		LogicalAccounts: service,
		Store:           tradeStore,
		NewID:           func() string { return "logical-1" },
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	created, err := handler.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name: "main", ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER,
		MarketType:      tradepb.MarketType_MARKET_TYPE_SPOT,
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

	claimed, err := handler.ClaimLogicalAccountOwner(
		ctx,
		&tradepb.ClaimLogicalAccountOwnerReq{
			LogicalAccountId: "logical-1",
			RunnerId:         "runner-1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		claimed.GetLogicalAccount().GetOwnerRunnerId() != "runner-1" {
		t.Fatalf("claimed = %+v", claimed)
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
