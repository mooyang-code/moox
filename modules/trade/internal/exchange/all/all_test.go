package all

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestBlankImport_ShouldRegisterBuiltInExchanges(t *testing.T) {
	for _, name := range []exchange.Exchange{exchange.ExchangeBinance, exchange.ExchangeOKX} {
		adapter, err := exchange.Bind(exchange.AccountConfig{
			ExchangeAccountID: "account-1",
			Exchange:          name,
			MarketType:        exchange.MarketTypeSpot,
			ExecutionMode:     exchange.ExecutionModePaper,
			SettlementAsset:   "USDT",
		}, exchange.Credential{})
		require.NoError(t, err, "exchange %s", name)
		require.NotNil(t, adapter)
		require.Equal(t, name, adapter.Exchange())
	}
}

func TestOKXLiveBindingRequiresPassphrase(t *testing.T) {
	_, err := exchange.Bind(exchange.AccountConfig{
		ExchangeAccountID: "account-1",
		Exchange:          exchange.ExchangeOKX,
		MarketType:        exchange.MarketTypeSpot,
		ExecutionMode:     exchange.ExecutionModeLive,
		SettlementAsset:   "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret"})
	require.Error(t, err)
	require.True(t, exchange.IsKind(err, exchange.ErrorRejected), "error = %v", err)
}
