package test

import (
	"context"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This is a local integration test: RPC handlers and production Paper execution
// share a real SQLite store; only market data is substituted, not matching.
func TestOrdinaryPaperRPCWorkerFillAndReplay(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			ctx := spacecontext.WithSpaceID(context.Background(), testSpace)
			path := filepathForTest(t)
			db, err := store.Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			console := &rpc.ConsoleServer{Store: db, Paper: &papersimulation.Service{Store: db}, LogicalAccountServer: &rpc.LogicalAccountServer{Store: db, LogicalAccounts: &logicalapp.Service{Store: db}}}
			marketPB, positionPB := tradepb.MarketType_MARKET_TYPE_SPOT, tradepb.PositionSide_POSITION_SIDE_UNSPECIFIED
			if market == exchange.MarketTypeSwap {
				marketPB, positionPB = tradepb.MarketType_MARKET_TYPE_SWAP, tradepb.PositionSide_POSITION_SIDE_NET
			}
			created, err := console.CreatePaperSimulation(ctx, &tradepb.CreatePaperSimulationReq{
				AccountName: "ordinary-paper", LogicalAccountName: "ordinary-manual", Exchange: tradepb.Exchange_EXCHANGE_BINANCE,
				MarketType: marketPB, SettlementAsset: "USDT", InitialBalance: "100000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0",
				ControlMode: tradepb.ControlMode_CONTROL_MODE_MANUAL,
			})
			require.NoError(t, err)
			require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo())
			logicalID := created.GetLogicalAccount().GetLogicalAccountId()
			members, err := db.ListLogicalAccountMembers(ctx, testSpace, logicalID, false)
			require.NoError(t, err)
			require.Len(t, members, 1)
			accountID := members[0].TradingAccountID
			account, err := db.GetTradingAccount(ctx, testSpace, accountID)
			require.NoError(t, err)
			fake := newFakeExchange(market)
			instrument := fake.instrument
			require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
				return tx.UpsertInstrument(store.InstrumentRecord{
					Exchange: string(instrument.Exchange), MarketType: string(market), Environment: "PRODUCTION",
					InstrumentID: instrument.InstrumentID, ExchangeSymbol: instrument.ExchangeSymbol,
					BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, SettlementAsset: instrument.SettlementAsset,
					Linear: instrument.Linear, ContractValue: instrument.ContractValue.String(), ContractValueAsset: instrument.ContractValueAsset,
					ExchangeQuantityStep: instrument.ExchangeQuantityStep.String(), MinExchangeQuantity: instrument.MinExchangeQuantity.String(),
					PriceTick: instrument.PriceTick.String(), MinNotional: instrument.MinNotional.String(), Status: instrument.Status,
					ExchangeUpdatedAt: testNow.UnixMilli(),
				})
			}))
			adapter := &paperexec.Adapter{Store: db, Account: account, MarketData: fake, Now: func() time.Time { return testNow }}
			f := buildFixture(db, path, fake, recordingAdapter{ExecutionAdapter: adapter, recorder: fake})
			_, err = f.sync.SyncAccount(ctx, accountID)
			require.NoError(t, err)
			service := &operator.Service{Store: db, Orders: f.orders, Syncer: syncBridge{service: f.sync}, Prices: logicalAccountE2EPriceSource{at: testNow}, Now: func() time.Time { return testNow }}
			h := &rpc.ExecutionServer{Store: db, SubmitOrdinary: func(ctx context.Context, c rpc.SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error) {
				result, err := service.SubmitOrder(ctx, operator.SubmitOrderCommand{LogicalAccountID: c.LogicalAccountID, ManualOrderCommand: operator.ManualOrderCommand{
					SpaceID: c.SpaceID, ActionID: c.ActionID, TradingAccountID: c.TradingAccountID, ClientOrderID: c.ClientOrderID,
					InstrumentID: c.InstrumentID, Type: c.OrderType, FillPolicy: c.FillPolicy, Side: c.Side, PositionSide: c.PositionSide,
					Quantity: c.Quantity, LimitPrice: c.LimitPrice, Reason: c.Reason, DeadlineAt: c.DeadlineAt,
				}})
				return result.Action, result.Order, err
			}}
			req := &tradepb.SubmitOrderReq{ActionId: "ordinary-action", LogicalAccountId: logicalID, TradingAccountId: accountID, ClientOrderId: "ordinary-client", InstrumentId: testInstrumentID,
				OrderType: tradepb.OrderType_ORDER_TYPE_MARKET, Side: tradepb.OrderSide_ORDER_SIDE_BUY, PositionSide: positionPB, Quantity: "0.01", Reason: "ordinary", DeadlineAt: testNow.Add(time.Minute).UnixMilli()}
			accepted, err := h.SubmitOrder(ctx, req)
			require.NoError(t, err)
			require.Equal(t, tradepb.ErrorCode_SUCCESS, accepted.GetRetInfo().GetCode(), accepted.GetRetInfo())
			require.Equal(t, "RUNNING", accepted.GetAction().GetStatus())
			require.Equal(t, "PENDING", accepted.GetOrder().GetState())
			require.Zero(t, fake.placeCalls)
			foreign, err := h.SubmitOrder(spacecontext.WithSpaceID(context.Background(), "foreign-space"), req)
			require.NoError(t, err)
			require.NotEqual(t, tradepb.ErrorCode_SUCCESS, foreign.GetRetInfo().GetCode())
			require.Empty(t, foreign.GetOrder().GetOrderId())
			duplicate, err := h.SubmitOrder(ctx, req)
			require.NoError(t, err)
			require.Equal(t, accepted.GetOrder().GetOrderId(), duplicate.GetOrder().GetOrderId())
			require.Zero(t, fake.placeCalls)
			runManualRecoveryWorker(t, f, service)
			productionPaperMatch(t, f)
			filled, err := h.GetOrder(ctx, &tradepb.GetOrderReq{OrderId: accepted.GetOrder().GetOrderId()})
			require.NoError(t, err)
			require.Equal(t, "FILLED", filled.GetOrder().GetState())
			require.Equal(t, "0.01", filled.GetOrder().GetFilledQuantity())
			fills, err := h.ListFills(ctx, &tradepb.ListFillsReq{TradingAccountId: accountID})
			require.NoError(t, err)
			require.Len(t, fills.GetFills(), 1)
			positions, err := h.ListPositions(ctx, &tradepb.ListPositionsReq{LogicalAccountId: logicalID})
			require.NoError(t, err)
			if market == exchange.MarketTypeSwap {
				require.Len(t, positions.GetPositions(), 1)
			} else {
				require.Empty(t, positions.GetPositions(), "SPOT exposure is held in asset balances")
			}
			finalAccount, err := db.GetTradingAccount(ctx, testSpace, accountID)
			require.NoError(t, err)
			require.Equal(t, "99500", finalAccount.Snapshot.AvailableFunds)
			require.Equal(t, "100000", finalAccount.Snapshot.Equity)
			if market == exchange.MarketTypeSpot {
				balances := map[string]string{}
				for _, balance := range finalAccount.Snapshot.Balances {
					balances[balance.Asset] = balance.Total
				}
				require.Equal(t, "0.01", balances["BTC"])
				require.Equal(t, "99500", balances["USDT"])
			}
			replay, err := h.SubmitOrder(ctx, req)
			require.NoError(t, err)
			require.Equal(t, tradepb.ErrorCode_SUCCESS, replay.GetRetInfo().GetCode())
			require.Equal(t, "COMPLETED", replay.GetAction().GetStatus())
			require.Equal(t, "FILLED", replay.GetOrder().GetState())
			require.Equal(t, accepted.GetOrder().GetOrderId(), replay.GetOrder().GetOrderId())
			logical, err := db.GetLogicalAccount(ctx, testSpace, logicalID)
			require.NoError(t, err)
			require.Equal(t, "MANUAL", logical.ControlMode)
			require.Equal(t, "paper simulation created", logical.PauseReason)
			require.NoError(t, db.Close())
			reopened, err := store.Open(path)
			require.NoError(t, err)
			defer reopened.Close()
			persisted, err := reopened.GetOrder(ctx, testSpace, accepted.GetOrder().GetOrderId())
			require.NoError(t, err)
			require.Equal(t, "FILLED", persisted.State)
			_, total, err := reopened.ListFills(ctx, testSpace, store.FillQuery{TradingAccountID: accountID, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, int64(1), total)
		})
	}
}

func TestOrdinaryLiveMockSubmissionMarketRules(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			ctx := spacecontext.WithSpaceID(context.Background(), testSpace)
			fake := newFakeExchange(market)
			f := newFixtureWithMode(t, market, fake, exchange.ExecutionModeLive)
			f.account.LiveTradingEnabled = true // The adapter is the local fake, never a network exchange.
			logical := &logicalapp.Service{Store: f.store, Syncer: syncBridge{service: f.sync}, Now: func() time.Time { return testNow }}
			_, err := logical.Create(ctx, testSpace, testLogicalAccount, "manual-live-mock", exchange.ExecutionModeLive, market, "USDT", "MANUAL")
			require.NoError(t, err)
			require.NoError(t, logical.AddMember(ctx, logicalapp.AddMemberCommand{SpaceID: testSpace, LogicalAccountID: testLogicalAccount, TradingAccountID: testAccount, Enabled: true, AdoptExistingExposure: true}))
			service := &operator.Service{Store: f.store, Orders: f.orders, Syncer: syncBridge{service: f.sync}, Prices: logicalAccountE2EPriceSource{at: testNow}, Now: func() time.Time { return testNow }}
			command := operator.SubmitOrderCommand{LogicalAccountID: testLogicalAccount, ManualOrderCommand: operator.ManualOrderCommand{
				SpaceID: testSpace, ActionID: "ordinary-live", TradingAccountID: testAccount, ClientOrderID: "ordinary-live", InstrumentID: testInstrumentID,
				Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.01"), Reason: "local transport mock",
			}}
			if market == exchange.MarketTypeSwap {
				command.PositionSide = exchange.PositionSideNet
			}
			result, err := service.SubmitOrder(ctx, command)
			require.NoError(t, err)
			require.Equal(t, "PENDING", result.Order.State)
			require.Zero(t, fake.placeCalls)
			runManualRecoveryWorker(t, f, service)
			require.Equal(t, 1, fake.placeCalls)
			current, err := f.store.GetOrder(ctx, testSpace, result.Order.OrderID)
			require.NoError(t, err)
			require.Equal(t, "OPEN", current.State)
			invalid := command
			invalid.ActionID, invalid.ClientOrderID = "wrong-side", "wrong-side"
			invalid.PositionSide = exchange.PositionSideNet
			if market == exchange.MarketTypeSwap {
				invalid.PositionSide = exchange.PositionSideUnspecified
			}
			_, err = service.SubmitOrder(ctx, invalid)
			require.Error(t, err)
			require.Equal(t, 1, fake.placeCalls)
			status := exchange.AccountStatusDisabled
			_, err = f.account.Update(ctx, accountapp.UpdateCommand{TradingAccountID: testAccount, Status: &status})
			require.NoError(t, err)
			disabled := command
			disabled.ActionID, disabled.ClientOrderID = "disabled-account", "disabled-account"
			_, err = service.SubmitOrder(ctx, disabled)
			require.Error(t, err)
			_, err = f.store.GetOperatorAction(ctx, testSpace, disabled.ActionID)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			_, err = f.store.GetOrderByClientID(ctx, testSpace, testAccount, disabled.ClientOrderID)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			require.Equal(t, 1, fake.placeCalls)
			runManualRecoveryWorker(t, f, service)
			require.Equal(t, 1, fake.placeCalls, "a disabled account must not send another order")
			replay, err := service.SubmitOrder(ctx, command)
			require.NoError(t, err)
			require.Equal(t, result.Order.OrderID, replay.Order.OrderID)
		})
	}
}
