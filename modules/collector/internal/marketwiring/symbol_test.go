package marketwiring

import (
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

func TestManagedEnvironmentMembershipIsNotTimerClaimPayload(t *testing.T) {
	assignment := marketfetch.NodeAssignment{
		Provider: "binance", MarketID: "crypto", InstrumentType: "swap", MarketType: "swap", SourceID: "swap_http",
		DatasetID: "bars", Frequency: "1h", Enabled: true,
		GroupID: 0, GroupCount: 1, NodeID: "node-1", FunctionName: "function-1", Region: "ap-guangzhou",
		Subjects:        []string{"BTC-USDT", "CUSTOM-USDT"},
		ExternalSymbols: map[string]string{"BTC-USDT": "BTCUSDT", "CUSTOM-USDT": "EXPLICIT"},
	}
	env, err := marketfetch.BuildManagedEnvironment(assignment, nil, ResolveSymbol)
	require.NoError(t, err)
	for key, value := range env {
		t.Setenv(key, value)
	}
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_MARKET_FETCH_MODE", "")
	t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "runtime.local:11003")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "collector-node")
	invocation, err := marketfetch.TimerRequestFromEnv("request", "function", time.Now())
	require.NoError(t, err)
	require.Equal(t, "function", invocation.Claim.GetFunctionName())
	require.NotContains(t, invocation.Claim.String(), "BTC-USDT")
	require.NotContains(t, invocation.Claim.String(), "CUSTOM-USDT")
	require.NotContains(t, invocation.Claim.String(), "EXPLICIT")
	require.NotContains(t, strings.Join([]string{invocation.Claim.String()}, ""), "symbols")
}

func TestEnvironmentDoesNotCarryOtherMarketMembership(t *testing.T) {
	env, err := marketfetch.BuildManagedEnvironment(marketfetch.NodeAssignment{
		Provider: "eastmoney", MarketID: "stockhk", InstrumentType: "equity", MarketType: "equity", SourceID: "stockhk_http",
		DatasetID: "bars", Frequency: "1m", Enabled: true, Subjects: []string{"00700.XHKG"},
		ExternalSymbols: map[string]string{"00700.XHKG": "00700"},
	}, nil, ResolveSymbol)
	require.NoError(t, err)
	require.NotContains(t, env, "MOOX_MARKET_FETCH_SYMBOLS_JSON")
	require.NotContains(t, env, "MOOX_MARKET_FETCH_SUBJECTS")
}

func TestMarketProviderSymbolForCryptoDerivesBinanceSymbols(t *testing.T) {
	for _, test := range []struct {
		name    string
		market  string
		subject string
		want    string
	}{
		{name: "spot", market: "spot", subject: "BTC-USDT", want: "BTCUSDT"},
		{name: "swap", market: "swap", subject: "1000BONK-USDT", want: "1000BONKUSDT"},
		{name: "legacy subject", market: "spot", subject: "BTC-USDT", want: "BTCUSDT"},
		{name: "chinese swap", market: "swap", subject: "币安人生-USDT", want: "币安人生USDT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveSymbol("binance", "crypto", test.market, test.subject)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
