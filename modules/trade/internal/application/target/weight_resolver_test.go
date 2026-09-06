package target

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type conversionEquityStub struct{}

func (conversionEquityStub) ResolveLogicalAccountEquity(context.Context, string, string) (shared.Decimal, int64, error) {
	return shared.MustDecimal("2000"), 1900, nil
}

func TestWeightResolverFreezesTotalQuantityWithoutExecutionPin(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	resolver := WeightResolver{Store: fixture.store, Prices: targetPriceStub{price: shared.MustDecimal("100")}, Equity: conversionEquityStub{}, Now: func() time.Time { return fixture.now }}
	request := &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "target-1", LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionId: "session-1", StrategyId: "strategy-1",
		BarEndTime: timestamppb.New(time.UnixMilli(1000)), EffectiveAt: timestamppb.New(time.UnixMilli(1000)), ValidUntil: timestamppb.New(time.UnixMilli(3000)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SWAP", TargetWeight: "0.5"}},
	}
	conversion, err := resolver.Resolve(context.Background(), 0, request, "space-1")
	require.NoError(t, err)
	require.Equal(t, "2000", conversion.Equity.String())
	require.Equal(t, int64(1900), conversion.EquitySourceTime)
	require.Equal(t, "100", conversion.ReferencePrices["BTC-USDT-SWAP"])
	quantities, err := json.Marshal(conversion.QuantityTargets)
	require.NoError(t, err)
	require.JSONEq(t, `[{"instrument_id":"BTC-USDT-SWAP","quantity":"10"}]`, string(quantities))
	require.Equal(t, []ReferencePriceEvidence{{InstrumentID: "BTC-USDT-SWAP", TradingAccountID: "account-a", ExchangeSymbol: "BTCUSDT", Price: "100", UpdatedAt: 2000}}, conversion.ReferencePriceEvidence)
}

func TestWeightResolverPersistsRepeatingQuantityAtLogicalPrecision(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSpot)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: string(exchange.MarketTypeSpot), ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SPOT", BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "0.01", MinExchangeQuantity: "0.01", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	resolver := WeightResolver{Store: fixture.store, Prices: targetPriceStub{price: shared.MustDecimal("3")}, Equity: conversionEquityStub{}, Now: func() time.Time { return fixture.now }}
	request := &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "target-quantized", LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionId: "session-1", StrategyId: "strategy-1",
		BarEndTime: timestamppb.New(time.UnixMilli(1000)), EffectiveAt: timestamppb.New(time.UnixMilli(1000)), ValidUntil: timestamppb.New(time.UnixMilli(3000)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", TargetWeight: "0.1"}},
	}
	conversion, err := resolver.Resolve(context.Background(), 0, request, "space-1")
	require.NoError(t, err)
	require.JSONEq(t, `[{"instrument_id":"BTC-USDT-SPOT","quantity":"66.666666666666666666"}]`, string(mustJSON(conversion.QuantityTargets)))
}

func TestRequestHashCanonicalizesTargetOrderAndDecimal(t *testing.T) {
	left := &tradeeventpb.LogicalAccountTargetWeightRequested{RunnerId: "r", LogicalAccountId: "l", CommandSequence: 1, Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "B", TargetWeight: "0.50"}, {InstrumentId: "A", TargetWeight: "-0"}}}
	right := &tradeeventpb.LogicalAccountTargetWeightRequested{RunnerId: "r", LogicalAccountId: "l", CommandSequence: 1, Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "A", TargetWeight: "0"}, {InstrumentId: "B", TargetWeight: "0.5"}}}
	h1, err := RequestHash(left)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := RequestHash(right)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash changed after canonicalization: %s != %s", h1, h2)
	}
}
