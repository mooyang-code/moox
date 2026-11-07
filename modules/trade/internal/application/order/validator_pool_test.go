package order

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// 币安统一账户：现货在 MARGIN 钱包、合约在 UNIFIED 钱包，但资金是同一个池，
// 现货买入和合约保证金都看账户级可用资金（已扣保证金），两个钱包里的余额标签不分割资金。
func TestPortfolioMarginAccountSharesOneFundsPool(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	account := mixedStandardAccount("0", "0")
	account.AccountMode = exchange.AccountModeBinancePortfolioMargin
	account.Snapshot = exchange.AccountSnapshot{
		Balances: []exchange.AssetBalance{
			{Wallet: exchange.WalletMargin, Asset: "USDT", Available: shared.MustDecimal("300")},
			{Wallet: exchange.WalletUnified, Asset: "USDT", Available: shared.MustDecimal("700")},
		},
		AvailableFunds: shared.MustDecimal("1000"),
	}
	spotSpec := testSpec(now)
	spotSpec.InstrumentID = "BTC-USDT-SPOT"
	spotSpec.Quantity = shared.MustDecimal("9") // 名义 900，超过 MARGIN 钱包的 300，但没超过共用的资金池 1000
	swapSpec := testSpec(now)
	swapSpec.InstrumentID, swapSpec.PositionSide = "BTC-USDT-SWAP", exchange.PositionSideNet

	_, err := testValidator(now, account, testInstrument(exchange.MarketTypeSpot)).Validate(context.Background(), "space-1", spotSpec)
	require.NoError(t, err, "现货买单按共用的资金池判断")
	_, err = testValidator(now, account, testInstrument(exchange.MarketTypeSwap)).Validate(context.Background(), "space-1", swapSpec)
	require.NoError(t, err)

	account.Snapshot.AvailableFunds = shared.MustDecimal("500")
	_, err = testValidator(now, account, testInstrument(exchange.MarketTypeSpot)).Validate(context.Background(), "space-1", spotSpec)
	require.ErrorIs(t, err, ErrInsufficientFunds, "合约占掉保证金后资金池只剩 500，900 的现货买单被拒")
}

// 统一账户和 OKX 的预留互相占用：现货预留要算进合约的占用里，普通账户则按钱包分开。
func TestMarketsSharingFundsFollowsFundsPool(t *testing.T) {
	standard := mixedStandardAccount("1", "1")
	require.Equal(t, []string{"SPOT"}, standard.MarketsSharingFunds(exchange.MarketTypeSpot))
	require.Equal(t, []string{"SWAP"}, standard.MarketsSharingFunds(exchange.MarketTypeSwap))

	portfolio := standard
	portfolio.AccountMode = exchange.AccountModeBinancePortfolioMargin
	require.Equal(t, []string{"SPOT", "SWAP"}, portfolio.MarketsSharingFunds(exchange.MarketTypeSpot))
	require.Equal(t, []string{"SPOT", "SWAP"}, portfolio.MarketsSharingFunds(exchange.MarketTypeSwap))

	okx := standard
	okx.Exchange, okx.AccountMode = exchange.ExchangeOKX, exchange.AccountModeUnspecified
	require.Equal(t, []string{"SPOT", "SWAP"}, okx.MarketsSharingFunds(exchange.MarketTypeSwap), "OKX 尚未检测账户类型时也按统一账户")
}
