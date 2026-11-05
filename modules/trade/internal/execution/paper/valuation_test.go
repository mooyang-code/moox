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

type valuationFacts struct {
	FactStore
	positions []store.PositionRecord
}

func (f valuationFacts) GetTradingAccountByID(context.Context, string) (store.TradingAccountRecord, error) {
	return store.TradingAccountRecord{SpaceID: "s", TradingAccountID: "a", Markets: []string{"SWAP"}, SettlementAsset: "USDT"}, nil
}
func (f valuationFacts) GetPaperBalanceSnapshot(context.Context, string, string) (store.PaperBalanceSnapshot, error) {
	return store.PaperBalanceSnapshot{Totals: map[string]shared.Decimal{"USDT": shared.MustDecimal("1000")}}, nil
}
func (f valuationFacts) ListInstruments(context.Context, string, string) ([]store.InstrumentRecord, error) {
	return nil, nil
}
func (f valuationFacts) ListPositions(context.Context, string, string, exchange.SymbolKey) ([]store.PositionRecord, error) {
	return f.positions, nil
}

type valuationMarket struct {
	execution.MarketDataSource
	instruments []exchange.Instrument
	quoteCalls  int
	priceCalls  int
	updatedAt   time.Time
}

func (m *valuationMarket) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return m.instruments, nil
}
func (m *valuationMarket) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	m.quoteCalls++
	return execution.MarketQuote{}, nil
}
func (m *valuationMarket) GetReferencePrice(context.Context, string) (exchange.ReferencePrice, error) {
	m.priceCalls++
	return exchange.ReferencePrice{Price: shared.MustDecimal("110"), UpdatedAt: m.updatedAt}, nil
}

func valuationAdapter(market *valuationMarket, now time.Time) *Adapter {
	symbols := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	facts := valuationFacts{}
	for _, symbol := range symbols {
		market.instruments = append(market.instruments, exchange.Instrument{ExchangeSymbol: symbol})
		facts.positions = append(facts.positions, store.PositionRecord{
			ExchangeSymbol: symbol, SignedQuantity: "1", EntryPrice: "100", Leverage: "2",
		})
	}
	return &Adapter{Store: facts, MarketData: market, Now: func() time.Time { return now }}
}

// 账户快照一笔子单要算多次，估值不能逐持仓请求盘口。
func TestSnapshotValuesPositionsWithoutBookQuotes(t *testing.T) {
	now := time.UnixMilli(1_000_000).UTC()
	market := &valuationMarket{updatedAt: now.Add(-time.Second)}
	snapshot, err := valuationAdapter(market, now).GetAccountSnapshot(context.Background())
	require.NoError(t, err)
	require.Zero(t, market.quoteCalls)
	require.Equal(t, 3, market.priceCalls)
	require.Zero(t, snapshot.UnrealizedPnL.Cmp(shared.MustDecimal("30")))
	require.Zero(t, snapshot.UsedMargin.Cmp(shared.MustDecimal("165")))
}

func TestSnapshotRejectsStaleValuationPrice(t *testing.T) {
	now := time.UnixMilli(1_000_000).UTC()
	market := &valuationMarket{updatedAt: now.Add(-11 * time.Second)}
	_, err := valuationAdapter(market, now).GetAccountSnapshot(context.Background())
	require.ErrorContains(t, err, "stale")
}
