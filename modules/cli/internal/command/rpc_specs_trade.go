package command

// 交易（TradeConsoleService）只读查询。交易模块尚未完工：账户创建与修改、下单、撤单、平仓、减仓、纸面模拟、下架名单的写入与解除等写操作不提供命令，
// Agent 也不要代用户创建实盘账户、下单、清仓或解除下架禁止。

const tradeConsole = "trpc.moox.trade.TradeConsoleService"

func init() {
	rpcGroupShort["trade"] = "交易：只读查询（账户、订单、成交、持仓、权益、收益拆解、操作记录、下架名单）；写操作不提供命令"
	rpcGroupShort["trade account"] = "交易账户"
	rpcGroupShort["trade logical"] = "逻辑账户"
	rpcGroupShort["trade order"] = "订单"
	rpcGroupShort["trade blocked"] = "下架名单"

	registerRPCSpecs(
		rpcSpec{Path: []string{"trade", "account", "list"}, Short: "列出交易账户", Service: tradeConsole, Method: "ListTradingAccounts", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("exchange", "exchange", "按交易所过滤"), strFlag("market-type", "market_type", "按已开通的市场过滤（写 MARKET_TYPE_SPOT、MARKET_TYPE_SWAP 或 MARKET_TYPE_STOCK）"), strFlag("execution-mode", "execution_mode", "按执行模式过滤"), strFlag("status", "status", "按状态过滤")}},
		rpcSpec{Path: []string{"trade", "account", "show"}, Short: "查看交易账户", Service: tradeConsole, Method: "GetTradingAccount",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID")}},
		rpcSpec{Path: []string{"trade", "inspect-credential"}, Short: "检测一把交易所密钥的账户类型和权限（只读，不落库，不下单）", Service: tradeConsole, Method: "InspectExchangeCredential",
			Flags: []rpcFlag{reqFlag("exchange", "exchange", "交易所（EXCHANGE_BINANCE 或 EXCHANGE_OKX）"), reqFlag("environment", "environment", "环境（ACCOUNT_ENVIRONMENT_PRODUCTION 或 ACCOUNT_ENVIRONMENT_TESTNET）"),
				reqFlag("credential-secret-id", "credential_secret_id", "密钥管理里的交易所密钥 ID"), jsonFlag("markets", "markets", "要开通的市场，JSON 数组，如 [\"MARKET_TYPE_SPOT\",\"MARKET_TYPE_SWAP\"]；不填时只拦和市场无关的问题，和市场有关的（双向持仓、缺万向划转权限等）写在提醒里")}},
		rpcSpec{Path: []string{"trade", "logical", "list"}, Short: "列出逻辑账户", Service: tradeConsole, Method: "ListLogicalAccounts", Page: rpcPageSize},
		rpcSpec{Path: []string{"trade", "logical", "show"}, Short: "查看逻辑账户", Service: tradeConsole, Method: "GetLogicalAccount",
			Flags: []rpcFlag{reqFlag("logical-account-id", "logical_account_id", "逻辑账户 ID")}},
		rpcSpec{Path: []string{"trade", "logical", "target"}, Short: "查看逻辑账户当前的目标", Service: tradeConsole, Method: "GetLogicalAccountTarget",
			Flags: []rpcFlag{reqFlag("logical-account-id", "logical_account_id", "逻辑账户 ID")}},
		rpcSpec{Path: []string{"trade", "order", "list"}, Short: "列出订单", Service: tradeConsole, Method: "ListOrders", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("logical-account-id", "logical_account_id", "逻辑账户 ID"), strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("instrument-id", "instrument_id", "合约或标的"),
				strFlag("state", "state", "订单状态"), strFlag("owner-type", "owner_type", "按订单来源过滤：TARGET（策略目标）、OPERATOR（人工操作）、EXTERNAL（外部）、SYSTEM（BNB 补买、稳定币调平）"),
				boolFlag("only-open", "only_open", "只列未完成订单"), intFlag("start-time", "start_time", "起点（毫秒时间戳）"), intFlag("end-time", "end_time", "终点（毫秒时间戳）")}},
		rpcSpec{Path: []string{"trade", "order", "show"}, Short: "查看订单", Service: tradeConsole, Method: "GetOrder",
			Flags: []rpcFlag{reqFlag("order-id", "order_id", "订单 ID")}},
		rpcSpec{Path: []string{"trade", "fills"}, Short: "列出成交", Service: tradeConsole, Method: "ListFills", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("order-id", "order_id", "订单 ID"), strFlag("instrument-id", "instrument_id", "合约或标的"),
				intFlag("start-time", "start_time", "起点（毫秒时间戳）"), intFlag("end-time", "end_time", "终点（毫秒时间戳）")}},
		rpcSpec{Path: []string{"trade", "positions"}, Short: "列出持仓", Service: tradeConsole, Method: "ListPositions",
			Flags: []rpcFlag{strFlag("logical-account-id", "logical_account_id", "逻辑账户 ID"), strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("instrument-id", "instrument_id", "合约或标的")}},
		rpcSpec{Path: []string{"trade", "holdings"}, Short: "列出账户的持仓明细", Service: tradeConsole, Method: "ListHoldings",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID")}},
		rpcSpec{Path: []string{"trade", "ledger"}, Short: "查询交易账户的资金流水（手续费、资金费、已实现盈亏、划转）", Service: tradeConsole, Method: "ListAccountLedger",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID"), intFlag("start-time", "start_time", "起点（毫秒时间戳，默认 7 天前）"),
				intFlag("end-time", "end_time", "终点（毫秒时间戳，默认现在）"), intFlag("limit", "limit", "最多条数（默认 200，最多 1000）"),
				strFlag("market-type", "market_type", "要查流水的市场（写 MARKET_TYPE_SPOT 或 MARKET_TYPE_SWAP）；账户开通多个市场且都支持流水时必填")}},
		rpcSpec{Path: []string{"trade", "equity"}, Short: "查询权益曲线", Service: tradeConsole, Method: "QueryEquityCurve",
			Flags: []rpcFlag{strFlag("account-id", "trading_account_id", "交易账户 ID"), strFlag("logical-account-id", "logical_account_id", "逻辑账户 ID"),
				intFlag("start-time", "start_time", "起点（毫秒时间戳）"), intFlag("end-time", "end_time", "终点（毫秒时间戳）")}},
		rpcSpec{Path: []string{"trade", "capabilities"}, Short: "查看交易账户的执行能力", Service: tradeConsole, Method: "GetExecutionCapabilities",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID")}},
		rpcSpec{Path: []string{"trade", "instruments"}, Short: "列出交易账户可下单的标的", Service: tradeConsole, Method: "ListTradableInstruments",
			Flags: []rpcFlag{reqFlag("account-id", "trading_account_id", "交易账户 ID")}},
		rpcSpec{Path: []string{"trade", "action"}, Short: "查看一次操作的执行结果", Service: tradeConsole, Method: "GetOperatorAction",
			Flags: []rpcFlag{reqFlag("action-id", "action_id", "操作 ID")}},
		rpcSpec{Path: []string{"trade", "actions"}, Short: "列出组合的人工操作记录（人工单、撤单、清仓、减仓、下架清仓）", Service: tradeConsole, Method: "ListOperatorActions", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("logical-account-id", "logical_account_id", "逻辑账户 ID"), strFlag("action-type", "action_type", "按动作类型过滤：MANUAL_ORDER、SUBMIT_ORDER、CANCEL_ORDER、FLATTEN、REDUCE、DELIST_FLATTEN"),
				intFlag("start-time", "start_time", "起点（毫秒时间戳）"), intFlag("end-time", "end_time", "终点（毫秒时间戳）")}},
		rpcSpec{Path: []string{"trade", "pnl"}, Short: "收益拆解：手续费、已实现盈亏（成交表汇总）与资金费（交易所流水，时间范围最长 90 天，取不全时标明截断）", Service: tradeConsole, Method: "SummarizeAccountPnl",
			Flags: []rpcFlag{strFlag("logical-account-id", "logical_account_id", "逻辑账户 ID（与 --account-id 二选一）"), strFlag("account-id", "trading_account_id", "交易账户 ID（与 --logical-account-id 二选一）"),
				intFlag("start-time", "start_time", "起点（毫秒时间戳，默认 7 天前）"), intFlag("end-time", "end_time", "终点（毫秒时间戳，默认现在）")}},
		rpcSpec{Path: []string{"trade", "blocked", "list"}, Short: "列出下架名单（全局，名单里的标的只减不增）", Service: tradeConsole, Method: "ListBlockedInstruments"},
		rpcSpec{Path: []string{"trade", "holders"}, Short: "列出策略持有某个标的的执行账户（跨空间，全局；零头和模拟盘有标记）", Service: tradeConsole, Method: "ListInstrumentHolders",
			Flags: []rpcFlag{reqFlag("exchange", "exchange", "交易所（写 EXCHANGE_BINANCE 或 EXCHANGE_OKX）"), reqFlag("market-type", "market_type", "市场（写 MARKET_TYPE_SPOT 或 MARKET_TYPE_SWAP）"),
				reqFlag("exchange-symbol", "exchange_symbol", "交易所代码，例如 FOOUSDT、FOO-USDT-SWAP"), boolFlag("paper-only", "paper_only", "只看模拟盘")}},
	)
}
