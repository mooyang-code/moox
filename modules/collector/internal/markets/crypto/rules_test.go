package crypto

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

func TestRulesAreRegistered(t *testing.T) {
	rules, err := markets.RulesFor("crypto")
	require.NoError(t, err)
	require.IsType(t, Rules{}, rules)
}

func TestDefaultInstrumentType(t *testing.T) {
	rules := Rules{}
	require.Equal(t, marketdata.InstrumentSpot, rules.DefaultInstrumentType(""))
	require.Equal(t, marketdata.InstrumentSpot, rules.DefaultInstrumentType("spot"))
	require.Equal(t, marketdata.InstrumentSwap, rules.DefaultInstrumentType(" SWAP "))
	// 现货加永续的统一数据集不是具体品种，按默认品种。
	require.Equal(t, marketdata.InstrumentSpot, rules.DefaultInstrumentType("multi"))
}

// Code 是交易所的 symbol，现货与永续相同；写法不能变。
func TestParseSubjectKeepsExchangeSymbolForm(t *testing.T) {
	rules := Rules{}
	for _, tc := range []struct {
		instrument marketdata.InstrumentType
		subjectID  string
		want       string
	}{
		{marketdata.InstrumentSpot, "BTC-USDT", "BTCUSDT"},
		{marketdata.InstrumentSwap, "BTC-USDT", "BTCUSDT"},
		{marketdata.InstrumentSpot, "btc-usdt-spot", "BTCUSDT"},
		{marketdata.InstrumentSwap, "1000BONK-USDT-SWAP", "1000BONKUSDT"},
		{marketdata.InstrumentSwap, "币安人生-USDT", "币安人生USDT"},
	} {
		got, err := rules.ParseSubject(tc.instrument, tc.subjectID)
		require.NoError(t, err, tc.subjectID)
		require.Equal(t, markets.Subject{Code: tc.want}, got, tc.subjectID)
	}
	for _, tc := range []struct {
		instrument marketdata.InstrumentType
		subjectID  string
	}{
		{marketdata.InstrumentSwap, "BTC-USDT-SPOT"},
		{marketdata.InstrumentSpot, "BTC-USDT-SWAP"},
		{marketdata.InstrumentSpot, "BTCUSDT"},
		{marketdata.InstrumentSpot, "币安 人生-USDT"},
		{marketdata.InstrumentEquity, "BTC-USDT"},
		{marketdata.InstrumentSpot, ""},
	} {
		_, err := rules.ParseSubject(tc.instrument, tc.subjectID)
		require.Error(t, err, tc.subjectID)
	}
}

func TestSubjectAttributes(t *testing.T) {
	rules := Rules{}
	require.Equal(t,
		map[string]string{"subject_type": "crypto_pair", "market": "CRYPTO", "currency": "USDT", "base": "BTC", "quote": "USDT"},
		rules.SubjectAttributes(marketdata.Instrument{SubjectID: "BTC-USDT", BaseAsset: "btc", QuoteAsset: "usdt"}))
	require.Equal(t, "BTCUSDT", rules.SubjectAttributes(marketdata.Instrument{BaseAsset: "BTC", QuoteAsset: "USDT", Pair: "btcusdt"})["pair"])
}
