package eventconsumer

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
)

func TestNormalizeTargetRequestAcceptsSameInstrumentInSpotAndSwap(t *testing.T) {
	request := &tradeeventpb.LogicalAccountTargetWeightRequested{
		Targets: []*tradeeventpb.InstrumentWeightTarget{
			{InstrumentId: "BTC-USDT", MarketType: "SPOT", TargetWeight: "0.5"},
			{InstrumentId: "BTC-USDT", MarketType: "SWAP", TargetWeight: "-0.2"},
		},
	}

	normalized, err := normalizeTargetRequest(request, []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap})

	require.NoError(t, err)
	require.Equal(t, "BTC-USDT-SPOT", normalized.GetTargets()[0].GetInstrumentId())
	require.Equal(t, "BTC-USDT-SWAP", normalized.GetTargets()[1].GetInstrumentId())
	require.Equal(t, "BTC-USDT", request.GetTargets()[0].GetInstrumentId(), "不能改动入参")
}

func TestNormalizeTargetRequestRejectsMarketOutsideTradableSet(t *testing.T) {
	for name, market := range map[string]string{"空市场": "", "组合没开通的市场": "SWAP"} {
		t.Run(name, func(t *testing.T) {
			request := &tradeeventpb.LogicalAccountTargetWeightRequested{
				Targets: []*tradeeventpb.InstrumentWeightTarget{
					{InstrumentId: "BTC-USDT", MarketType: market, TargetWeight: "0.5"},
				},
			}

			_, err := normalizeTargetRequest(request, []exchange.MarketType{exchange.MarketTypeSpot})

			require.ErrorIs(t, err, exchange.ErrInvalidInstrumentID)
		})
	}
}

func TestNormalizeTargetRequestRejectsDuplicateAfterNormalization(t *testing.T) {
	// 带后缀与不带后缀的写法在事件契约层看是两个标的，标准化后是同一个内部 ID。
	request := &tradeeventpb.LogicalAccountTargetWeightRequested{
		Targets: []*tradeeventpb.InstrumentWeightTarget{
			{InstrumentId: "BTC-USDT", MarketType: "SPOT", TargetWeight: "0.5"},
			{InstrumentId: "BTC-USDT-SPOT", MarketType: "SPOT", TargetWeight: "0.1"},
		},
	}

	_, err := normalizeTargetRequest(request, []exchange.MarketType{exchange.MarketTypeSpot})

	require.ErrorIs(t, err, exchange.ErrInvalidInstrumentID)
}

func TestHandleTargetRejectsMarketTheLogicalAccountCannotTrade(t *testing.T) {
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()

	result := HandleTarget(context.Background(), logicalTargetDelivery(
		t, now, "target-swap", "runner-1", "logical-1",
		[]*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT", MarketType: "SWAP", TargetWeight: "1",
		}},
	), targetOptions(tradeStore, now))

	require.Equal(t, jetstream.TERM, result.Decision)
	require.Equal(t, "invalid_contract", targetErrorCode(result))
}
