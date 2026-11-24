package command

import "context"

// 交易（TradeConsoleService）写操作：账户与组合的创建、修改，杠杆，成员，暂停与恢复，人工下单、撤单、清仓，模拟账户的开关。
// 会改动账户或发出订单的命令都默认只预览，--yes 才执行；对实盘账户执行前要先得到用户的明确同意。

// mergeLogicalSettings 取回组合账户现有的 key（order_settings 或 fund_settings），把用户给的字段合并进 settings：
// 这两个修改接口整体替换设置，只给一个字段会把其余设置清空。参数里没有的设置项用 --data 的 settings 对象给出，同样与已有设置合并。
func mergeLogicalSettings(key string) rpcMerge {
	return mergeExisting("settings", nil, func(ctx context.Context, inv rpcInvocation, request map[string]any) (map[string]any, error) {
		id, _ := lookupRequest(request, "logical_account_id")
		account, err := fetchObject(ctx, inv, "GetLogicalAccount", map[string]any{"logical_account_id": id}, "logical_account")
		if err != nil {
			return nil, err
		}
		settings, _ := account[key].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		return settings, nil
	})
}

func init() {
	accountID := reqFlag("account-id", "trading_account_id", "交易账户 ID")
	logicalID := reqFlag("logical-account-id", "logical_account_id", "逻辑账户 ID")
	reason := strFlag("reason", "reason", "原因（写进操作记录）")
	registerRPCSpecs(
		// ---- 交易账户 ----
		rpcSpec{Path: []string{"trade", "account", "create"}, Destructive: true, Short: "新建实盘或测试网交易账户（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "CreateTradingAccount",
			Flags: []rpcFlag{reqFlag("name", "name", "账户名称"), reqFlag("exchange", "exchange", "交易所（EXCHANGE_BINANCE 或 EXCHANGE_OKX）"),
				jsonFlag("markets", "markets", "开通的市场，JSON 数组，如 [\"MARKET_TYPE_SPOT\",\"MARKET_TYPE_SWAP\"]"), strFlag("settlement-asset", "settlement_asset", "结算资产，默认 USDT"),
				strFlag("margin-mode", "margin_mode", "保证金模式（CROSS 或 ISOLATED）"), strFlag("environment", "live.environment", "环境（ACCOUNT_ENVIRONMENT_PRODUCTION 或 ACCOUNT_ENVIRONMENT_TESTNET）"),
				strFlag("credential-secret-id", "live.credential_secret_id", "密钥管理里的交易所密钥 ID")}},
		rpcSpec{Path: []string{"trade", "account", "update"}, Destructive: true, Short: "修改交易账户：只改给出的字段（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "UpdateTradingAccount",
			Flags: []rpcFlag{accountID, strFlag("name", "name", "账户名称"), strFlag("credential-secret-id", "credential_secret_id", "换用的交易所密钥 ID"),
				strFlag("settlement-asset", "settlement_asset", "结算资产"), strFlag("margin-mode", "margin_mode", "保证金模式（CROSS 或 ISOLATED）"),
				strFlag("status", "status", "状态（ENABLED 或 DISABLED）"), intFlag("order-retention-days", "order_retention_days", "模拟账户订单与成交明细保留天数（0 不清理，1～365）")}},
		rpcSpec{Path: []string{"trade", "account", "leverage"}, Destructive: true, Short: "设置杠杆（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "SetLeverage",
			Flags: []rpcFlag{accountID, reqFlag("instrument-id", "instrument_id", "标的 ID；* 表示账户默认值"), reqFlag("leverage", "leverage", "杠杆倍数，如 1、3")}},
		rpcSpec{Path: []string{"trade", "account", "sync"}, Short: "立即同步交易账户的余额与持仓", Service: tradeConsole, Method: "SyncTradingAccount", Flags: []rpcFlag{accountID}},
		rpcSpec{Path: []string{"trade", "account", "reinspect"}, Short: "重新检测交易账户的类型与权限", Service: tradeConsole, Method: "ReinspectTradingAccount", Flags: []rpcFlag{accountID}},
		rpcSpec{Path: []string{"trade", "account", "flatten"}, Destructive: true, AutoIDs: []string{"action_id"}, Short: "平掉交易账户的全部持仓（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "FlattenTradingAccount",
			Flags: []rpcFlag{accountID, reason}},

		// ---- 逻辑账户（组合） ----
		rpcSpec{Path: []string{"trade", "logical", "create"}, Destructive: true, Short: "新建逻辑账户（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "CreateLogicalAccount",
			Flags: []rpcFlag{reqFlag("name", "name", "名称"), reqFlag("execution-mode", "execution_mode", "执行模式（EXECUTION_MODE_PAPER、EXECUTION_MODE_TESTNET 或 EXECUTION_MODE_LIVE）"),
				strFlag("settlement-asset", "settlement_asset", "结算资产，默认 USDT"), strFlag("control-mode", "control_mode", "控制方式（CONTROL_MODE_AUTO 或 CONTROL_MODE_MANUAL）")}},
		rpcSpec{Path: []string{"trade", "logical", "update"}, Destructive: true, Short: "修改逻辑账户名称（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "UpdateLogicalAccount",
			Flags: []rpcFlag{logicalID, reqFlag("name", "name", "名称")}},
		rpcSpec{Path: []string{"trade", "logical", "order-settings"}, Destructive: true, Merge: mergeLogicalSettings("order_settings"), Short: "修改组合的下单设置：只改给出的字段，其余保持（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "UpdateLogicalAccountOrderSettings",
			Flags: []rpcFlag{logicalID, strFlag("rebalance-threshold-mode", "settings.rebalance_threshold_mode", "调仓阈值方式：TARGET_RATIO、POSITION_RATIO、EQUITY_RATIO、ALWAYS"),
				strFlag("rebalance-threshold-ratio", "settings.rebalance_threshold_ratio", "调仓阈值比例"), strFlag("min-notional-spot", "settings.min_notional_spot", "现货最小下单金额"),
				strFlag("min-notional-swap", "settings.min_notional_swap", "永续最小下单金额"), strFlag("max-child-notional", "settings.max_child_notional", "单笔子单金额上限"),
				strFlag("order-mode", "settings.order_mode", "下单方式：MARKET 或 PSEUDO_MARKET"), strFlag("slippage-step", "settings.slippage_step", "伪市价单每次加的滑点"),
				strFlag("slippage-max", "settings.slippage_max", "伪市价单滑点上限"), intFlag("batch-interval-seconds", "settings.batch_interval_seconds", "分批下单间隔秒数（0 用默认）")}},
		rpcSpec{Path: []string{"trade", "logical", "fund-settings"}, Destructive: true, Merge: mergeLogicalSettings("fund_settings"), Short: "修改组合的资金设置：只改给出的字段，其余保持（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "UpdateLogicalAccountFundSettings",
			Flags: []rpcFlag{logicalID, strFlag("reserve-ratio", "settings.reserve_ratio", "资金预留比例：换算目标时从权益里扣掉的比例，0.05 表示 5%"),
				strFlag("bnb-reserve-amount", "settings.bnb_reserve_amount", "BNB 保留数量（0 关闭）"), boolFlag("bnb-discount", "settings.bnb_discount", "用 BNB 抵扣手续费"),
				boolFlag("dust-convert", "settings.dust_convert_enabled", "定期把现货零头兑成 BNB")}},
		rpcSpec{Path: []string{"trade", "logical", "add-member"}, Destructive: true, Short: "把交易账户加进组合（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "AddLogicalAccountMember",
			Flags: []rpcFlag{logicalID, accountID, boolFlag("enabled", "enabled", "加入后立即参与执行"), intFlag("priority", "priority", "优先级（小的先下单）"),
				boolFlag("adopt-existing-exposure", "adopt_existing_exposure", "接管账户里已有的持仓")}},
		rpcSpec{Path: []string{"trade", "logical", "remove-member"}, Destructive: true, Short: "把交易账户移出组合（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "RemoveLogicalAccountMember",
			Flags: []rpcFlag{logicalID, accountID}},
		rpcSpec{Path: []string{"trade", "logical", "pause"}, Destructive: true, Short: "暂停组合的自动执行（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "PauseLogicalAccount",
			Flags: []rpcFlag{logicalID, reason}},
		rpcSpec{Path: []string{"trade", "logical", "resume"}, Destructive: true, Short: "恢复组合的自动执行（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "ResumeLogicalAccount",
			Flags: []rpcFlag{logicalID}},
		rpcSpec{Path: []string{"trade", "logical", "flatten"}, Destructive: true, AutoIDs: []string{"action_id"}, Short: "清仓整个组合（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "FlattenLogicalAccount",
			Flags: []rpcFlag{logicalID, reason}},

		// ---- 人工订单 ----
		rpcSpec{Path: []string{"trade", "order", "submit"}, Destructive: true, AutoIDs: []string{"action_id", "client_order_id"}, Short: "人工下单（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "SubmitOrder",
			Flags: []rpcFlag{logicalID, accountID, reqFlag("instrument-id", "instrument_id", "标的 ID"), reqFlag("market-type", "market_type", "市场（MARKET_TYPE_SPOT、MARKET_TYPE_SWAP 或 MARKET_TYPE_STOCK）"),
				reqFlag("side", "side", "方向（ORDER_SIDE_BUY 或 ORDER_SIDE_SELL）"), reqFlag("quantity", "quantity", "数量"),
				strFlag("order-type", "order_type", "订单类型（ORDER_TYPE_MARKET 或 ORDER_TYPE_LIMIT），默认市价"), strFlag("limit-price", "limit_price", "限价单价格"),
				strFlag("fill-policy", "fill_policy", "成交方式（FILL_POLICY_GTC、FILL_POLICY_IOC 等）"), strFlag("position-side", "position_side", "持仓方向（双向持仓才用）"),
				intFlag("deadline-at", "deadline_at", "截止时间（毫秒时间戳）"), reason}},
		rpcSpec{Path: []string{"trade", "order", "cancel"}, Destructive: true, AutoIDs: []string{"action_id"}, Short: "撤单（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "CancelOrder",
			Flags: []rpcFlag{reqFlag("order-id", "order_id", "订单 ID"), reason}},

		// ---- 模拟账户 ----
		rpcSpec{Path: []string{"trade", "paper", "create"}, Destructive: true, Short: "新建模拟账户，并新建或加入一个组合（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "CreatePaperSimulation",
			Flags: []rpcFlag{reqFlag("account-name", "account_name", "模拟账户名称"), strFlag("logical-account-name", "logical_account_name", "新建组合的名称（与 --logical-account-id 二选一）"),
				strFlag("logical-account-id", "logical_account_id", "加入已有组合"), reqFlag("exchange", "exchange", "模拟的交易所（EXCHANGE_BINANCE 或 EXCHANGE_OKX）"),
				jsonFlag("markets", "markets", "开通的市场，JSON 数组，如 [\"MARKET_TYPE_SWAP\"]"), strFlag("settlement-asset", "settlement_asset", "结算资产，默认 USDT"),
				strFlag("margin-mode", "margin_mode", "保证金模式（CROSS 或 ISOLATED）"), reqFlag("initial-balance", "initial_balance", "初始资金"),
				strFlag("maker-fee-rate", "maker_fee_rate", "挂单费率"), strFlag("taker-fee-rate", "taker_fee_rate", "吃单费率"), strFlag("slippage-bps", "slippage_bps", "滑点（基点）"),
				strFlag("control-mode", "control_mode", "控制方式（CONTROL_MODE_AUTO 或 CONTROL_MODE_MANUAL）"), intFlag("priority", "priority", "在组合里的优先级"),
				intFlag("order-retention-days", "order_retention_days", "订单与成交明细保留天数（0 不清理，默认 5）")}},
		rpcSpec{Path: []string{"trade", "paper", "close"}, Destructive: true, Short: "关闭模拟账户（默认只预览，--yes 才执行）", Service: tradeConsole, Method: "ClosePaperSimulation", Flags: []rpcFlag{accountID}},
	)
}
