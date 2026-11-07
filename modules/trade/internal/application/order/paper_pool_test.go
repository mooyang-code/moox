package order

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

type fixedSnapshotAdapter struct {
	*adapterStub
	snapshot exchange.AccountSnapshot
}

func (a fixedSnapshotAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return a.snapshot, nil
}

// 模拟盘现货和合约共用一个资金池：账户开通了合约，预留结算币的订单（现货买单、合约开仓）都要扣掉合约占用的保证金；
// 现货卖出预留的是基础资产，不扣；没开通合约的账户、实盘也不扣。
func TestPaperMarginPoolAppliesToSpotBuyOfMixedAccount(t *testing.T) {
	s, _, adapter := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	account := executableAccount(exchange.MarketTypeSwap)
	account.Markets = []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	account.ExecutionMode = exchange.ExecutionModePaper
	now := s.now()
	s.Adapters = errorBoundarySource{adapter: fixedSnapshotAdapter{adapterStub: adapter, snapshot: exchange.AccountSnapshot{
		UsedMargin: shared.MustDecimal("10000"), UnrealizedPnL: shared.MustDecimal("-500"), ExchangeUpdatedAt: now,
		Present: exchange.AccountSnapshotPresence{UsedMargin: true, UnrealizedPnL: true},
	}}}

	spotBuy := Validation{Account: account, ReservedAsset: "USDT", Instrument: exchange.Instrument{MarketType: exchange.MarketTypeSpot}}
	adjustment, err := s.paperMarginAdjustment(context.Background(), spotBuy)
	require.NoError(t, err)
	require.Equal(t, "-10500", adjustment.String(), "先开合约占 10000 保证金、浮亏 500，现货买单可用现金少 10500")

	spotSell := Validation{Account: account, ReservedAsset: "BTC", Instrument: exchange.Instrument{MarketType: exchange.MarketTypeSpot}}
	adjustment, err = s.paperMarginAdjustment(context.Background(), spotSell)
	require.NoError(t, err)
	require.True(t, adjustment.IsZero(), "现货卖出预留的是基础资产")

	reduceOnly := spotBuy
	reduceOnly.ReduceOnly = true
	adjustment, err = s.fundsMarginAdjustment(context.Background(), reduceOnly)
	require.NoError(t, err)
	require.True(t, adjustment.IsZero(), "减仓单不占新保证金")

	spotOnly := spotBuy
	spotOnly.Account.Markets = []exchange.MarketType{exchange.MarketTypeSpot}
	require.False(t, paperUsesMarginPool(spotOnly), "没开通合约的账户没有保证金占用")
	live := spotBuy
	live.Account.ExecutionMode = exchange.ExecutionModeLive
	require.False(t, paperUsesMarginPool(live))
}
