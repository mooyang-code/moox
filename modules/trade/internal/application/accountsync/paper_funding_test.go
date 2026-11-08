package accountsync

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// paperPublicQuotes 是模拟盘同步估值用的公共行情：任何标的都给一个刚取到的价格。
type paperPublicQuotes struct{}

func (paperPublicQuotes) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return nil, nil
}
func (paperPublicQuotes) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	price := shared.MustDecimal("100")
	return execution.MarketQuote{Bid: price, Ask: price, Last: price, SourceTime: time.Now()}, nil
}
func (paperPublicQuotes) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{Price: shared.MustDecimal("100"), UpdatedAt: time.Now()}, nil
}

// 资金费按成交推结算时刻的持仓，前提是模拟盘同步把持仓原样写回：成交之后跑一次同步（含平仓后删除持仓行、留墓碑），
// 持仓表里的数量和按成交推出的各结算时刻持仓都不变。
func TestPaperSyncWriteBackKeepsFundingPositions(t *testing.T) {
	ctx := context.Background()
	db := openSyncStore(t)
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			CreatedAt: time.UnixMilli(1), SpaceID: "space-1", TradingAccountID: "paper-1", Name: "paper",
			Exchange: "BINANCE", Markets: []string{"SWAP"}, ExecutionMode: "PAPER", Environment: "PAPER",
			SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
			LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "5"},
		}); err != nil {
			return err
		}
		for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: symbol, InstrumentID: symbol, BaseAsset: symbol[:3],
				QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: true, ContractValue: "1", ContractValueAsset: symbol[:3],
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	// 成交都在保护期（10 秒）之前：同步会按快照删除空仓、写墓碑，不会因为太新而跳过。
	base := time.Now().Add(-3 * time.Hour).Truncate(time.Millisecond)
	reducer := &consumer.Reducer{Store: db}
	source := consumer.Source{SpaceID: "space-1", TradingAccountID: "paper-1", Kind: consumer.OriginPaperMatcher}
	for index, step := range []struct {
		symbol, quantity string
		side             exchange.Side
		at               time.Duration
	}{
		{"BTCUSDT", "2", exchange.SideBuy, 0},
		{"ETHUSDT", "3", exchange.SideSell, 20 * time.Minute},
		{"BTCUSDT", "2", exchange.SideSell, 40 * time.Minute},
		{"ETHUSDT", "1", exchange.SideBuy, 70 * time.Minute},
	} {
		id := string(rune('a' + index))
		require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
			return tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", OrderID: "order-" + id, TradingAccountID: "paper-1", ClientOrderID: "client-" + id,
				ExchangeOrderID: "exchange-" + id, Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: step.symbol,
				OrderType: "MARKET", Side: string(step.side), PositionSide: "NET", Quantity: step.quantity,
				ReferencePrice: "100", ReferencePriceAt: base.UnixMilli(), OwnerType: "EXTERNAL", OwnerID: "exchange-" + id,
				State: "OPEN", ReservedAsset: "USDT", ReservedQuantity: "100", RemainingReservedQuantity: "100", Version: 1,
			})
		}))
		applied, err := reducer.ApplyFill(ctx, exchange.Fill{
			Market: exchange.MarketTypeSwap, ExchangeTradeID: "trade-" + id, ExchangeOrderID: "exchange-" + id, ClientOrderID: "client-" + id,
			ExchangeSymbol: step.symbol, Side: step.side, PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal(step.quantity),
			Price: shared.MustDecimal("100"), SettlementAsset: "USDT", TradedAt: base.Add(step.at),
		}, source)
		require.NoError(t, err)
		require.True(t, applied)
	}
	settlements := []time.Duration{-time.Minute, 10 * time.Minute, 30 * time.Minute, 60 * time.Minute, 2 * time.Hour}
	snapshot := func() map[string][]string {
		activity, err := db.PaperSwapActivity(ctx, "space-1", "paper-1", base.Add(-time.Hour).UnixMilli())
		require.NoError(t, err)
		sweep := activity.Sweep()
		result := map[string][]string{}
		for _, offset := range settlements {
			sweep.AdvanceTo(base.Add(offset).UnixMilli())
			for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
				result[symbol] = append(result[symbol], sweep.Quantity(symbol).String())
			}
		}
		for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
			result[symbol+":now"] = []string{activity.Positions[symbol].String()}
		}
		return result
	}
	before := snapshot()
	require.Equal(t, []string{"0", "2", "2", "0", "0"}, before["BTCUSDT"])
	require.Equal(t, []string{"0", "0", "-3", "-3", "-2"}, before["ETHUSDT"])

	account, err := db.GetTradingAccountByID(ctx, "paper-1")
	require.NoError(t, err)
	adapter := &paper.Adapter{Account: account, Store: db, MarketData: paperPublicQuotes{}}
	service := &Service{Store: db, Fills: reducer, Adapters: syncAdapterSource{adapter: adapter}}
	for range 2 {
		_, err := service.SyncAccount(ctx, "paper-1")
		require.NoError(t, err)
	}
	require.Equal(t, before, snapshot(), "同步写回之后，持仓和按成交推出的结算时刻持仓都不变")
	// 模拟盘快照来自持仓表本身，平掉的持仓留着数量为 0 的行，同步不会删它。快照里没有这一行时（相当于交易所不返回空仓），
	// 同步按删除边界删掉它并留下墓碑：持仓和推出的结算时刻持仓照样不变。
	positions, err := adapter.ListPositionSnapshots(ctx)
	require.NoError(t, err)
	var kept []store.PositionRecord
	for _, position := range positions {
		if position.SignedQuantity.IsZero() {
			continue
		}
		kept = append(kept, store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "paper-1", MarketType: string(position.Market), InstrumentID: position.InstrumentID,
			ExchangeSymbol: position.ExchangeSymbol, PositionSide: string(position.PositionSide), SignedQuantity: position.SignedQuantity.String(),
			EntryPrice: position.EntryPrice.String(), MarkPrice: position.MarkPrice.String(), Leverage: position.Leverage.String(),
			MarginMode: string(position.MarginMode), ExchangeUpdatedAt: position.ExchangeUpdatedAt.UnixMilli(),
		})
	}
	require.Len(t, kept, 1)
	readAt := time.Now()
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.ReplacePositionsForAccount("space-1", "paper-1", kept, readAt.UnixMilli(), readAt.Add(-positionLagGrace).UnixMilli())
	}))
	var rows, tombstones int64
	require.NoError(t, db.DBForTest().Raw("SELECT COUNT(*) FROM t_trading_positions WHERE c_exchange_symbol = 'BTCUSDT'").Scan(&rows).Error)
	require.NoError(t, db.DBForTest().Raw("SELECT COUNT(*) FROM t_trading_position_tombstones WHERE c_exchange_symbol = 'BTCUSDT'").Scan(&tombstones).Error)
	require.Zero(t, rows, "空仓行被删除")
	require.Equal(t, int64(1), tombstones, "删除时留下墓碑")
	after := snapshot()
	require.Equal(t, before["BTCUSDT"], after["BTCUSDT"])
	require.Equal(t, before["ETHUSDT"], after["ETHUSDT"])
	require.Equal(t, before["ETHUSDT:now"], after["ETHUSDT:now"])
	require.Equal(t, []string{"0"}, after["BTCUSDT:now"])
}
