package exchange

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeMarketsOrdersAndDeduplicates(t *testing.T) {
	got, err := NormalizeMarkets([]MarketType{MarketTypeSwap, MarketTypeSpot, MarketTypeSwap})
	require.NoError(t, err)
	require.Equal(t, []MarketType{MarketTypeSpot, MarketTypeSwap}, got)
	require.Equal(t, "spot,swap", JoinMarkets(got))
}

func TestNormalizeMarketsRejectsInvalidSets(t *testing.T) {
	for name, markets := range map[string][]MarketType{
		"空集合":       nil,
		"未知市场":      {MarketTypeSpot, "FUTURES"},
		"股票不能和现货混合": {MarketTypeEquity, MarketTypeSpot},
		"股票不能和合约混合": {MarketTypeSwap, MarketTypeEquity},
	} {
		_, err := NormalizeMarkets(markets)
		require.Error(t, err, name)
	}
	stock, err := NormalizeMarkets([]MarketType{MarketTypeEquity})
	require.NoError(t, err)
	require.Equal(t, []MarketType{MarketTypeEquity}, stock)
}

func TestParseMarketsRequiresCanonicalText(t *testing.T) {
	got, err := ParseMarkets("spot,swap")
	require.NoError(t, err)
	require.Equal(t, []MarketType{MarketTypeSpot, MarketTypeSwap}, got)
	for _, raw := range []string{"", "swap,spot", "spot,spot", "spot, swap", "SPOT,SWAP", "SPOT"} {
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
	// 一律按后缀判断：单市场账户也不例外，没有后缀的加密货币标的没有市场。
	require.Equal(t, MarketType(""), ResolveInstrumentMarket("BTCUSDT", []MarketType{MarketTypeSwap}))
	require.Equal(t, MarketTypeSwap, ResolveInstrumentMarket("BTC-USDT-SWAP", []MarketType{MarketTypeSpot}), "后缀不能被账户的市场改写")
	require.Equal(t, MarketType(""), ResolveInstrumentMarket("600519.XSHG", []MarketType{MarketTypeSpot}), "没开股票市场的账户不认股票代码")
	require.Equal(t, MarketTypeEquity, ResolveInstrumentMarket("600519.XSHG", []MarketType{MarketTypeEquity}))
}

func TestSymbolKeyRoundTripAndSeparatesMarkets(t *testing.T) {
	spot := SymbolKey{Market: MarketTypeSpot, Symbol: "BTCUSDT"}
	swap := SymbolKey{Market: MarketTypeSwap, Symbol: "BTCUSDT"}
	require.NotEqual(t, spot, swap)
	require.Equal(t, "spot:BTCUSDT", spot.String())
	require.Equal(t, spot, NewSymbolKey("spot", "BTCUSDT"))
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
	require.Equal(t, MarketTypeEquity, MarketOfInstrumentID("600519.XSHG"))
}
