package fundsettings

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

func TestParseDefaultsAndExcludedAssets(t *testing.T) {
	empty, err := Parse("{}")
	require.NoError(t, err)
	require.True(t, empty.ReserveRatio.IsZero())
	require.False(t, empty.BNBReserveEnabled())
	require.False(t, empty.ExcludedFromManaged("BNB"), "没开 BNB 保留时 BNB 是普通资产")

	settings, err := Parse(`{
		"reserve_ratio": "0.05", "bnb_reserve_amount": "200",
		"stablecoin_margin": {"usdc": "0.5", "USDT": "0.5"},
		"margin_coins": {"eth": "100"}, "arbitrage_base_assets": ["btc"]}`)
	require.NoError(t, err)
	require.Equal(t, "0.05", settings.ReserveRatio.String())
	for _, asset := range []string{"BNB", "USDC", "ETH", "BTC", "bnb"} {
		require.True(t, settings.ExcludedFromManaged(asset), asset)
	}
	require.False(t, settings.ExcludedFromManaged("SOL"))
	require.True(t, settings.IsArbitrageBase("BTC"))
	require.Zero(t, settings.MarginCoinValue().Cmp(shared.MustDecimal("100")))
}

func TestParseRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{
		`not json`, `{"reserve_ratio":"1"}`, `{"reserve_ratio":"-0.1"}`, `{"reserve_ratio":"abc"}`,
		`{"bnb_reserve_amount":"-1"}`, `{"margin_coins":{"ETH":"-5"}}`,
	} {
		_, err := Parse(raw)
		require.Error(t, err, raw)
	}
}
