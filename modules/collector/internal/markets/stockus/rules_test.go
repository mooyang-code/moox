package stockus

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

func TestRulesAreRegistered(t *testing.T) {
	rules, err := markets.RulesFor("stockus")
	require.NoError(t, err)
	require.IsType(t, Rules{}, rules)
}

// 美股只有股票：任务或标签写什么品种都按股票认。
func TestDefaultInstrumentType(t *testing.T) {
	rules := Rules{}
	for _, marketType := range []string{"", "equity", " Index ", "spot"} {
		require.Equal(t, marketdata.InstrumentEquity, rules.DefaultInstrumentType(marketType), marketType)
	}
	hasTradeCount, err := markets.HasTradeCount("stockus", "")
	require.NoError(t, err)
	require.False(t, hasTradeCount)
}

func TestParseSubject(t *testing.T) {
	rules := Rules{}
	for subjectID, want := range map[string]markets.Subject{
		// 正常代码，三个交易所。
		"AAPL.XNAS": {Code: "AAPL", Exchange: "XNAS"},
		"JPM.XNYS":  {Code: "JPM", Exchange: "XNYS"},
		"SPY.XASE":  {Code: "SPY", Exchange: "XASE"},
		// 带类别的代码保留点，按最后一个点拆分。
		"BRK.B.XNYS": {Code: "BRK.B", Exchange: "XNYS"},
		"BF.B.XNYS":  {Code: "BF.B", Exchange: "XNYS"},
		// 单字母代码。
		"F.XNYS": {Code: "F", Exchange: "XNYS"},
		"T.XNYS": {Code: "T", Exchange: "XNYS"},
		// 含数字、长度正好 10 位。
		"A1.XNAS":         {Code: "A1", Exchange: "XNAS"},
		"ABCDEFGHIJ.XNAS": {Code: "ABCDEFGHIJ", Exchange: "XNAS"},
	} {
		got, err := rules.ParseSubject(marketdata.InstrumentEquity, subjectID)
		require.NoError(t, err, subjectID)
		require.Equal(t, want, got, subjectID)
	}
	for name, subjectID := range map[string]string{
		"小写不改写":     "aapl.XNAS",
		"小写交易所":     "AAPL.xnas",
		"前后空格":      " AAPL.XNAS ",
		"空代码":       ".XNAS",
		"空字符串":      "",
		"代码含空格":     "AA PL.XNAS",
		"非法字符横线":    "BRK-B.XNYS",
		"非法字符下划线":   "BRK_B.XNYS",
		"非法字符斜杠":    "BRK/B.XNYS",
		"全角字母":      "ＡＡＰＬ.XNAS",
		"两个连续点":     "BRK..B.XNYS",
		"点在代码首":     ".AAPL.XNAS",
		"点在代码尾":     "AAPL..XNAS",
		"代码以数字开头":   "1AAPL.XNAS",
		"过长":        "ABCDEFGHIJK.XNAS",
		"带点后过长":     "ABCDEFGH.IJ.XNAS",
		"错误交易所":     "AAPL.XSHG",
		"没有交易所":     "AAPL",
		"没有交易所带类别":  "BRK.B",
		"交易所写成美股前缀": "AAPL.US",
		"带数据源前缀":    "105.AAPL.XNAS",
		"只有交易所":     "XNAS",
		"加密货币":      "BTC-USDT",
	} {
		_, err := rules.ParseSubject(marketdata.InstrumentEquity, subjectID)
		require.ErrorIs(t, err, marketdata.ErrUnsupportedSymbol, name)
	}
}

func TestSubjectAttributes(t *testing.T) {
	for _, exchange := range []string{"XNAS", "XNYS", "XASE"} {
		require.Equal(t,
			map[string]string{"subject_type": "stock", "market": exchange, "currency": "USD", "exchange": exchange},
			Rules{}.SubjectAttributes(marketdata.Instrument{SubjectID: "AAPL." + exchange, Exchange: exchange}), exchange)
	}
}

// 美股的 market.yaml 不写 subject_sync：没有全市场标的同步的默认任务。
func TestMarketConfig(t *testing.T) {
	config, err := markets.ConfigFor(MarketID)
	require.NoError(t, err)
	require.Equal(t, "USD", config.Currency)
	require.Equal(t, "usd", config.AmountUnit())
	require.Equal(t, 90, config.AllowedLatenessSeconds)
	require.Equal(t, "default", config.SeriesTag)
	require.True(t, config.RealtimeBatchNoCap)
	require.Equal(t, markets.SubjectEgressDirect, config.SubjectEgress)
	require.Nil(t, config.SubjectSync)
}
