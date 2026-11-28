package marketfetch

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

// DefaultInstrumentType 返回市场在任务或标签没写品种时用的品种（标的规则给出）；未登记的市场报错。
func DefaultInstrumentType(marketID, marketType string) (marketdata.InstrumentType, error) {
	rules, err := markets.RulesFor(marketID)
	if err != nil {
		return "", err
	}
	return rules.DefaultInstrumentType(marketType), nil
}

// ProviderSymbol 按计划市场的标的规则，把标的 ID 换成传给数据源的代码。
func ProviderSymbol(market PlanMarket, subjectID string) (string, error) {
	instrumentType := marketdata.InstrumentType(strings.ToLower(strings.TrimSpace(market.InstrumentType)))
	if instrumentType == "" {
		var err error
		if instrumentType, err = DefaultInstrumentType(market.MarketID, market.MarketType); err != nil {
			return "", err
		}
	}
	subject, err := parseSubject(market.MarketID, instrumentType, subjectID)
	if err != nil {
		return "", err
	}
	return subject.Code, nil
}

// parseSubject 调市场的标的规则，把标的 ID 换成传给数据源的代码和交易所；instrumentType 为空时用市场的默认品种。
func parseSubject(marketID string, instrumentType marketdata.InstrumentType, subjectID string) (markets.Subject, error) {
	rules, err := markets.RulesFor(marketID)
	if err != nil {
		return markets.Subject{}, err
	}
	if instrumentType == "" {
		instrumentType = rules.DefaultInstrumentType("")
	}
	return rules.ParseSubject(instrumentType, subjectID)
}
