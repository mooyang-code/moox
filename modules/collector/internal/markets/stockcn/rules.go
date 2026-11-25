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

// DefaultInstrumentType：没写品种时是股票；写了别的品种（例如 index）原样返回，不折成股票，
// 行情源目录里没有它的数据源，建任务与取数时按“目录里没有数据源”拒绝。
func (Rules) DefaultInstrumentType(marketType string) marketdata.InstrumentType {
	if value := strings.ToLower(strings.TrimSpace(marketType)); value != "" {
		return marketdata.InstrumentType(value)
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
