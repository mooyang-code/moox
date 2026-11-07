package fundsettings

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 数值字段坏了取默认值，排除集合保留：设置坏了不能让底仓等排除资产被交易。
func TestParseLenientKeepsExclusionsWhenNumbersAreBroken(t *testing.T) {
	settings, warning, err := ParseLenient(`{
		"reserve_ratio": "oops", "bnb_reserve_amount": 200,
		"stablecoin_margin": {"usdc": "x"}, "margin_coins": {"eth": "bad"}, "arbitrage_base_assets": ["btc"]}`)

	require.NoError(t, err)
	require.Error(t, warning)
	require.True(t, settings.ReserveRatio.IsZero(), "预留比例坏了取默认 0")
	require.True(t, settings.BNBReserveEnabled(), "BNB 保留金额读不出来按开启处理")
	require.True(t, settings.ExcludedFromManaged("BNB"))
	require.True(t, settings.ExcludedFromManaged("USDC"))
	require.True(t, settings.ExcludedFromManaged("ETH"), "保证金币的键保留，配置值坏了按 0")
	require.True(t, settings.IsArbitrageBase("BTC"))
	require.True(t, settings.MarginCoinValue().IsZero())
}

// 排除字段本身的类型坏了、整份不是合法 JSON：无从知道排除哪些资产，直接报错。
func TestParseLenientFailsWhenExclusionFieldsAreUnreadable(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`{"arbitrage_base_assets": "BTC"}`,
		`{"arbitrage_base_assets": [1, 2]}`,
		`{"margin_coins": ["ETH"]}`,
		`{"stablecoin_margin": 5}`,
	} {
		_, _, err := ParseLenient(raw)
		require.Error(t, err, raw)
	}
}

func TestParseLenientDefaultsAndStrictAgreeOnGoodSettings(t *testing.T) {
	raw := `{"reserve_ratio":"0.05","bnb_reserve_amount":"200","margin_coins":{"ETH":"100"},"arbitrage_base_assets":["BTC"]}`
	lenient, warning, err := ParseLenient(raw)
	require.NoError(t, err)
	require.NoError(t, warning)
	strict, err := Parse(raw)
	require.NoError(t, err)
	require.Equal(t, strict, lenient)
	empty, warning, err := ParseLenient("")
	require.NoError(t, err)
	require.NoError(t, warning)
	require.False(t, empty.HasExclusions())
}

// 键名大小写不同时，宽松和严格解析结论相同（都按 encoding/json 的不区分大小写匹配）：
// 不能出现严格一侧认为 BTC 是底仓、宽松一侧不认，执行器就把底仓平掉。
func TestParseLenientMatchesStrictOnKeyCase(t *testing.T) {
	for _, raw := range []string{
		`{"ARBITRAGE_BASE_ASSETS":["BTC"],"BNB_RESERVE_AMOUNT":"200","Margin_Coins":{"ETH":"100"},"Stablecoin_Margin":{"USDC":"0.5"},"RESERVE_RATIO":"0.05"}`,
		`{"Arbitrage_Base_Assets":["BTC"]}`,
	} {
		lenient, warning, err := ParseLenient(raw)
		require.NoError(t, err, raw)
		require.NoError(t, warning, raw)
		strict, err := Parse(raw)
		require.NoError(t, err, raw)
		require.Equal(t, strict, lenient, raw)
		require.True(t, lenient.IsArbitrageBase("BTC"), raw)
	}
}
