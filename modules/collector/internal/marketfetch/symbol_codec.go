package marketfetch

import (
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

// defaultInstrumentType 返回市场在任务或标签没写品种时用的品种（标的规则给出）；未登记的市场报错。
func defaultInstrumentType(marketID, marketType string) (marketdata.InstrumentType, error) {
	rules, err := markets.RulesFor(marketID)
	if err != nil {
		return "", err
	}
	return rules.DefaultInstrumentType(marketType), nil
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
