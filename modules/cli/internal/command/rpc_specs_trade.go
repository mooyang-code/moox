package command

// 交易（TradeConsoleService）只读查询。交易模块尚未完工：账户创建与修改、下单、撤单、平仓、纸面模拟等写操作不提供命令，
// Agent 也不要代用户创建实盘账户、下单或清仓。

const tradeConsole = "trpc.moox.trade.TradeConsoleService"

func init() {
	rpcGroupShort["trade"] = "交易：只读查询（账户、订单、成交、持仓、权益）；写操作不提供命令"
	rpcGroupShort["trade account"] = "交易账户"
	rpcGroupShort["trade logical"] = "逻辑账户"
	rpcGroupShort["trade order"] = "订单"

	registerRPCSpecs(
		rpcSpec{Path: []string{"trade", "account", "list"}, Short: "列出交易账户", Service: tradeConsole, Method: "ListTradingAccounts", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("exchange", "exchange", "按交易所过滤"), strFlag("market-type", "market_type", "按市场类型过滤"), strFlag("execution-mode", "execution_mode", "按执行模式过滤"), strFlag("status", "status", "按状态过滤")}},
		rpcSpec{Path: []string{"trade", "account", "show"}, Short: "查看交易账户", Service: tradeConsole, Method: "GetTradingAccount",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID")}},
		rpcSpec{Path: []string{"trade", "logical", "list"}, Short: "列出逻辑账户", Service: tradeConsole, Method: "ListLogicalAccounts", Page: rpcPageSize},
		rpcSpec{Path: []string{"trade", "logical", "show"}, Short: "查看逻辑账户", Service: tradeConsole, Method: "GetLogicalAccount",
			Flags: []rpcFlag{reqFlag("logical-account-id", "logical_account_id", "逻辑账户 ID")}},
		rpcSpec{Path: []string{"trade", "logical", "target"}, Short: "查看逻辑账户当前的目标", Service: tradeConsole, Method: "GetLogicalAccountTarget",
			Flags: []rpcFlag{reqFlag("logical-account-id", "logical_account_id", "逻辑账户 ID")}},
		rpcSpec{Path: []string{"trade", "order", "list"}, Short: "列出订单", Service: tradeConsole, Method: "ListOrders", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("logical-account-id", "logical_account_id", "逻辑账户 ID"), strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("instrument-id", "instrument_id", "合约或标的"),
				strFlag("state", "state", "订单状态"), boolFlag("only-open", "only_open", "只列未完成订单"), intFlag("start-time", "start_time", "起点（毫秒时间戳）"), intFlag("end-time", "end_time", "终点（毫秒时间戳）")}},
		rpcSpec{Path: []string{"trade", "order", "show"}, Short: "查看订单", Service: tradeConsole, Method: "GetOrder",
			Flags: []rpcFlag{reqFlag("order-id", "order_id", "订单 ID")}},
		rpcSpec{Path: []string{"trade", "fills"}, Short: "列出成交", Service: tradeConsole, Method: "ListFills", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("order-id", "order_id", "订单 ID"), strFlag("instrument-id", "instrument_id", "合约或标的"),
				intFlag("start-time", "start_time", "起点（毫秒时间戳）"), intFlag("end-time", "end_time", "终点（毫秒时间戳）")}},
		rpcSpec{Path: []string{"trade", "positions"}, Short: "列出持仓", Service: tradeConsole, Method: "ListPositions",
			Flags: []rpcFlag{strFlag("logical-account-id", "logical_account_id", "逻辑账户 ID"), strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("instrument-id", "instrument_id", "合约或标的")}},
		rpcSpec{Path: []string{"trade", "holdings"}, Short: "列出账户的持仓明细", Service: tradeConsole, Method: "ListHoldings",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID")}},
		rpcSpec{Path: []string{"trade", "equity"}, Short: "查询权益曲线", Service: tradeConsole, Method: "QueryEquityCurve",
			Flags: []rpcFlag{strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("logical-account-id", "logical_account_id", "逻辑账户 ID"),
				intFlag("start-time", "start_time", "起点（毫秒时间戳）"), intFlag("end-time", "end_time", "终点（毫秒时间戳）")}},
		rpcSpec{Path: []string{"trade", "capabilities"}, Short: "查看交易账户的执行能力", Service: tradeConsole, Method: "GetExecutionCapabilities",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID")}},
		rpcSpec{Path: []string{"trade", "action"}, Short: "查看一次操作的执行结果", Service: tradeConsole, Method: "GetOperatorAction",
			Flags: []rpcFlag{reqFlag("action-id", "action_id", "操作 ID")}},
	)
}
