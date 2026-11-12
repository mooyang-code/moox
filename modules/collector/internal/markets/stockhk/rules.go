// Package stockhk 是港股市场的标的规则，包初始化时登记到 markets。
package stockhk

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

const (
	// MarketID 是港股市场的空间 ID。
	MarketID = "stockhk"
	// Exchange 是香港交易所的 MIC。
	Exchange = "XHKG"
	// codeLength 是港股代码的位数（不足的在前面补零，例如 00700）。
	codeLength = 5
)

func init() { markets.Register(MarketID, Rules{}) }

// Rules 实现 markets.SubjectRules。
type Rules struct{}

// DefaultInstrumentType：港股只有股票一个品种。
func (Rules) DefaultInstrumentType(string) marketdata.InstrumentType {
	return marketdata.InstrumentEquity
}

// ParseSubject 把 00700.XHKG 拆成传给数据源的代码 00700 和交易所 XHKG；
// 代码必须是五位数字（保留前导零），交易所必须是 XHKG。
func (Rules) ParseSubject(_ marketdata.InstrumentType, subjectID string) (markets.Subject, error) {
	code, exchange, ok := strings.Cut(strings.ToUpper(strings.TrimSpace(subjectID)), ".")
	if !ok || exchange != Exchange {
		return markets.Subject{}, fmt.Errorf("%w: 港股标的 %q 的交易所必须是 %s", marketdata.ErrUnsupportedSymbol, subjectID, Exchange)
	}
	if !isCode(code) {
		return markets.Subject{}, fmt.Errorf("%w: 港股标的 %q 的代码必须是 %d 位数字", marketdata.ErrUnsupportedSymbol, subjectID, codeLength)
	}
	return markets.Subject{Code: code, Exchange: Exchange}, nil
}

func isCode(code string) bool {
	if len(code) != codeLength {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// SubjectAttributes：交易所固定是 XHKG，币种取 market.yaml。
func (Rules) SubjectAttributes(marketdata.Instrument) map[string]string {
	currency := ""
	if config, err := markets.ConfigFor(MarketID); err == nil {
		currency = config.Currency
	}
	return map[string]string{"subject_type": "stock", "market": Exchange, "currency": currency, "exchange": Exchange}
}
