package runtimecomposition

import (
	"encoding/json"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestManagedEnvironmentAndTimerShareProviderSymbolCodec(t *testing.T) {
	assignment := marketfetch.NodeAssignment{
		Provider: "binance", MarketID: "crypto", InstrumentType: "swap", MarketType: "swap", SourceID: "swap_http",
		DatasetID: "bars", Frequency: "1H", Enabled: true,
		Subjects:        []string{"BTC-USDT-SWAP", "CUSTOM-USDT-SWAP"},
		ExternalSymbols: map[string]string{"BTC-USDT-SWAP": "BTCUSDT", "CUSTOM-USDT-SWAP": "EXPLICIT"},
	}
	env, err := marketfetch.BuildManagedEnvironment(assignment, nil, CompactSymbol)
	require.NoError(t, err)
	var symbols map[string]string
	require.NoError(t, json.Unmarshal([]byte(env["MOOX_MARKET_FETCH_SYMBOLS_JSON"]), &symbols))
	require.Equal(t, map[string]string{"CUSTOM-USDT-SWAP": "EXPLICIT"}, symbols)
	for key, value := range env {
		t.Setenv(key, value)
	}
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_MARKET_FETCH_MODE", "")
	req, _, err := marketfetch.TimerRequestFromEnv("request", "function", time.Now(), marketfetch.RuntimeResolvers{SourceID: DefaultSourceID, Symbol: ResolveSymbol})
	require.NoError(t, err)
	require.Len(t, req.Items, 2)
	require.Equal(t, "BTCUSDT", req.Items[0].Symbol)
	require.Equal(t, "EXPLICIT", req.Items[1].Symbol)
	require.Equal(t, "swap_http", req.Items[0].SourceID)
	_, _, err = marketfetch.TimerRequestFromEnv("request", "function", time.Now())
	require.Error(t, err, "a runtime without the codec cannot guess omitted wire symbols")
}

func TestEnvironmentKeepsOtherMarketSymbolsExplicit(t *testing.T) {
	env, err := marketfetch.BuildManagedEnvironment(marketfetch.NodeAssignment{
		Provider: "eastmoney", MarketID: "stockhk", InstrumentType: "equity", MarketType: "equity", SourceID: "stockhk_http",
		DatasetID: "bars", Frequency: "1m", Enabled: true, Subjects: []string{"00700.XHKG"},
		ExternalSymbols: map[string]string{"00700.XHKG": "00700"},
	}, nil, CompactSymbol)
	require.NoError(t, err)
	require.Equal(t, `{"00700.XHKG":"00700"}`, env["MOOX_MARKET_FETCH_SYMBOLS_JSON"])
}

func TestMarketProviderSymbolForCryptoDerivesBinanceSymbols(t *testing.T) {
	for _, test := range []struct {
		name    string
		market  string
		subject string
		want    string
	}{
		{name: "spot", market: "spot", subject: "BTC-USDT-SPOT", want: "BTCUSDT"},
		{name: "swap", market: "swap", subject: "1000BONK-USDT-SWAP", want: "1000BONKUSDT"},
		{name: "legacy subject", market: "spot", subject: "BTC-USDT", want: "BTCUSDT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveSymbol("binance", "crypto", test.market, test.subject, "")
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
