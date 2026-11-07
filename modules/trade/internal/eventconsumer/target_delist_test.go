package eventconsumer

import (
	"context"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
)

// capturingResolver 记下换算器收到的目标，用来确认被跳过的标的已经改成“保持”。
type capturingResolver struct {
	testWeightResolver
	received []*tradeeventpb.InstrumentWeightTarget
}

func (c *capturingResolver) Resolve(ctx context.Context, signalTime int64, request *tradeeventpb.LogicalAccountTargetWeightRequested, spaceID string) (targetapp.WeightConversion, error) {
	c.received = request.GetTargets()
	return c.testWeightResolver.Resolve(ctx, signalTime, request, spaceID)
}

func seedDelistedInstrument(t *testing.T, tradeStore *store.Store, blocked bool) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT",
			InstrumentID: "FOO-USDT-SPOT", BaseAsset: "FOO", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "BREAK",
		})
	}))
	if blocked {
		_, err := tradeStore.BlockInstrument(context.Background(), store.BlockedInstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", Reason: "下架", BlockedAt: 1,
		})
		require.NoError(t, err)
	}
}

// 名单里已经不能交易的标的：目标里只跳过这一项（改成保持），其余照常接收，不拒绝整份目标。
func TestHandleTargetSkipsBlockedUntradableInstrumentInsteadOfRejecting(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	seedDelistedInstrument(t, tradeStore, true)
	now := time.Now().UTC()
	resolver := &capturingResolver{}
	options := TargetOptions{Store: tradeStore, Now: func() time.Time { return now }, WeightResolver: resolver}

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-delist", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{
			{InstrumentId: "BTC-USDT-SPOT", MarketType: "SPOT", TargetWeight: "0.5"},
			{InstrumentId: "FOO-USDT-SPOT", MarketType: "SPOT", TargetWeight: "0.3"},
		},
	), options)

	require.Equal(t, jetstream.ACK, result.Decision, "err=%v", result.Err)
	held := map[string]bool{}
	for _, target := range resolver.received {
		held[target.GetInstrumentId()] = target.GetHold()
	}
	require.Equal(t, map[string]bool{"BTC-USDT-SPOT": false, "FOO-USDT-SPOT": true}, held)
}

// 不在名单里的不可交易标的仍然整份拒绝。
func TestHandleTargetStillRejectsUntradableInstrumentOutsideTheList(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	seedDelistedInstrument(t, tradeStore, false)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-untradable", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "FOO-USDT-SPOT", MarketType: "SPOT", TargetWeight: "0.3"}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Contains(t, result.Err.Error(), "FOO-USDT-SPOT")
}
