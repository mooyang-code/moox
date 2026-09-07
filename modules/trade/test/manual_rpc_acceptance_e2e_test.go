package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func TestManualRPCTransientAcceptanceReturnsSuccessWithDurableAction(t *testing.T) {
	for _, condition := range []string{"not_ready", "stale_quote"} {
		t.Run(condition, func(t *testing.T) {
			f, service, command := manualRecoveryFixture(t)
			if condition == "not_ready" {
				f.account.SessionState = readySession(false)
			} else {
				service.Prices = logicalAccountE2EPriceSource{at: testNow.Add(-2 * time.Minute)}
			}
			handler := &rpc.ExecutionServer{Store: f.store, PlaceManual: func(ctx context.Context, c rpc.ManualOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
				result, err := service.PlaceManualOrder(ctx, operator.ManualOrderCommand{
					SpaceID: c.SpaceID, ActionID: c.ActionID, TradingAccountID: c.TradingAccountID,
					ClientOrderID: c.ClientOrderID, InstrumentID: c.InstrumentID,
					Type: c.OrderType, FillPolicy: c.FillPolicy, Side: c.Side, PositionSide: c.PositionSide,
					Quantity: c.Quantity, LimitPrice: c.LimitPrice, Reason: c.Reason, DeadlineAt: c.DeadlineAt,
				})
				return result.Action, result.Order, err
			}}
			ctx := spacecontext.WithSpaceID(context.Background(), testSpace)
			req := &tradepb.PlaceManualOrderReq{
				ActionId: command.ActionID, TradingAccountId: command.TradingAccountID,
				ClientOrderId: command.ClientOrderID, InstrumentId: command.InstrumentID,
				OrderType: tradepb.OrderType_ORDER_TYPE_MARKET, Side: tradepb.OrderSide_ORDER_SIDE_BUY,
				PositionSide: tradepb.PositionSide_POSITION_SIDE_NET, Quantity: "0.01", Reason: command.Reason,
			}
			for attempt := 0; attempt < 2; attempt++ {
				response, err := handler.PlaceManualOrder(ctx, req)
				require.NoError(t, err)
				require.Equal(t, tradepb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode(), response.GetRetInfo())
				require.Equal(t, command.ActionID, response.GetAction().GetActionId())
				require.Equal(t, "RUNNING", response.GetAction().GetStatus())
				persisted, err := f.store.GetOperatorAction(ctx, testSpace, command.ActionID)
				require.NoError(t, err)
				require.Equal(t, persisted.LastError, response.GetAction().GetLastError())
				require.NotEmpty(t, persisted.LastError)
				require.Zero(t, f.fake.placeCalls)
			}
		})
	}
}
