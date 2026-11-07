package paper

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// delistedFacts 是一个模拟账户：持有 BTC（正常交易）和 DEAD（已下架的零头），以及一个停止交易的 HALT。
// haltFill 非空时是 HALT 在本账户最近一笔成交的价格；blocked 是下架名单。
type delistedFacts struct {
	FactStore
	account  store.TradingAccountRecord
	haltFill string
	blocked  []store.BlockedInstrumentRecord
}

func (f delistedFacts) ListFills(_ context.Context, _ string, query store.FillQuery) ([]store.FillRecord, int64, error) {
	if query.ExchangeSymbol == "HALTUSDT" && f.haltFill != "" {
		return []store.FillRecord{{Price: f.haltFill}}, 1, nil
	}
	return nil, 0, nil
}

func (f delistedFacts) LoadBlockedSet(context.Context) (store.BlockedSet, error) {
	return store.NewBlockedSet(f.blocked), nil
}

func (f delistedFacts) GetTradingAccountByID(context.Context, string) (store.TradingAccountRecord, error) {
	return f.account, nil
}
func (f delistedFacts) GetPaperBalanceSnapshot(context.Context, string, string) (store.PaperBalanceSnapshot, error) {
	return store.PaperBalanceSnapshot{Totals: map[string]shared.Decimal{
		"USDT": shared.MustDecimal("1000"), "BTC": shared.MustDecimal("1"),
		"DEAD": shared.MustDecimal("3"), "HALT": shared.MustDecimal("2"), "GONE": shared.MustDecimal("5"),
	}, Reserved: map[string]shared.Decimal{}}, nil
}
func (f delistedFacts) ListInstruments(context.Context, string, string) ([]store.InstrumentRecord, error) {
	return []store.InstrumentRecord{
		{MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "TRADING"},
		{MarketType: "SPOT", ExchangeSymbol: "DEADUSDT", InstrumentID: "DEAD-USDT", BaseAsset: "DEAD", QuoteAsset: "USDT", Status: "TRADING"},
		{MarketType: "SPOT", ExchangeSymbol: "HALTUSDT", InstrumentID: "HALT-USDT", BaseAsset: "HALT", QuoteAsset: "USDT", Status: "BREAK"},
		// GONE 没有估值标的：已从标的列表里移除。
	}, nil
}
func (f delistedFacts) ListPositions(context.Context, string, string, exchange.SymbolKey) ([]store.PositionRecord, error) {
	return nil, nil
}

type delistedMarket struct {
	execution.MarketDataSource
	now    time.Time
	asked  []string
	failed bool
}

func (m *delistedMarket) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{
		{MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT"},
		{MarketType: "SPOT", ExchangeSymbol: "DEADUSDT", InstrumentID: "DEAD-USDT"},
		{MarketType: "SPOT", ExchangeSymbol: "HALTUSDT", InstrumentID: "HALT-USDT"},
	}, nil
}
func (m *delistedMarket) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	return execution.MarketQuote{Bid: shared.MustDecimal("99"), Ask: shared.MustDecimal("101"), SourceTime: m.now}, nil
}
func (m *delistedMarket) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	m.asked = append(m.asked, key.Symbol)
	if key.Symbol == "DEADUSDT" {
		// 币安对已下架标的回 HTTP 400 + -1121。
		return exchange.ReferencePrice{}, &exchange.Error{Kind: exchange.ErrorRejected, HTTPStatus: 400, Code: exchange.CodeBinanceInvalidSymbol}
	}
	if m.failed {
		return exchange.ReferencePrice{}, &exchange.Error{Kind: exchange.ErrorRateLimited, HTTPStatus: 429}
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal("100"), UpdatedAt: m.now}, nil
}

func delistedPaperAccount() store.TradingAccountRecord {
	return store.TradingAccountRecord{
		SpaceID: "space", TradingAccountID: "paper", Name: "模拟盘", Exchange: "BINANCE", Markets: []string{"SPOT"},
		ExecutionMode: "PAPER", SettlementAsset: "USDT",
	}
}

// 规则与实盘权益采样一致（execution.DegradedPrice）：交易所确认不存在（DEAD）、没有估值标的（GONE）按 0 计；
// 停止交易（HALT）按最近一笔成交价计；BTC 暂时限流时沿用不超龄的最后已知价。
func TestPaperSnapshotDegradesUnvaluableHoldingsAndSpotSellSkipsSnapshot(t *testing.T) {
	d, _, order := deciderFixture(t, "SPOT")
	now := d.Now()
	market := &delistedMarket{now: now}
	paperAdapter := &Adapter{
		Account:    store.TradingAccountRecord{SpaceID: "space", TradingAccountID: "paper"},
		Store:      delistedFacts{account: delistedPaperAccount(), haltFill: "7"},
		MarketData: market, Now: func() time.Time { return now },
	}
	snapshot, err := paperAdapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err, "下架、停止交易、没有标的的零头都不能让快照失败")
	// USDT 1000 + BTC 1 × 100 + HALT 2 × 7（最近成交价）；DEAD、GONE 按 0 计。
	require.Equal(t, "1114", snapshot.Equity.String())
	require.NotContains(t, market.asked, "HALTUSDT", "状态不可交易的标的不发取价请求")
	require.NotEmpty(t, telemetry.Recent(telemetry.AttentionValuationDegraded, now, time.Minute))

	// 之后 BTC 暂时取价失败（限流）：有不超龄的最后已知价就用它，不报错。
	market.failed = true
	snapshot, err = paperAdapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1114", snapshot.Equity.String())

	// 最后已知价超过保留时间：不再沿用，照旧报错。
	later := now.Add(execution.LastPriceMaxAge + time.Second)
	paperAdapter.Now = func() time.Time { return later }
	_, err = paperAdapter.GetAccountSnapshot(context.Background())
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited), "err = %v", err)
	paperAdapter.Now = func() time.Time { return now }
	market.failed = false

	// 现货卖单撮合不读快照。
	d.Adapters = deciderAdapters{paperAdapter}
	order.Side, order.ReservedAsset, order.RemainingReservedQuantity = "SELL", "BTC", "1"
	decision, err := d.Decide(context.Background(), order)
	require.NoError(t, err)
	require.False(t, decision.Cancel || decision.Rest)
}

// 标的仍在交易、从没取到过价时，暂时的取价失败照旧报错：不能一次限流就把真实持仓按 0 计。
func TestPaperSnapshotKeepsFailingOnTransientQuoteErrorWithoutLastPrice(t *testing.T) {
	now := time.Now().UTC()
	paperAdapter := &Adapter{
		Account: store.TradingAccountRecord{SpaceID: "space", TradingAccountID: "paper"},
		// HALT 有成交记录，报错只能来自仍在交易的 BTC。
		Store:      delistedFacts{account: delistedPaperAccount(), haltFill: "7"},
		MarketData: &delistedMarket{now: now, failed: true}, Now: func() time.Time { return now },
	}
	_, err := paperAdapter.GetAccountSnapshot(context.Background())
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited), "err = %v", err)
}

// BTC 在下架名单里但还在交易、从没取到过价、这次被限流：照旧报错，不因为在名单里就按 0 计。
func TestPaperSnapshotBlockedTradingHoldingKeepsFailingOnQuoteError(t *testing.T) {
	now := time.Now().UTC()
	facts := delistedFacts{account: delistedPaperAccount(), haltFill: "7",
		blocked: []store.BlockedInstrumentRecord{{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT"}}}
	paperAdapter := &Adapter{
		Account: store.TradingAccountRecord{SpaceID: "space", TradingAccountID: "paper"}, Store: facts,
		MarketData: &delistedMarket{now: now, failed: true}, Now: func() time.Time { return now },
	}
	_, err := paperAdapter.GetAccountSnapshot(context.Background())
	require.True(t, exchange.IsKind(err, exchange.ErrorRateLimited), "err = %v", err)
}

// 停止交易的 HALT 没有成交记录：不在下架名单里时报错，在下架名单里按 0 计。
func TestPaperSnapshotHaltedHoldingNeedsFillOrDelisting(t *testing.T) {
	now := time.Now().UTC()
	facts := delistedFacts{account: delistedPaperAccount()}
	paperAdapter := &Adapter{
		Account: store.TradingAccountRecord{SpaceID: "space", TradingAccountID: "paper"}, Store: facts,
		MarketData: &delistedMarket{now: now}, Now: func() time.Time { return now },
	}
	_, err := paperAdapter.GetAccountSnapshot(context.Background())
	require.ErrorContains(t, err, "HALTUSDT 状态为 BREAK")

	facts.blocked = []store.BlockedInstrumentRecord{{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "HALTUSDT"}}
	paperAdapter.Store = facts
	snapshot, err := paperAdapter.GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1100", snapshot.Equity.String())
}
