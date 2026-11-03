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
}

var marketRules = map[MarketType]MarketRule{
	MarketTypeSpot: {PositionFromBalance: true},
	MarketTypeSwap: {
		UsesMargin: true, CanShort: true, ReduceOnly: true, Leverage: true,
		ContractValue: true, PositionSide: PositionSideNet,
	},
	MarketTypeStock: {PositionFromBalance: true, RawInstrumentID: true},
}

// RuleFor 返回市场规则，未知市场返回零值。数据库记录里的市场是字符串，所以接受两种类型。
func RuleFor[T ~string](market T) MarketRule {
	return marketRules[MarketType(market)]
}
