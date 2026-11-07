package fundsettings

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

// 读取（宽松）、读取（严格）和写入三条路径的键集合必须相同：少一个键就会出现一边认得一边不认。
func TestKeySetsAgreeBetweenLenientAndStrictDocuments(t *testing.T) {
	require.ElementsMatch(t, shared.JSONKeys(rawDocument{}), shared.JSONKeys(Document{}))
}

func TestParseForWriteRejectsUnknownAndMiscasedKeys(t *testing.T) {
	_, _, err := ParseForWrite(`{"reserve_ratio":"0.1","mystery":true}`)
	require.ErrorContains(t, err, "未知")
	_, _, err = ParseForWrite(`{"Reserve_Ratio":"0.1"}`)
	require.ErrorContains(t, err, "大小写")
	_, _, err = ParseForWrite(`{"bnb_discount":"yes"}`)
	require.Error(t, err, "布尔字段给字符串")
	_, _, err = ParseForWrite(`{"reserve_ratio":0.1}`)
	require.Error(t, err, "数值必须是字符串")
}

func TestStablecoinMarginMustSumToOneAndIncludeUSDT(t *testing.T) {
	for _, raw := range []string{
		`{"stablecoin_margin":{"USDT":"0.5","USDC":"0.4"}}`,
		`{"stablecoin_margin":{"USDT":"0.5","USDC":"0.6"}}`,
		`{"stablecoin_margin":{"USDC":"0.5","FDUSD":"0.5"}}`,
		`{"stablecoin_margin":{"USDT":"0","USDC":"1"}}`,
		`{"stablecoin_margin":{"USDT":"-0.5","USDC":"1.5"}}`,
		`{"stablecoin_margin":{"USDT":"abc"}}`,
		`{"stablecoin_margin":{"usdt":"0.5","USDT":"0.5"}}`,
		`{"stablecoin_margin":{"USDT":"0.3333","USDC":"0.3333","FDUSD":"0.3333"}}`,
	} {
		_, _, err := ParseForWrite(raw)
		require.Error(t, err, raw)
	}
	settings, canonical, err := ParseForWrite(`{"stablecoin_margin":{"usdt":"0.5000","usdc":"0.25","fdusd":"0.25"}}`)
	require.NoError(t, err)
	require.Equal(t, []string{"FDUSD", "USDC", "USDT"}, settings.StablecoinMargin)
	require.Equal(t, "0.5", settings.StablecoinTargets["USDT"].String())
	require.JSONEq(t, `{"stablecoin_margin":{"USDT":"0.5","USDC":"0.25","FDUSD":"0.25"}}`, canonical)
	_, _, err = ParseForWrite(`{"stablecoin_margin":{"USDT":"1"}}`)
	require.NoError(t, err, "只有 USDT 也合法（等于不调平）")
}

func TestExcludedAssetGroupsCannotOverlap(t *testing.T) {
	for _, raw := range []string{
		`{"stablecoin_margin":{"USDT":"0.5","USDC":"0.5"},"margin_coins":{"usdc":"10"}}`,
		`{"margin_coins":{"ETH":"10"},"arbitrage_base_assets":["eth"]}`,
		`{"arbitrage_base_assets":["BTC","btc"]}`,
		`{"arbitrage_base_assets":["BTC USDT"]}`,
	} {
		_, _, err := ParseForWrite(raw)
		require.Error(t, err, raw)
	}
}

func TestBNBDiscountAndDustSettings(t *testing.T) {
	settings, canonical, err := ParseForWrite(`{"bnb_discount":true,"dust_convert_enabled":true}`)
	require.NoError(t, err)
	require.True(t, settings.BNBDiscount)
	require.True(t, settings.DustConvert)
	require.Equal(t, 15*24*time.Hour, settings.DustConvertInterval, "默认 15 天")
	require.JSONEq(t, `{"bnb_discount":true,"dust_convert_enabled":true}`, canonical)
	settings, _, err = ParseForWrite(`{"dust_convert_enabled":true,"dust_convert_interval_days":7}`)
	require.NoError(t, err)
	require.Equal(t, 7*24*time.Hour, settings.DustConvertInterval)
	for _, raw := range []string{`{"dust_convert_interval_days":-1}`, `{"dust_convert_interval_days":366}`} {
		_, _, err := ParseForWrite(raw)
		require.Error(t, err, raw)
	}
}

// 写入的规范 JSON 再走读取路径，有效设置不变；宽松解析同样认得新键。
func TestCanonicalJSONRoundTripsThroughBothReaders(t *testing.T) {
	_, canonical, err := ParseForWrite(`{"reserve_ratio":"0.0500","bnb_discount":true,"bnb_reserve_amount":"200.0","dust_convert_enabled":true,
		"stablecoin_margin":{"usdt":"0.6","usdc":"0.4"},"margin_coins":{"eth":"100"},"arbitrage_base_assets":["btc"]}`)
	require.NoError(t, err)
	strict, err := Parse(canonical)
	require.NoError(t, err)
	lenient, warning, err := ParseLenient(canonical)
	require.NoError(t, err)
	require.NoError(t, warning)
	require.Equal(t, strict, lenient)
	require.True(t, lenient.BNBDiscount)
	require.Equal(t, "0.6", lenient.StablecoinTargets["USDT"].String())
}

func TestLenientKeepsExclusionsWhenStablecoinTargetsAreBroken(t *testing.T) {
	settings, warning, err := ParseLenient(`{"stablecoin_margin":{"USDT":"0.5","USDC":"oops"}}`)
	require.NoError(t, err)
	require.Error(t, warning)
	require.True(t, settings.ExcludedFromManaged("USDC"))
	require.Nil(t, settings.StablecoinTargets, "目标比例坏了不做调平")
}
