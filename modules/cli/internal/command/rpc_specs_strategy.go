package command

// 策略（StrategyMgr）：定义、实例、结果、历史回放、模板与展示设置。

const strategyMgr = "trpc.moox.strategy.StrategyMgr"

// 策略服务按请求头里的空间 ID 取元数据，所有命令都带 --space-id（默认 crypto）。
func init() {
	rpcGroupShort["strategy"] = "策略定义、实例、结果、历史回放与模板（调用 StrategyMgr）"
	rpcGroupShort["strategy instance"] = "策略实例"
	rpcGroupShort["strategy result"] = "策略每期结果"
	rpcGroupShort["strategy target"] = "策略当前目标权重"
	rpcGroupShort["strategy replay"] = "策略服务里的历史回放（逐期因子 → 策略 → 纸面成交的完整回放用顶层 replay 命令）"
	rpcGroupShort["strategy template"] = "策略模板"
	rpcGroupShort["strategy display"] = "策略实例的展示设置"

	createInstanceFlags := []rpcFlag{
		reqFlag("strategy-id", "instance.strategy_id", "策略定义 ID"),
		reqFlag("factor-task-id", "instance.factor_task_id", "关联的因子计算任务（只能选本空间里运行中、还没被别的实例关联的任务；创建后不能改）"),
		strFlag("logical-account-id", "instance.logical_account_id", "挂载的逻辑账户；不挂＝观察实例，只算目标不下单"),
		strFlag("instance-id", "instance.instance_id", "实例 ID；不给由服务端生成，重试创建时带上同一个 ID 保持幂等"),
		boolFlag("enable", "instance.enabled", "创建后立即启用；不加则创建后停用，确认无误再 enable"),
	}
	updateInstanceFlags := []rpcFlag{
		reqFlag("instance-id", "instance.instance_id", "实例 ID"),
		reqFlag("strategy-id", "instance.strategy_id", "改绑到这个策略定义"),
		strFlag("logical-account-id", "instance.logical_account_id", "挂载的逻辑账户；留空表示观察实例（整体替换）"),
		strFlag("factor-task-id", "instance.factor_task_id", "可省略；给了就必须与现有的一致（因子任务创建时写定）"),
	}
	registerRPCSpecs(
		rpcSpec{Path: []string{"strategy", "list"}, Short: "列出策略定义", Service: strategyMgr, Method: "ListStrategies", Page: rpcPagePSize},
		rpcSpec{Path: []string{"strategy", "show"}, Short: "查看一个策略定义", Service: strategyMgr, Method: "GetStrategy",
			Flags: []rpcFlag{reqFlag("strategy-id", "strategy_id", "策略 ID")}},
		rpcSpec{Path: []string{"strategy", "validate"}, Short: "试算策略 DSL（不落库）", Service: strategyMgr, Method: "ValidateStrategy",
			Flags: []rpcFlag{fileFlag("dsl-file", "dsl_yaml", "策略 DSL YAML 文件；- 为标准输入", true), strFlag("factor-task-id", "factor_task_id", "用这个因子任务校验列引用")}},
		rpcSpec{Path: []string{"strategy", "create"}, Short: "创建策略定义（DSL 写在 --dsl-file 里，写法见 skill 的 strategy-dsl）", Service: strategyMgr, Method: "CreateStrategy",
			Flags: []rpcFlag{fileFlag("dsl-file", "strategy.dsl_yaml", "策略 DSL YAML 文件；- 为标准输入", true), strFlag("strategy-id", "strategy.strategy_id", "策略 ID；不给由服务端生成"), strFlag("name", "strategy.name", "策略名称")}},
		rpcSpec{Path: []string{"strategy", "update"}, Short: "修改策略定义（引用它的实例要先停用）", Service: strategyMgr, Method: "UpdateStrategy",
			Flags: []rpcFlag{reqFlag("strategy-id", "strategy_id", "策略 ID"), fileFlag("dsl-file", "dsl_yaml", "策略 DSL YAML 文件；- 为标准输入", true)}},
		rpcSpec{Path: []string{"strategy", "delete"}, Short: "删除策略定义（默认只预览，--yes 才执行）", Service: strategyMgr, Method: "DeleteStrategy", Destructive: true,
			Flags: []rpcFlag{reqFlag("strategy-id", "strategy_id", "策略 ID")}},

		rpcSpec{Path: []string{"strategy", "instance", "list"}, Short: "列出策略实例（--space-id 过滤空间，默认 crypto）", Service: strategyMgr, Method: "ListStrategyInstances", Page: rpcPagePSize, SpaceField: "space_id",
			Flags: []rpcFlag{strFlag("strategy-id", "strategy_id", "只列这个策略的实例"), boolFlag("enabled", "enabled", "按启用状态过滤（--enabled=false 只列停用的）")}},
		rpcSpec{Path: []string{"strategy", "instance", "show"}, Short: "查看一个策略实例", Service: strategyMgr, Method: "GetStrategyInstance",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},
		rpcSpec{Path: []string{"strategy", "instance", "create"}, Short: "创建策略实例（默认停用，确认无误再 enable；也可加 --enable 创建即启用）", Service: strategyMgr, Method: "CreateStrategyInstance", SpaceField: "instance.space_id", Flags: createInstanceFlags},
		rpcSpec{Path: []string{"strategy", "instance", "update"}, Short: "修改停用中的策略实例绑定的策略定义与账户（因子任务创建时写定，不能改）", Service: strategyMgr, Method: "UpdateStrategyInstance", SpaceField: "instance.space_id",
			Flags: updateInstanceFlags},
		rpcSpec{Path: []string{"strategy", "instance", "enable"}, Destructive: true, Short: "启用策略实例（挂了账户的实例启用后会按目标下单；默认只预览，--yes 才执行）", Service: strategyMgr, Method: "SetStrategyInstanceEnabled", Fixed: map[string]any{"enabled": true},
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},
		rpcSpec{Path: []string{"strategy", "instance", "disable"}, Destructive: true, Short: "停用策略实例（默认只预览，--yes 才执行）", Service: strategyMgr, Method: "SetStrategyInstanceEnabled", Fixed: map[string]any{"enabled": false},
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},

		rpcSpec{Path: []string{"strategy", "result", "list"}, Short: "列出实例每期的结果", Service: strategyMgr, Method: "ListStrategyResults", Page: rpcPagePSize,
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID"), strFlag("session-id", "session_id", "只看这次启用会话的结果")}},
		rpcSpec{Path: []string{"strategy", "result", "show"}, Short: "查看一期结果", Service: strategyMgr, Method: "GetStrategyResult",
			Flags: []rpcFlag{reqFlag("result-id", "result_id", "结果 ID")}},
		rpcSpec{Path: []string{"strategy", "target", "list"}, Short: "查看实例当前的目标权重", Service: strategyMgr, Method: "ListStrategyTargets",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},

		rpcSpec{Path: []string{"strategy", "replay", "start"}, Short: "开始一次策略历史回放", Service: strategyMgr, Method: "StartReplay",
			Flags: []rpcFlag{strFlag("strategy-id", "strategy_id", "策略 ID"), fileFlag("dsl-file", "dsl_yaml", "不落库的策略 DSL 文件", false), strFlag("factor-task-id", "factor_task_id", "因子计算任务"),
				timeFlag("from", "start_time", "起点（RFC3339）", true), timeFlag("to", "end_time", "终点（RFC3339）", true), {Name: "fee-bps", Field: "fee_bps", Kind: rpcFloat, Usage: "费率（基点）"}, strFlag("instance-id", "instance_id", "沿用这个实例的设置")}},
		rpcSpec{Path: []string{"strategy", "replay", "list"}, Short: "列出策略历史回放", Service: strategyMgr, Method: "ListReplays", Page: rpcPagePSize},
		rpcSpec{Path: []string{"strategy", "replay", "show"}, Short: "查看一次回放", Service: strategyMgr, Method: "GetReplay",
			Flags: []rpcFlag{reqFlag("replay-id", "replay_id", "回放 ID")}},
		rpcSpec{Path: []string{"strategy", "replay", "bars"}, Short: "查看回放逐期明细", Service: strategyMgr, Method: "ListReplayBars", Page: rpcPagePSize,
			Flags: []rpcFlag{reqFlag("replay-id", "replay_id", "回放 ID"), boolFlag("brief", "brief", "只返回摘要")}},
		rpcSpec{Path: []string{"strategy", "replay", "cancel"}, Short: "取消回放", Service: strategyMgr, Method: "CancelReplay",
			Flags: []rpcFlag{reqFlag("replay-id", "replay_id", "回放 ID")}},

		rpcSpec{Path: []string{"strategy", "template", "list"}, Short: "列出策略模板", Service: strategyMgr, Method: "ListStrategyTemplates"},
		rpcSpec{Path: []string{"strategy", "template", "show"}, Short: "查看一个模板", Service: strategyMgr, Method: "GetStrategyTemplate",
			Flags: []rpcFlag{reqFlag("template-id", "template_id", "模板 ID")}},
		rpcSpec{Path: []string{"strategy", "template", "save"}, Short: "保存模板", Service: strategyMgr, Method: "SaveStrategyTemplate",
			Flags: []rpcFlag{fileFlag("template-file", "template_yaml", "模板 YAML 文件；- 为标准输入", true)}},
		rpcSpec{Path: []string{"strategy", "template", "delete"}, Short: "删除模板（默认只预览，--yes 才执行）", Service: strategyMgr, Method: "DeleteStrategyTemplate", Destructive: true,
			Flags: []rpcFlag{reqFlag("template-id", "template_id", "模板 ID")}},
		rpcSpec{Path: []string{"strategy", "template", "render"}, Short: "用变量渲染模板，得到策略 DSL", Service: strategyMgr, Method: "RenderStrategyTemplate",
			Flags: []rpcFlag{reqFlag("template-id", "template_id", "模板 ID"), mapFlag("var", "variables", "模板变量 名=值，可重复"), strFlag("params-csv", "params_csv", "参数 CSV")}},

		rpcSpec{Path: []string{"strategy", "display", "get"}, Short: "查看实例的展示设置", Service: strategyMgr, Method: "GetStrategyDisplaySettings",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},
		rpcSpec{Path: []string{"strategy", "display", "set"}, Short: "设置实例要隐藏的分类、成员与列", Service: strategyMgr, Method: "SetStrategyDisplaySettings",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID"), jsonFlag("hidden", "hidden", `隐藏项 JSON，如 '{"categories":[],"members":[],"columns":[]}'`)}},
	)
}
