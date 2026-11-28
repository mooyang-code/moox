package order

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func mixedStandardAccount(spotUSDT, futuresUSDT string) store.TradingAccountRecord {
	account := executableAccount(exchange.MarketTypeSwap)
	account.Markets = []string{string(exchange.MarketTypeSpot), string(exchange.MarketTypeSwap)}
	account.AccountMode = string(exchange.AccountModeBinanceStandard)
	account.LeverageSettings = store.LeverageSettings{"*": "5"}
	account.Snapshot = store.TradingAccountSnapshot{
		Balances: []store.AssetBalance{
			{Wallet: string(exchange.WalletSpot), Asset: "USDT", Available: spotUSDT},
			{Wallet: string(exchange.WalletFutures), Asset: "USDT", Available: futuresUSDT},
		},
		AvailableFunds: shared.MustDecimal(futuresUSDT).Add(shared.MustDecimal(spotUSDT)).String(),
	}
	return account
}

// 普通账户的现货钱包和合约钱包分开算：合约钱包没钱，现货钱包再多也不能下合约单，反过来也一样。
func TestValidatorChecksFundsInTheOrderWalletOfStandardAccount(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	swapSpec := testSpec(now)
	swapSpec.InstrumentID, swapSpec.PositionSide = "BTC-USDT-SWAP", exchange.PositionSideNet
	spotSpec := testSpec(now)
	spotSpec.InstrumentID = "BTC-USDT-SPOT"

	_, err := testValidator(now, mixedStandardAccount("1000", "10"), testInstrument(exchange.MarketTypeSwap)).Validate(context.Background(), "space-1", swapSpec)
	require.ErrorIs(t, err, ErrInsufficientFunds, "合约钱包只有 10，保证金 20 不够")
	_, err = testValidator(now, mixedStandardAccount("1000", "10"), testInstrument(exchange.MarketTypeSpot)).Validate(context.Background(), "space-1", spotSpec)
	require.NoError(t, err, "现货钱包有 1000，合约钱包没钱不影响现货")

	_, err = testValidator(now, mixedStandardAccount("5", "1000"), testInstrument(exchange.MarketTypeSpot)).Validate(context.Background(), "space-1", spotSpec)
	require.ErrorIs(t, err, ErrInsufficientFunds, "现货钱包只有 5")
	_, err = testValidator(now, mixedStandardAccount("5", "1000"), testInstrument(exchange.MarketTypeSwap)).Validate(context.Background(), "space-1", swapSpec)
	require.NoError(t, err)
}

// 统一账户和 OKX 的两个市场共用统一钱包，合约保证金按账户级可用资金算。
func TestValidatorSharesUnifiedWalletForOKX(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	account := mixedStandardAccount("0", "0")
	account.Exchange = string(exchange.ExchangeOKX)
	account.AccountMode = string(exchange.AccountModeOKXUnified)
	account.Snapshot = store.TradingAccountSnapshot{
		Balances:       []store.AssetBalance{{Wallet: string(exchange.WalletUnified), Asset: "USDT", Available: "1000"}},
		AvailableFunds: "1000",
	}
	swapSpec := testSpec(now)
	swapSpec.InstrumentID, swapSpec.PositionSide = "BTC-USDT-SWAP", exchange.PositionSideNet
	spotSpec := testSpec(now)
	spotSpec.InstrumentID = "BTC-USDT-SPOT"

	_, err := testValidator(now, account, testInstrument(exchange.MarketTypeSwap)).Validate(context.Background(), "space-1", swapSpec)
	require.NoError(t, err)
	_, err = testValidator(now, account, testInstrument(exchange.MarketTypeSpot)).Validate(context.Background(), "space-1", spotSpec)
	require.NoError(t, err)
}
