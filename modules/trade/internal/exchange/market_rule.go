package exchange

// MarketRule 是一个市场类型的交易规则。现货、合约、股票的差异都放在这里，
// 业务代码按规则运行，不再判断具体的市场类型。
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
	// RawInstrumentID 表示标的 ID 原样使用，不拼市场后缀，如 600519.XSHG。
	RawInstrumentID bool
	// BoardLot 表示按整手交易：买入不足一手的差额视为已满足；卖出时余下不足一手的零股可以一次卖完。
	// 加密货币“数量低于交易所最小值”的判断不受影响。
	BoardLot bool
	// PriceLimits 表示有涨跌停（绝对价，每次取自报价）和停牌：下单价截在涨跌停价之间，涨停买、跌停卖、停牌顺延。
	PriceLimits bool
	// T1 表示当日买入的数量次日才能卖出（按北京时间的自然日）。
	T1 bool
	// StockFees 表示费用按 A 股规则算（佣金最低 5 元、卖出收印花税、双向过户费），见 StockFee。
	StockFees bool
	// PriceCageTicks 是价格笼子的最小价位数：比例带（MaxBuyPriceRatio、MinSellPriceRatio）和这么多个最小价位取更宽的那个；0 表示只有比例带。
	PriceCageTicks int
}

var marketRules = map[MarketType]MarketRule{
	MarketTypeSpot: {PositionFromBalance: true},
	MarketTypeSwap: {
		UsesMargin: true, CanShort: true, ReduceOnly: true, Leverage: true,
		ContractValue: true, PositionSide: PositionSideNet,
	},
	MarketTypeStock: {
		PositionFromBalance: true, RawInstrumentID: true,
		BoardLot: true, PriceLimits: true, T1: true, StockFees: true, PriceCageTicks: 10,
	},
}

// RuleFor 返回市场规则，未知市场返回零值。数据库记录里的市场是字符串，所以接受两种类型。
func RuleFor[T ~string](market T) MarketRule {
	return marketRules[MarketType(market)]
}
