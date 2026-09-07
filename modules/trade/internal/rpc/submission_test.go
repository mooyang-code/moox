package rpc

import (
	"context"
	"errors"
	"testing"

	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

func ordinaryRequest() *tradepb.SubmitOrderReq {
	return &tradepb.SubmitOrderReq{
		ActionId: "action-1", LogicalAccountId: "logical-1", TradingAccountId: "account-1",
		ClientOrderId: "client-1", InstrumentId: "BTCUSDT", Quantity: "1",
		OrderType: tradepb.OrderType_ORDER_TYPE_MARKET, Side: tradepb.OrderSide_ORDER_SIDE_BUY,
		Reason: "ordinary", DeadlineAt: 2000000000000,
	}
}

func TestSubmitOrderRPCPreservesDurableAcceptance(t *testing.T) {
	var got SubmitOrderCommand
	h := &ExecutionServer{
		PlaceManual: func(context.Context, ManualOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
			t.Fatal("ordinary submission called takeover")
			return store.OperatorActionRecord{}, store.OrderRecord{}, nil
		},
		SubmitOrdinary: func(_ context.Context, command SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
			got = command
			return store.OperatorActionRecord{ActionID: "action-1", Status: "RUNNING", ActionType: "SUBMIT_ORDER", LastError: "transient diagnostic"}, store.OrderRecord{OrderID: "order-1", State: "PENDING"}, nil
		},
	}
	rsp, err := h.SubmitOrder(spacecontext.WithSpaceID(context.Background(), "space-1"), ordinaryRequest())
	if err != nil || rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || rsp.GetAction().GetStatus() != "RUNNING" || rsp.GetOrder().GetState() != "PENDING" {
		t.Fatalf("response = %+v, error = %v", rsp, err)
	}
	if got.SpaceID != "space-1" || got.LogicalAccountID != "logical-1" || got.ClientOrderID != "client-1" || got.DeadlineAt != 2000000000000 || got.Quantity.String() != "1" {
		t.Fatalf("command = %+v", got)
	}
}

func TestSubmitOrderRPCValidationAndFailure(t *testing.T) {
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	for _, tc := range []struct {
		name string
		ctx  context.Context
		req  *tradepb.SubmitOrderReq
		want tradepb.ErrorCode
	}{
		{"no_space", context.Background(), ordinaryRequest(), tradepb.ErrorCode_NO_PERMISSION},
		{"nil", ctx, nil, tradepb.ErrorCode_INVALID_PARAM},
		{"missing_fields", ctx, &tradepb.SubmitOrderReq{}, tradepb.ErrorCode_INVALID_PARAM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &ExecutionServer{SubmitOrdinary: func(context.Context, SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
				t.Fatal("invalid request reached application")
				return store.OperatorActionRecord{}, store.OrderRecord{}, nil
			}}
			rsp, err := h.SubmitOrder(tc.ctx, tc.req)
			if err != nil || rsp.GetRetInfo().GetCode() != tc.want {
				t.Fatalf("response = %+v, error = %v", rsp, err)
			}
		})
	}
	for _, failure := range []error{operatorapp.ErrInvalidCommand, errors.New("database unavailable")} {
		h := &ExecutionServer{SubmitOrdinary: func(context.Context, SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
			return store.OperatorActionRecord{ActionID: "action-1", Status: "RUNNING"}, store.OrderRecord{}, failure
		}}
		rsp, err := h.SubmitOrder(ctx, ordinaryRequest())
		want := tradepb.ErrorCode_INNER_ERR
		if errors.Is(failure, operatorapp.ErrInvalidCommand) {
			want = tradepb.ErrorCode_INVALID_PARAM
		}
		if err != nil || rsp.GetRetInfo().GetCode() != want {
			t.Fatalf("response = %+v, error = %v", rsp, err)
		}
		if rsp.GetAction().GetActionId() != "action-1" || rsp.GetAction().GetStatus() != "RUNNING" {
			t.Fatalf("lost durable recovery identity: %+v", rsp)
		}
	}
}
