package target

import (
	"context"
	"encoding/json"
	"errors"
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
	bar := timestamppb.New(time.UnixMilli(1000))
	left := &tradeeventpb.LogicalAccountTargetWeightRequested{InstanceId: "instance-1", SessionId: "session-1", StrategyId: "strategy-1", LogicalAccountId: "l", BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(time.UnixMilli(3000)), Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "B", TargetWeight: "0.50"}, {InstrumentId: "A", TargetWeight: "-0"}}}
	right := &tradeeventpb.LogicalAccountTargetWeightRequested{InstanceId: "instance-1", SessionId: "session-1", StrategyId: "strategy-1", LogicalAccountId: "l", BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(time.UnixMilli(3000)), Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "A", TargetWeight: "0"}, {InstrumentId: "B", TargetWeight: "0.5"}}}
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

// 保持标的不取报价、不换算数量；请求哈希随 hold 变化，同样权重的保持与非保持不是同一个请求。
func TestWeightResolverHoldTargetSkipsConversionAndChangesRequestHash(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	// 价格源总是失败：保持标的若被换算会让整个解析失败。
	resolver := WeightResolver{Store: fixture.store, Prices: targetPriceStub{err: errors.New("hold 标的不应该取报价")}, Equity: conversionEquityStub{}, Now: func() time.Time { return fixture.now }}
	bar := timestamppb.New(time.UnixMilli(1000))
	request := func(hold bool) *tradeeventpb.LogicalAccountTargetWeightRequested {
		return &tradeeventpb.LogicalAccountTargetWeightRequested{
			TargetId: "target-1", LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionId: "session-1", StrategyId: "strategy-1",
			BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(time.UnixMilli(3000)),
			Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SWAP", TargetWeight: "0.3", Hold: hold}},
		}
	}
	conversion, err := resolver.Resolve(context.Background(), 0, request(true), "space-1")
	require.NoError(t, err)
	require.Equal(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Hold: true}}, conversion.QuantityTargets)
	require.Empty(t, conversion.ReferencePrices)

	held, err := RequestHash(request(true))
	require.NoError(t, err)
	normal, err := RequestHash(request(false))
	require.NoError(t, err)
	require.NotEqual(t, held, normal)
}

type failingEquityStub struct{}

func (failingEquityStub) ResolveLogicalAccountEquity(context.Context, string, string) (shared.Decimal, int64, error) {
	return shared.Decimal{}, 0, errors.New("权益快照暂时不可用")
}

// 全部是保持标的的目标不依赖权益快照与报价：权益读取失败时保持标记仍然被接受，旧的调仓目标不会继续执行。
func TestWeightResolverAllHoldTargetDoesNotNeedEquity(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	resolver := WeightResolver{Store: fixture.store, Prices: targetPriceStub{err: errors.New("不应取报价")}, Equity: failingEquityStub{}, Now: func() time.Time { return fixture.now }}
	bar := timestamppb.New(time.UnixMilli(1000))
	request := &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "target-1", LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionId: "session-1", StrategyId: "strategy-1",
		BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(time.UnixMilli(3000)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SWAP", TargetWeight: "0.3", Hold: true}},
	}
	conversion, err := resolver.Resolve(context.Background(), 0, request, "space-1")
	require.NoError(t, err)
	require.Equal(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Hold: true}}, conversion.QuantityTargets)

	// 混有需要换算的目标时仍然需要权益快照。
	request.Targets = append(request.Targets, &tradeeventpb.InstrumentWeightTarget{InstrumentId: "ETH-USDT-SWAP", TargetWeight: "0.2"})
	_, err = resolver.Resolve(context.Background(), 0, request, "space-1")
	require.Error(t, err)
}
