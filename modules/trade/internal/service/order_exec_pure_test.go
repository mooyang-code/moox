package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitSymbol_KnownPairs_ShouldSplit(t *testing.T) {
	base, quote := splitSymbol("BTCUSDT")
	assert.Equal(t, "BTC", base)
	assert.Equal(t, "USDT", quote)
	base, quote = splitSymbol("ETHBTC")
	assert.Equal(t, "ETH", base)
	assert.Equal(t, "BTC", quote)
}

func TestFreezeCost_BuyLimit_ShouldFreezeQuote(t *testing.T) {
	cur, amt, err := freezeCost("buy", "BTCUSDT", "100", "2", "")
	require.NoError(t, err)
	assert.Equal(t, "USDT", cur)
	assert.Equal(t, "200", amt)
}

func TestFreezeCost_Sell_ShouldFreezeBase(t *testing.T) {
	cur, amt, err := freezeCost("sell", "BTCUSDT", "100", "1.5", "")
	require.NoError(t, err)
	assert.Equal(t, "BTC", cur)
	assert.Equal(t, "1.5", amt)
}

func TestFreezeCost_MarketBuyByAmount_ShouldUseAmount(t *testing.T) {
	cur, amt, err := freezeCost("buy", "BTCUSDT", "0", "0", "50")
	require.NoError(t, err)
	assert.Equal(t, "USDT", cur)
	assert.Equal(t, "50", amt)
}

func TestAddSubMulDivSvc_ShouldCompute(t *testing.T) {
	sum, err := addSvc("1.5", "2.5")
	require.NoError(t, err)
	assert.Equal(t, "4", sum)
	diff, err := subSvc("5", "2")
	require.NoError(t, err)
	assert.Equal(t, "3", diff)
	prod, err := mulSvc("2", "3")
	require.NoError(t, err)
	assert.Equal(t, "6", prod)
	quot, err := divSvcSafe("10", "4")
	require.NoError(t, err)
	assert.Equal(t, "2.5", quot)
	zero, err := divSvcSafe("10", "0")
	require.NoError(t, err)
	assert.Equal(t, "0", zero)
}

func TestRemainingFreeze_PartialFill_ShouldShrink(t *testing.T) {
	cur, amt, err := remainingFreeze(&Order{
		Side: "buy", Symbol: "BTCUSDT", Price: "100", Quantity: "2", FilledQty: "0.5",
	})
	require.NoError(t, err)
	assert.Equal(t, "USDT", cur)
	assert.Equal(t, "150", amt)
}
