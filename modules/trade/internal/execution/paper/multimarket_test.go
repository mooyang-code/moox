package paper

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 模拟账户同时开通现货和合约：一个 USDT 资金池，现货持有的币按现货报价估值，合约持仓按合约报价估值，
// 同名代码（BTCUSDT）靠市场区分。
type twoMarketFacts struct {
	FactStore
	positions []store.PositionRecord
}

func (twoMarketFacts) GetTradingAccountByID(context.Context, string) (store.TradingAccountRecord, error) {
	return store.TradingAccountRecord{
		SpaceID: "s", TradingAccountID: "a", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"}, SettlementAsset: "USDT",
	}, nil
}

func (twoMarketFacts) GetPaperBalanceSnapshot(context.Context, string, string) (store.PaperBalanceSnapshot, error) {
	return store.PaperBalanceSnapshot{
		Totals:   map[string]shared.Decimal{"USDT": shared.MustDecimal("1000"), "BTC": shared.MustDecimal("2")},
		Reserved: map[string]shared.Decimal{"USDT": shared.MustDecimal("10")},
	}, nil
}

func (twoMarketFacts) ListInstruments(_ context.Context, _ string, market string) ([]store.InstrumentRecord, error) {
	if market != "SPOT" {
		return nil, nil
	}
	return []store.InstrumentRecord{{MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"}}, nil
}

func (f twoMarketFacts) ListPositions(context.Context, string, string, exchange.SymbolKey) ([]store.PositionRecord, error) {
	return f.positions, nil
}

type twoMarketPrices struct {
	execution.MarketDataSource
	seen map[exchange.SymbolKey]int
	at   time.Time
}

func (m *twoMarketPrices) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{
		{MarketType: exchange.MarketTypeSpot, ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT"},
		{MarketType: exchange.MarketTypeSwap, ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP"},
	}, nil
}

func (m *twoMarketPrices) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	m.seen[key]++
	price := "100" // 现货
	if key.Market == exchange.MarketTypeSwap {
		price = "110" // 合约
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal(price), UpdatedAt: m.at}, nil
}

func TestTwoMarketPaperSnapshotUsesOnePoolAndMarketSpecificPrices(t *testing.T) {
	now := time.UnixMilli(1_000_000).UTC()
	market := &twoMarketPrices{seen: map[exchange.SymbolKey]int{}, at: now}
	adapter := &Adapter{
		Store: twoMarketFacts{positions: []store.PositionRecord{{
			MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", SignedQuantity: "3", EntryPrice: "100", Leverage: "5",
		}}},
		MarketData: market, Now: func() time.Time { return now },
	}

	snapshot, err := adapter.GetAccountSnapshot(context.Background())

	require.NoError(t, err)
	spot := exchange.SymbolKey{Market: exchange.MarketTypeSpot, Symbol: "BTCUSDT"}
	swap := exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTCUSDT"}
	require.Equal(t, 1, market.seen[spot], "现货持有的 BTC 按现货报价估值")
	require.Equal(t, 1, market.seen[swap], "合约持仓按合约报价估值")
	// 权益 = USDT 1000 + 现货 2 BTC × 100 + 合约浮盈 (110-100) × 3
	require.Zero(t, snapshot.Equity.Cmp(shared.MustDecimal("1230")), snapshot.Equity.String())
	require.Zero(t, snapshot.UnrealizedPnL.Cmp(shared.MustDecimal("30")))
	// 保证金 = 3 × 110 / 5；可用 = 现金 + 浮盈 - 保证金 - 预留
	require.Zero(t, snapshot.UsedMargin.Cmp(shared.MustDecimal("66")))
	require.Zero(t, snapshot.AvailableFunds.Cmp(shared.MustDecimal("954")), snapshot.AvailableFunds.String())
	for _, balance := range snapshot.Balances {
		require.Equal(t, exchange.WalletUnified, balance.Wallet, "模拟盘单一资金池只有一个统一钱包")
		if balance.Asset == "USDT" {
			// 现货买单看的是这一行：合约占用的保证金和浮亏已经扣掉，不能按全额现金下现货单。
			require.Zero(t, balance.Available.Cmp(shared.MustDecimal("924")), balance.Available.String())
		}
	}
}

func TestPaperAdapterRoutesQuotesByMarket(t *testing.T) {
	now := time.UnixMilli(1_000_000).UTC()
	market := &twoMarketPrices{seen: map[exchange.SymbolKey]int{}, at: now}
	adapter := &Adapter{MarketData: market}

	for _, key := range []exchange.SymbolKey{
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "BTC-USDT-SPOT"),
		exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTC-USDT-SWAP"),
	} {
		_, err := adapter.GetReferencePrice(context.Background(), key)
		require.NoError(t, err)
	}

	// 内部标的 ID 按所属市场换成交易所代码：两个市场的同名代码各查各的。
	require.Equal(t, 1, market.seen[exchange.SymbolKey{Market: exchange.MarketTypeSpot, Symbol: "BTCUSDT"}])
	require.Equal(t, 1, market.seen[exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTCUSDT"}])
}
