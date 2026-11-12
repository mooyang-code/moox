package stockcn

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

func TestRulesAreRegistered(t *testing.T) {
	rules, err := markets.RulesFor("stockcn")
	require.NoError(t, err)
	require.IsType(t, Rules{}, rules)
}

func TestDefaultInstrumentType(t *testing.T) {
	rules := Rules{}
	require.Equal(t, marketdata.InstrumentEquity, rules.DefaultInstrumentType(""))
	require.Equal(t, marketdata.InstrumentEquity, rules.DefaultInstrumentType("equity"))
	require.Equal(t, marketdata.InstrumentIndex, rules.DefaultInstrumentType(" Index "))
	require.Equal(t, marketdata.InstrumentConvertibleBond, rules.DefaultInstrumentType("convertible_bond"))
	require.Equal(t, marketdata.InstrumentEquity, rules.DefaultInstrumentType("spot"))
}

// Code 是各数据源、采集计划、行里的 provider_symbol 现在用的写法，不能变。
func TestParseSubjectKeepsProviderSymbolForm(t *testing.T) {
	rules := Rules{}
	for subjectID, want := range map[string]markets.Subject{
		"600000.XSHG":   {Code: "sh600000", Exchange: "XSHG"},
		"000001.XSHE":   {Code: "sz000001", Exchange: "XSHE"},
		"920002.XBSE":   {Code: "bj920002", Exchange: "XBSE"},
		" 600519.xshg ": {Code: "sh600519", Exchange: "XSHG"},
	} {
		got, err := rules.ParseSubject(marketdata.InstrumentEquity, subjectID)
		require.NoError(t, err, subjectID)
		require.Equal(t, want, got, subjectID)
	}
	// 写法不随品种变。
	other, err := rules.ParseSubject(marketdata.InstrumentIndex, "600000.XSHG")
	require.NoError(t, err)
	require.Equal(t, markets.Subject{Code: "sh600000", Exchange: "XSHG"}, other)

	for _, subjectID := range []string{"", "600000", "600000.XHKG", "00700.XHKG", "BTC-USDT"} {
		_, err := rules.ParseSubject(marketdata.InstrumentEquity, subjectID)
		require.Error(t, err, subjectID)
	}
}

func TestSubjectAttributes(t *testing.T) {
	require.Equal(t,
		map[string]string{"subject_type": "stock", "market": "XSHG", "currency": "CNY", "exchange": "XSHG"},
		Rules{}.SubjectAttributes(marketdata.Instrument{SubjectID: "600000.XSHG", Exchange: "XSHG"}))
}
