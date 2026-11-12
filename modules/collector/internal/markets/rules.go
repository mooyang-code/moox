package markets

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// SubjectRules 是一个市场的标的规则。主流程只调用这个接口，不判断市场名。
// 每个市场一个实现包（markets/crypto、markets/stockcn），在包初始化时用 Register 登记。
type SubjectRules interface {
	// DefaultInstrumentType 返回任务或标签没写品种时用哪个；写了 marketType 的按它认。
	DefaultInstrumentType(marketType string) marketdata.InstrumentType
	// ParseSubject 把标的 ID 拆成传给数据源的代码和交易所。
	ParseSubject(instrumentType marketdata.InstrumentType, subjectID string) (Subject, error)
	// SubjectAttributes 返回同步标的时写入的属性。
	SubjectAttributes(instrument marketdata.Instrument) map[string]string
}

// Subject 是 ParseSubject 的结果。
type Subject struct {
	// Code 是传给数据源的代码：A 股 sh600000，crypto BTCUSDT。
	Code string
	// Exchange 是交易所（XSHG、XSHE、XBSE）；crypto 为空。
	Exchange string
}

var (
	rulesMu sync.RWMutex
	rules   = map[string]SubjectRules{}
)

// Register 登记一个市场的标的规则；同一个市场重复登记是编码错误。
func Register(marketID string, value SubjectRules) {
	id := normalizeID(marketID)
	rulesMu.Lock()
	defer rulesMu.Unlock()
	if _, exists := rules[id]; exists || id == "" || value == nil {
		panic(fmt.Sprintf("市场 %q 的标的规则重复登记或为空", marketID))
	}
	rules[id] = value
}

// HasTradeCount 报告市场某个品种的 K 线是否带报价成交量和成交笔数（否则只有成交额 amount）。
// marketType 为空或不是具体品种（例如现货加永续的统一数据集）时，按市场的默认品种判断；未登记的市场报错。
func HasTradeCount(marketID, marketType string) (bool, error) {
	value, err := RulesFor(marketID)
	if err != nil {
		return false, err
	}
	return value.DefaultInstrumentType(marketType).HasTradeCount(), nil
}

// RulesFor 返回市场的标的规则；没登记的市场返回 ErrUnknownMarket。
func RulesFor(marketID string) (SubjectRules, error) {
	rulesMu.RLock()
	defer rulesMu.RUnlock()
	value, ok := rules[normalizeID(marketID)]
	if !ok {
		return nil, fmt.Errorf("%w：%q 没有标的规则", ErrUnknownMarket, marketID)
	}
	return value, nil
}
