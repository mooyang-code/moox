package tradingaccount

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestComputePaperEquitySinglePoolConsistency(t *testing.T) {
	margin, pnl := PaperPositionValuation(
		shared.MustDecimal("2"),
		shared.MustDecimal("100"),
		shared.MustDecimal("115"),
		shared.MustDecimal("5"),
	)
	require.Equal(t, "46", margin.String())
	require.Equal(t, "30", pnl.String())

	// 混合市场（现货 + 合约）：总权益含现货市值与合约浮盈；合约可用含浮盈，现货结算币可用不含浮盈。
	snap := ComputePaperEquity(PaperEquityInput{
		SettlementAsset:   "USDT",
		HasBalanceMarket:  true,
		HasPositionMarket: true,
		Totals: map[string]shared.Decimal{
			"USDT": shared.MustDecimal("1000"),
			"BTC":  shared.MustDecimal("0.5"),
		},
		Locked: map[string]shared.Decimal{
			"USDT": shared.MustDecimal("10"),
		},
		AssetPrices: map[string]shared.Decimal{
			"BTC": shared.MustDecimal("200"),
		},
		UsedMargin:    margin,
		UnrealizedPnL: pnl,
	})
	// Equity = 1000 + 30 + 0.5*200 = 1130
	require.Equal(t, "1130", snap.Equity.String())
	// Swap available = 1000 + 30 - 46 - 10 = 974
	require.Equal(t, "974", snap.AvailableFunds.String())
	// Spot settlement available = 1000 + min(30,0) - 46 - 10 = 944
	var usdt exchange.AssetBalance
	for _, row := range snap.Balances {
		if row.Asset == "USDT" {
			usdt = row
		}
	}
	require.Equal(t, exchange.WalletUnified, usdt.Wallet)
	require.Equal(t, "944", usdt.Available.String())
	// PaperMarginPoolAdjustment 与 ComputePaperEquity 保持完全一致
	require.Equal(t, "944", shared.MustDecimal("1000").Add(PaperMarginPoolAdjustment(false, margin, pnl)).Sub(shared.MustDecimal("10")).String())
	require.Equal(t, "974", shared.MustDecimal("1000").Add(PaperMarginPoolAdjustment(true, margin, pnl)).Sub(shared.MustDecimal("10")).String())
}

func TestComputePaperEquityFloatingLossAndT1AndCachedClose(t *testing.T) {
	// 合约浮亏时现货结算币可用同时扣除浮亏和保证金。
	snap := ComputePaperEquity(PaperEquityInput{
		SettlementAsset:   "USDT",
		HasBalanceMarket:  true,
		HasPositionMarket: true,
		Totals:            map[string]shared.Decimal{"USDT": shared.MustDecimal("1000")},
		UsedMargin:        shared.MustDecimal("200"),
		UnrealizedPnL:     shared.MustDecimal("-150"),
	})
	require.Equal(t, "850", snap.Equity.String())
	require.Equal(t, "650", snap.AvailableFunds.String())
	require.Equal(t, "650", snap.Balances[0].Available.String())

	// 关闭模拟盘且无实时报价时，含余额市场保留缓存净值。
	cached := shared.MustDecimal("1234.5")
	closed := ComputePaperEquity(PaperEquityInput{
		SettlementAsset:      "USDT",
		HasBalanceMarket:     true,
		HasPositionMarket:    true,
		Totals:               map[string]shared.Decimal{"USDT": shared.MustDecimal("1000")},
		UsedMargin:           shared.MustDecimal("200"),
		UnrealizedPnL:        shared.MustDecimal("-150"),
		PreserveCachedEquity: &cached,
	})
	require.Equal(t, "1234.5", closed.Equity.String())
	require.Equal(t, "650", closed.AvailableFunds.String())

	// T+1 扣减当日买入股票且不小于 0。
	stock := ComputePaperEquity(PaperEquityInput{
		SettlementAsset:  "CNY",
		HasBalanceMarket: true,
		Totals:           map[string]shared.Decimal{"CNY": shared.MustDecimal("5000"), "600519.XSHG": shared.MustDecimal("300")},
		Locked:           map[string]shared.Decimal{"600519.XSHG": shared.MustDecimal("100")},
		T1Locked:         map[string]shared.Decimal{"600519.XSHG": shared.MustDecimal("250")},
		AssetPrices:      map[string]shared.Decimal{"600519.XSHG": shared.MustDecimal("10")},
	})
	require.Equal(t, "8000", stock.Equity.String())
	require.Equal(t, "0", stock.Balances[0].Available.String())
}
