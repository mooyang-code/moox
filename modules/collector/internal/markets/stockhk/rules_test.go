package stockhk

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

func TestRulesAreRegistered(t *testing.T) {
	rules, err := markets.RulesFor("stockhk")
	require.NoError(t, err)
	require.IsType(t, Rules{}, rules)
}

// 港股只有股票：任务或标签写什么品种都按股票认。
func TestDefaultInstrumentType(t *testing.T) {
	rules := Rules{}
	for _, marketType := range []string{"", "equity", " Index ", "spot"} {
		require.Equal(t, marketdata.InstrumentEquity, rules.DefaultInstrumentType(marketType), marketType)
	}
	hasTradeCount, err := markets.HasTradeCount("stockhk", "")
	require.NoError(t, err)
	require.False(t, hasTradeCount)
}

func TestParseSubject(t *testing.T) {
	rules := Rules{}
	for subjectID, want := range map[string]markets.Subject{
		// 正常代码。
		"09988.XHKG": {Code: "09988", Exchange: "XHKG"},
		"80700.XHKG": {Code: "80700", Exchange: "XHKG"},
		// 前导零是代码的一部分，不能丢。
		"00700.XHKG":   {Code: "00700", Exchange: "XHKG"},
		"00005.XHKG":   {Code: "00005", Exchange: "XHKG"},
		" 00001.xhkg ": {Code: "00001", Exchange: "XHKG"},
	} {
		got, err := rules.ParseSubject(marketdata.InstrumentEquity, subjectID)
		require.NoError(t, err, subjectID)
		require.Equal(t, want, got, subjectID)
	}
	for _, subjectID := range []string{
		// 非法代码：空、没有交易所、位数不对、带非数字、带数据源前缀、全角数字。
		"", "00700", ".XHKG", "700.XHKG", "0700.XHKG", "000700.XHKG", "0070A.XHKG", "hk00700.XHKG", "-0700.XHKG", "００７００.XHKG",
		// 错误交易所。
		"00700.HK", "00700.XSHG", "600000.XSHG", "00700.XHKG.XHKG", "BTC-USDT",
	} {
		_, err := rules.ParseSubject(marketdata.InstrumentEquity, subjectID)
		require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol, subjectID)
	}
}

func TestSubjectAttributes(t *testing.T) {
	require.Equal(t,
		map[string]string{"subject_type": "stock", "market": "XHKG", "currency": "HKD", "exchange": "XHKG"},
		Rules{}.SubjectAttributes(marketdata.Instrument{SubjectID: "00700.XHKG", Exchange: "XHKG"}))
}

// 港股的 market.yaml 不写 subject_sync：没有全市场标的同步的默认任务。
func TestMarketConfig(t *testing.T) {
	config, err := markets.ConfigFor(MarketID)
	require.NoError(t, err)
	require.Equal(t, "HKD", config.Currency)
	require.Equal(t, "hkd", config.AmountUnit())
	require.Equal(t, 90, config.AllowedLatenessSeconds)
	require.Equal(t, "default", config.SeriesTag)
	require.True(t, config.RealtimeBatchNoCap)
	require.Equal(t, markets.SubjectEgressDirect, config.SubjectEgress)
	require.Nil(t, config.SubjectSync)
}
