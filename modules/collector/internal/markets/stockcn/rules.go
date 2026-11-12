// Package stockcn 是 A 股市场的标的规则，包初始化时登记到 markets。
package stockcn

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	stocksource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

// MarketID 是 A 股市场的空间 ID。
const MarketID = "stockcn"

func init() { markets.Register(MarketID, Rules{}) }

// Rules 实现 markets.SubjectRules。
type Rules struct{}

// DefaultInstrumentType：没写品种时是股票；marketType 为 index、convertible_bond 时是对应品种。
func (Rules) DefaultInstrumentType(marketType string) marketdata.InstrumentType {
	switch value := marketdata.InstrumentType(strings.ToLower(strings.TrimSpace(marketType))); value {
	case marketdata.InstrumentIndex, marketdata.InstrumentConvertibleBond:
		return value
	}
	return marketdata.InstrumentEquity
}

// ParseSubject 把 600000.XSHG 换成各数据源共用的代码 sh600000，并给出交易所；写法不随品种变。
// 代码与交易所的对应沿用现有的股票代码规则（sources/stockcn.ProviderSymbol）。
func (Rules) ParseSubject(_ marketdata.InstrumentType, subjectID string) (markets.Subject, error) {
	code, err := stocksource.ProviderSymbol(subjectID)
	if err != nil {
		return markets.Subject{}, err
	}
	_, exchange, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(subjectID)), ".")
	return markets.Subject{Code: code, Exchange: exchange}, nil
}

// SubjectAttributes：交易所取自标的，币种取 market.yaml。
func (Rules) SubjectAttributes(item marketdata.Instrument) map[string]string {
	currency := ""
	if config, err := markets.ConfigFor(MarketID); err == nil {
		currency = config.Currency
	}
	return map[string]string{"subject_type": "stock", "market": item.Exchange, "currency": currency, "exchange": item.Exchange}
}
