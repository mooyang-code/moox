package exchange

import "github.com/mooyang-code/moox/modules/trade/internal/domain/shared"

// MarketRule 是一个市场类型（现货、合约、股票）共有的交易规则，与在哪个场所交易无关。
// 业务代码按规则运行，不再判断具体的市场类型；各场所不同的部分（T+1、费用、价位表）在 ExchangeRule 里，按“场所 + 市场”取。
type MarketRule struct {
	// PositionFromBalance 为 true 时持仓来自余额，否则来自持仓表。
	PositionFromBalance bool
	// UsesMargin 为 true 时按保证金占用资金，否则占用全额。
	UsesMargin bool
	CanShort   bool
	// ReduceOnly 表示交易所支持只减仓订单。
	ReduceOnly bool
	// Leverage 表示账户需要配置杠杆和保证金模式。
	Leverage bool
	// ContractValue 表示下单数量要按合约面值换算。
	ContractValue bool
	PositionSide  PositionSide
	// RawInstrumentID 表示标的 ID 原样使用，不拼市场后缀，如 600519.XSHG、00700.XHKG。
	RawInstrumentID bool
	// BoardLot 表示按整手交易：买入不足一手的差额视为已满足；卖出时余下不足一手的零股可以一次卖完。
	// 加密货币“数量低于交易所最小值”的判断不受影响。
	BoardLot bool
	// QuoteGuards 表示按报价里的停牌标记和涨跌停价（绝对价，每次取自报价）判断能不能成交：停牌顺延；报价给了涨跌停价时
	// （A 股）下单价截在涨跌停价之间，涨停买、跌停卖顺延。港股没有涨跌停，报价里涨跌停价为空，只有停牌生效。
	// 这类市场没有市价单，清仓、减仓也发限价 IOC。
	QuoteGuards bool
}

var marketRules = map[MarketType]MarketRule{
	MarketTypeSpot: {PositionFromBalance: true},
	MarketTypeSwap: {
		UsesMargin: true, CanShort: true, ReduceOnly: true, Leverage: true,
		ContractValue: true, PositionSide: PositionSideNet,
	},
	MarketTypeEquity: {PositionFromBalance: true, RawInstrumentID: true, BoardLot: true, QuoteGuards: true},
}

// RuleFor 返回市场类型共有的规则，未知市场返回零值。数据库记录里的市场是字符串，所以接受两种类型。
func RuleFor[T ~string](market T) MarketRule {
	return marketRules[MarketType(market)]
}

// FeeFunc 是一个场所的费用函数：一笔成交（或预留）的全部费用。commissionRate 是账户配置的佣金比例。
type FeeFunc func(side Side, notional, commissionRate shared.Decimal) shared.Decimal

// ExchangeRule 是一个场所在一个市场上的完整交易规则：市场类型共有的规则，加上各场所不同的部分。
// 同是股票（equity），A 股（ashare）是 T+1、A 股费用、价格笼子 10 个价位；港股（hkex）是 T+0、港股费用、价位按价格分档。
type ExchangeRule struct {
	MarketRule
	// T1 表示当日买入的数量次日才能卖出（按北京时间的自然日，见 T1Since）；为假就是 T+0，当日买入可卖。
	T1 bool
	// Fee 是场所自己的费用函数（有最低佣金、印花税等）；nil 表示按账户费率比例收。下单预留、下单容量、成交扣费三处都用它。
	Fee FeeFunc
	// PriceCageTicks 是价格笼子的最小价位数：比例带（MaxBuyPriceRatio、MinSellPriceRatio）和这么多个最小价位取更宽的那个；0 表示只有比例带。
	PriceCageTicks int
	// PriceTick 是按价位分档的价格步长（港交所价位表）；nil 表示标的自己的固定步长。
	PriceTick func(price shared.Decimal) shared.Decimal
}

type exchangeMarket struct {
	name   Exchange
	market MarketType
}

// exchangeRules 只登记与市场类型默认值不同的场所：没有登记的“场所 + 市场”就是市场类型共有的规则。新增场所时在这里加一行。
var exchangeRules = map[exchangeMarket]ExchangeRule{
	{ExchangeAShare, MarketTypeEquity}: {T1: true, Fee: AShareFee, PriceCageTicks: 10},
	{ExchangeHKEX, MarketTypeEquity}:   {Fee: HKFee, PriceTick: HKPriceTick},
}

// RuleAt 返回“场所 + 市场”的交易规则。未知市场返回零值；没有单独登记的场所只有市场类型共有的规则。
func RuleAt[E ~string, M ~string](name E, market M) ExchangeRule {
	rule := exchangeRules[exchangeMarket{Exchange(name), MarketType(market)}]
	rule.MarketRule = marketRules[MarketType(market)]
	return rule
}

// BuyCost 是买入的总花费：成交额加场所的费用。只对有费用函数的场所有意义。
func (r ExchangeRule) BuyCost(notional, commissionRate shared.Decimal) shared.Decimal {
	if r.Fee == nil {
		return notional
	}
	return notional.Add(r.Fee(SideBuy, notional, commissionRate))
}

// SnapPrice 把限价对齐到场所的价位表：按价格所在档位的步长取整，买入向上、卖出向下。没有价位表的场所原样返回
// （它们的限价已经按标的的固定步长取整）。
func (r ExchangeRule) SnapPrice(price shared.Decimal, side Side) shared.Decimal {
	if r.PriceTick == nil || price.Cmp(shared.Zero()) <= 0 {
		return price
	}
	tick := r.PriceTick(price)
	if tick.Cmp(shared.Zero()) <= 0 {
		return price
	}
	floored := shared.FloorToStep(price, tick)
	if side == SideBuy && floored.Cmp(price) != 0 {
		return floored.Add(tick)
	}
	return floored
}

// MarketsWhere 按固定顺序返回场所里规则满足条件的市场，例如这个场所有 T+1 的市场。
func MarketsWhere[E ~string](name E, match func(ExchangeRule) bool) []MarketType {
	var result []MarketType
	for _, market := range marketOrder {
		if match(RuleAt(name, market)) {
			result = append(result, market)
		}
	}
	return result
}
