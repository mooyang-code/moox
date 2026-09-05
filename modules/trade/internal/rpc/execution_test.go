package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

type manualReplayDependencies struct{}

func (manualReplayDependencies) SyncAccount(context.Context, string) error { return nil }
func (manualReplayDependencies) LatestPrice(context.Context, string, string) (operator.Quote, error) {
	return operator.Quote{}, nil
}

func TestManualPersistedFailureMapsToInvalidParam(t *testing.T) {
	for _, code := range []string{"INSUFFICIENT_FUNDS", "QUANTITY_RULE", "INVALID_SPEC", "INVALID_COMMAND", "CROSS_ZERO", "UNKNOWN_CODE"} {
		t.Run(code, func(t *testing.T) {
			db := openRPCStore(t)
			ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
			if err := db.Transaction(ctx, func(tx *store.Tx) error {
				return tx.CreateLogicalAccount(store.LogicalAccountRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "manual", OwnerRunnerID: "runner-1", ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test"})
			}); err != nil {
				t.Fatal(err)
			}
			raw := `{"error_code":"` + code + `"}`
			_, _, err := db.CreateOperatorAction(ctx, store.OperatorActionRecord{SpaceID: "space-1", ActionID: "action-1", LogicalAccountID: "logical-1", ActionType: "MANUAL_ORDER", Status: "FAILED", Reason: "manual intervention", LastError: "retained failure detail", ResultJSON: &raw,
				RequestJSON: `{"trading_account_id":"account-1","client_order_id":"client-1","instrument_id":"BTCUSDT","order_type":"MARKET","side":"BUY","position_side":"NET","quantity":"1"}`})
			if err != nil {
				t.Fatal(err)
			}
			service := &operator.Service{Store: db, Orders: &orderapp.Service{Store: db}, Syncer: manualReplayDependencies{}, Prices: manualReplayDependencies{}}
			h := &ExecutionServer{PlaceManual: func(ctx context.Context, c ManualOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
				result, err := service.PlaceManualOrder(ctx, operator.ManualOrderCommand{SpaceID: c.SpaceID, ActionID: c.ActionID, TradingAccountID: c.TradingAccountID, ClientOrderID: c.ClientOrderID, InstrumentID: c.InstrumentID, Type: c.OrderType, FillPolicy: c.FillPolicy, Side: c.Side, PositionSide: c.PositionSide, Quantity: c.Quantity, LimitPrice: c.LimitPrice, Reason: c.Reason})
				return result.Action, result.Order, err
			}}
			for attempt := 0; attempt < 2; attempt++ {
				rsp, err := h.PlaceManualOrder(ctx, &tradepb.PlaceManualOrderReq{ActionId: "action-1", TradingAccountId: "account-1", ClientOrderId: "client-1", InstrumentId: "BTCUSDT", OrderType: tradepb.OrderType_ORDER_TYPE_MARKET, Side: tradepb.OrderSide_ORDER_SIDE_BUY, PositionSide: tradepb.PositionSide_POSITION_SIDE_NET, Quantity: "1", Reason: "manual intervention"})
				if err != nil {
					t.Fatal(err)
				}
				want := tradepb.ErrorCode_INVALID_PARAM
				if code == "UNKNOWN_CODE" {
					want = tradepb.ErrorCode_INNER_ERR
				}
				if rsp.GetRetInfo().GetCode() != want {
					t.Fatalf("response = %+v", rsp)
				}
			}
		})
	}
}

func TestCorruptManualResultTakesPrecedenceOverInvalidRecord(t *testing.T) {
	err := errors.Join(operator.ErrInvalidActionResult, store.ErrInvalidRecord)
	if got := errorInfo(err).GetCode(); got != tradepb.ErrorCode_INNER_ERR {
		t.Fatalf("code = %v", got)
	}
}

func TestManualRunningDiagnosticDoesNotHideStorageFailure(t *testing.T) {
	for _, failure := range []error{nil, errors.New("database unavailable")} {
		h := &ExecutionServer{PlaceManual: func(context.Context, ManualOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
			return store.OperatorActionRecord{ActionID: "action-1", Status: "RUNNING", LastError: "submission outcome unknown"}, store.OrderRecord{}, failure
		}}
		rsp, err := h.PlaceManualOrder(spacecontext.WithSpaceID(context.Background(), "space-1"), &tradepb.PlaceManualOrderReq{
			ActionId: "action-1", TradingAccountId: "account-1", ClientOrderId: "client-1", InstrumentId: "BTCUSDT",
			OrderType: tradepb.OrderType_ORDER_TYPE_MARKET, Side: tradepb.OrderSide_ORDER_SIDE_BUY,
			PositionSide: tradepb.PositionSide_POSITION_SIDE_NET, Quantity: "1", Reason: "manual intervention",
		})
		if err != nil {
			t.Fatal(err)
		}
		if (rsp.GetRetInfo().GetCode() == tradepb.ErrorCode_SUCCESS) != (failure == nil) {
			t.Fatalf("response = %+v, failure = %v", rsp, failure)
		}
		if rsp.GetAction().GetLastError() != "submission outcome unknown" {
			t.Fatalf("lost diagnostic: %+v", rsp)
		}
	}
}

func TestExecutionRPCRejectsMissingSpace(t *testing.T) {
	response, err := (&ExecutionServer{}).GetOrder(
		context.Background(),
		&tradepb.GetOrderReq{OrderId: "order-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_NO_PERMISSION {
		t.Fatalf("ret_info = %+v", response.GetRetInfo())
	}
}

func TestOrderConversionPreservesOwnershipAndExecutionFields(t *testing.T) {
	record := store.OrderRecord{
		OrderID: "order-1", TradingAccountID: "account-1",
		Exchange: "OKX", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP",
		OrderType: "MARKET", TimeInForce: "", Side: "SELL",
		PositionSide: "NET", Quantity: "2", ReferencePrice: "60000",
		ReferencePriceAt: 1000, ReduceOnly: true,
		OwnerType: "TARGET", OwnerID: "target-1",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
		State: "OPEN", FilledQuantity: "0", AveragePrice: "0",
		ReservedQuantity: "0", RemainingReservedQuantity: "0",
	}
	converted := orderToPB(record)
	if converted.GetExchange() != tradepb.Exchange_EXCHANGE_OKX ||
		converted.GetMarketType() != tradepb.MarketType_MARKET_TYPE_SWAP ||
		converted.GetOrderType() != tradepb.OrderType_ORDER_TYPE_MARKET ||
		converted.GetPositionSide() != tradepb.PositionSide_POSITION_SIDE_NET ||
		!converted.GetReducePositionOnly() ||
		converted.GetOwnerType() != "TARGET" ||
		converted.GetOwnerId() != "target-1" ||
		converted.GetLogicalAccountId() != "logical-1" ||
		converted.GetRunnerId() != "runner-1" ||
		converted.LimitPrice != nil {
		t.Fatalf("converted order = %+v", converted)
	}
}

func TestPlaceManualOrderUsesServerOwnedApplicationPath(t *testing.T) {
	var got ManualOrderCommand
	handler := &ExecutionServer{
		PlaceManual: func(
			_ context.Context,
			command ManualOrderCommand,
		) (store.OperatorActionRecord, store.OrderRecord, error) {
			got = command
			return store.OperatorActionRecord{
					ActionID: "action-1", LogicalAccountID: "logical-1",
					ActionType: "MANUAL_ORDER", Status: "COMPLETED",
				}, store.OrderRecord{
					OrderID: "order-1", OwnerType: "OPERATOR",
					OwnerID: "action-1", LogicalAccountID: "logical-1",
				}, nil
		},
	}
	response, err := handler.PlaceManualOrder(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.PlaceManualOrderReq{
			ActionId: "action-1", TradingAccountId: "account-1",
			ClientOrderId: "client-1", InstrumentId: "BTCUSDT",
			OrderType:    tradepb.OrderType_ORDER_TYPE_MARKET,
			Side:         tradepb.OrderSide_ORDER_SIDE_BUY,
			PositionSide: tradepb.PositionSide_POSITION_SIDE_NET,
			Quantity:     "1", Reason: "manual intervention",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		response.GetAction().GetActionId() != "action-1" ||
		response.GetOrder().GetOwnerType() != "OPERATOR" ||
		got.SpaceID != "space-1" ||
		got.ActionID != "action-1" ||
		got.Quantity.String() != "1" {
		t.Fatalf("response = %+v, command = %+v", response, got)
	}
}

func TestListOrdersCanScopeByLogicalAccount(t *testing.T) {
	tradeStore := openRPCStore(t)
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, accountID := range []string{"account-1", "account-2"} {
			if err := tx.CreateTradingAccount(store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: accountID, Name: accountID,
				Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
				Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
			}); err != nil {
				return err
			}
		}
		for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: symbol, ExchangeSymbol: symbol,
				BaseAsset: symbol[:3], QuoteAsset: "USDT",
				SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
				PriceTick: "0.01", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
			OwnerRunnerID: "runner-1",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "test",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-1", Enabled: true,
		}); err != nil {
			return err
		}
		for _, record := range []store.OrderRecord{
			{
				SpaceID: "space-1", OrderID: "order-1",
				TradingAccountID: "account-1", ClientOrderID: "client-1",
				Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "BTCUSDT", ExchangeSymbol: "BTCUSDT",
				OrderType: "MARKET", Side: "BUY", Quantity: "1",
				ReferencePrice: "100", OwnerType: "TARGET",
				OwnerID: "target-1", LogicalAccountID: "logical-1",
				RunnerID: "runner-1", State: "OPEN",
			},
			{
				SpaceID: "space-1", OrderID: "order-2",
				TradingAccountID: "account-2", ClientOrderID: "client-2",
				Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "ETHUSDT", ExchangeSymbol: "ETHUSDT",
				OrderType: "MARKET", Side: "BUY", Quantity: "1",
				ReferencePrice: "10", OwnerType: "EXTERNAL",
				OwnerID: "external-1", State: "OPEN",
			},
		} {
			if err := tx.CreateOrder(record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	response, err := (&ExecutionServer{Store: tradeStore}).ListOrders(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.ListOrdersReq{LogicalAccountId: "logical-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		len(response.GetOrders()) != 1 ||
		response.GetOrders()[0].GetOrderId() != "order-1" {
		t.Fatalf("response = %+v", response)
	}
}

func openRPCStore(t *testing.T) *store.Store {
	t.Helper()
	value, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	return value
}
