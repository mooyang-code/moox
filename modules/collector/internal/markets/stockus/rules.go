// Package stockus 是美股市场的标的规则，包初始化时登记到 markets。
//
// 标的 ID 是 <代码>.<MIC>，MIC 只有三个：XNAS（纳斯达克）、XNYS（纽交所）、XASE。
// 东方财富把纽交所、纳斯达克之外的美股（含 ETF 和 Cboe 上市的 CBOE）统一放在 107 市场，
// 这里一律记为 XASE。这个取名并不准确（XASE 本来是美国证券交易所），是已接受的取舍：
// 标的 ID 里的交易所只用来决定请求数据源时的市场前缀，不代表真实的上市地。
package stockus

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

const (
	// MarketID 是美股市场的空间 ID。
	MarketID = "stockus"
	// ExchangeNasdaq 是纳斯达克的 MIC。
	ExchangeNasdaq = "XNAS"
	// ExchangeNYSE 是纽交所的 MIC。
	ExchangeNYSE = "XNYS"
	// ExchangeOther 是东方财富 107 市场（其他交易所、ETF、CBOE）的标记，取名见包注释。
	ExchangeOther = "XASE"
	// maxCodeLength 是代码的最大长度（含点）。
	maxCodeLength = 10
)

func init() { markets.Register(MarketID, Rules{}) }

// Rules 实现 markets.SubjectRules。
type Rules struct{}

// DefaultInstrumentType：美股只有股票一个品种。
func (Rules) DefaultInstrumentType(string) marketdata.InstrumentType {
	return marketdata.InstrumentEquity
}

// ParseSubject 把 AAPL.XNAS 拆成传给数据源的代码 AAPL 和交易所 XNAS。
// 带类别的代码保留点，按最后一个点拆分：BRK.B.XNYS 的代码是 BRK.B、交易所是 XNYS。
// 标的 ID 必须是规范写法（大写、不带空格），不做静默改写。
func (Rules) ParseSubject(_ marketdata.InstrumentType, subjectID string) (markets.Subject, error) {
	cut := strings.LastIndex(subjectID, ".")
	if cut < 0 {
		return markets.Subject{}, fmt.Errorf("%w: 美股标的 %q 缺少交易所，格式必须是 <代码>.<%s|%s|%s>",
			marketdata.ErrUnsupportedSymbol, subjectID, ExchangeNasdaq, ExchangeNYSE, ExchangeOther)
	}
	code, exchange := subjectID[:cut], subjectID[cut+1:]
	if !isExchange(exchange) {
		return markets.Subject{}, fmt.Errorf("%w: 美股标的 %q 的交易所必须是 %s、%s、%s 之一",
			marketdata.ErrUnsupportedSymbol, subjectID, ExchangeNasdaq, ExchangeNYSE, ExchangeOther)
	}
	if !isCode(code) {
		return markets.Subject{}, fmt.Errorf("%w: 美股标的 %q 的代码不合法：只允许大写字母、数字、点，首字符是字母，点不在首尾且不连续，长度不超过 %d",
			marketdata.ErrUnsupportedSymbol, subjectID, maxCodeLength)
	}
	return markets.Subject{Code: code, Exchange: exchange}, nil
}

func isExchange(exchange string) bool {
	return exchange == ExchangeNasdaq || exchange == ExchangeNYSE || exchange == ExchangeOther
}

// isCode 判断代码是否合法：大写字母、数字、点；首字符是字母；点不在首尾、不连续；不超过 10 个字符。
func isCode(code string) bool {
	if code == "" || len(code) > maxCodeLength || code[0] < 'A' || code[0] > 'Z' || code[len(code)-1] == '.' {
		return false
	}
	for i := 1; i < len(code); i++ {
		c := code[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' && code[i-1] != '.':
		default:
			return false
		}
	}
	return true
}

// SubjectAttributes：交易所与市场都写 MIC（取自标的），币种取 market.yaml。
func (Rules) SubjectAttributes(item marketdata.Instrument) map[string]string {
	currency := ""
	if config, err := markets.ConfigFor(MarketID); err == nil {
		currency = config.Currency
	}
	return map[string]string{"subject_type": "stock", "market": item.Exchange, "currency": currency, "exchange": item.Exchange}
}
