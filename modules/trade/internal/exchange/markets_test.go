package exchange

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeMarketsOrdersAndDeduplicates(t *testing.T) {
	got, err := NormalizeMarkets([]MarketType{MarketTypeSwap, MarketTypeSpot, MarketTypeSwap})
	require.NoError(t, err)
	require.Equal(t, []MarketType{MarketTypeSpot, MarketTypeSwap}, got)
	require.Equal(t, "SPOT,SWAP", JoinMarkets(got))
}

func TestNormalizeMarketsRejectsInvalidSets(t *testing.T) {
	for name, markets := range map[string][]MarketType{
		"空集合":       nil,
		"未知市场":      {MarketTypeSpot, "FUTURES"},
		"股票不能和现货混合": {MarketTypeStock, MarketTypeSpot},
		"股票不能和合约混合": {MarketTypeSwap, MarketTypeStock},
	} {
		_, err := NormalizeMarkets(markets)
		require.Error(t, err, name)
	}
	stock, err := NormalizeMarkets([]MarketType{MarketTypeStock})
	require.NoError(t, err)
	require.Equal(t, []MarketType{MarketTypeStock}, stock)
}

func TestParseMarketsRequiresCanonicalText(t *testing.T) {
	got, err := ParseMarkets("SPOT,SWAP")
	require.NoError(t, err)
	require.Equal(t, []MarketType{MarketTypeSpot, MarketTypeSwap}, got)
	for _, raw := range []string{"", "SWAP,SPOT", "SPOT,SPOT", "SPOT, SWAP", "spot"} {
		_, err := ParseMarkets(raw)
		require.Error(t, err, raw)
	}
}

func TestUnionMarketsKeepsFixedOrder(t *testing.T) {
	require.Equal(t,
		[]MarketType{MarketTypeSpot, MarketTypeSwap},
		UnionMarkets([]MarketType{MarketTypeSwap}, []MarketType{MarketTypeSpot}, []MarketType{MarketTypeSwap}),
	)
	require.Empty(t, UnionMarkets())
}

func TestResolveInstrumentMarket(t *testing.T) {
	both := []MarketType{MarketTypeSpot, MarketTypeSwap}
	require.Equal(t, MarketTypeSwap, ResolveInstrumentMarket("BTC-USDT-SWAP", both))
	require.Equal(t, MarketTypeSpot, ResolveInstrumentMarket("BTC-USDT-SPOT", both))
	// 只开通一个市场时，不要求标的 ID 带后缀。
	require.Equal(t, MarketTypeSwap, ResolveInstrumentMarket("BTCUSDT", []MarketType{MarketTypeSwap}))
	require.Equal(t, MarketTypeStock, ResolveInstrumentMarket("600519.XSHG", []MarketType{MarketTypeStock}))
}

func TestSymbolKeyRoundTripAndSeparatesMarkets(t *testing.T) {
	spot := SymbolKey{Market: MarketTypeSpot, Symbol: "BTCUSDT"}
	swap := SymbolKey{Market: MarketTypeSwap, Symbol: "BTCUSDT"}
	require.NotEqual(t, spot, swap)
	require.Equal(t, "SPOT:BTCUSDT", spot.String())
	require.Equal(t, spot, NewSymbolKey("SPOT", "BTCUSDT"))
	got, err := ParseSymbolKey(swap.String())
	require.NoError(t, err)
	require.Equal(t, swap, got)
	for _, raw := range []string{"", "BTCUSDT", "SPOT:", ":BTCUSDT", "FUTURES:BTCUSDT"} {
		_, err := ParseSymbolKey(raw)
		require.Error(t, err, raw)
	}
	require.True(t, SymbolKey{}.IsZero())
	require.False(t, spot.IsZero())
}

func TestMarketOfInstrumentID(t *testing.T) {
	require.Equal(t, MarketTypeSwap, MarketOfInstrumentID("BTC-USDT-SWAP"))
	require.Equal(t, MarketTypeSpot, MarketOfInstrumentID("BTC-USDT-SPOT"))
	require.Equal(t, MarketTypeStock, MarketOfInstrumentID("600519.XSHG"))
}
