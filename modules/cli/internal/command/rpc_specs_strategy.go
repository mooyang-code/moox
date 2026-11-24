package command

import (
	"context"
	"errors"
	"fmt"
)

// 策略（StrategyMgr）：定义、实例、结果、历史回放与展示设置。

const strategyMgr = "trpc.moox.strategy.StrategyMgr"

// 策略服务按请求头里的空间 ID 取元数据，所有命令都带 --space-id（默认 crypto）。
func init() {
	rpcGroupShort["strategy"] = "策略定义、实例、结果、历史回放与展示设置（调用 StrategyMgr）"
	rpcGroupShort["strategy instance"] = "策略实例"
	rpcGroupShort["strategy result"] = "策略每期结果"
	rpcGroupShort["strategy target"] = "策略当前目标权重"
	rpcGroupShort["strategy replay"] = "策略服务里的历史回放（因子数据读结果 View 或由因子引擎现算）"
	rpcGroupShort["strategy display"] = "策略实例的展示设置"

	createInstanceFlags := []rpcFlag{
		reqFlag("strategy-id", "instance.strategy_id", "策略定义 ID"),
		mapFlag("factor-task", "instance.factor_tasks", "关联的因子计算任务，写成 市场=任务 ID，可重复：现货、永续策略各给一个（spot=…、swap=…，两个市场都用时两个都要给），股票策略 equity=…；DSL 写了 market.reference 时再给参考市场的任务（如 spot=…）；DSL 写了 sources 时按 来源名:市场=任务 ID 给每个来源绑定任务（如 daily:swap=…，市场与主帧的任务一一对应）；任务要是本空间里运行中的任务（启用时核对），一个任务可以被多个实例引用；DSL 写了 factor_params 的实例启用时独占关联主帧任务：创建时不检查，主帧任务已关联到别的实例时启用被拒；创建后不能改"),
		strFlag("logical-account-id", "instance.logical_account_id", "挂载的逻辑账户；不挂＝观察实例，只算目标不下单"),
		strFlag("instance-id", "instance.instance_id", "实例 ID；不给由服务端生成，重试创建时带上同一个 ID 保持幂等"),
		boolFlag("enable", "instance.enabled", "创建后立即启用；不加则创建后停用，确认无误再 enable"),
	}
	updateInstanceFlags := []rpcFlag{
		reqFlag("instance-id", "instance.instance_id", "实例 ID"),
		reqFlag("strategy-id", "instance.strategy_id", "改绑到这个策略定义"),
		strFlag("logical-account-id", "instance.logical_account_id", "挂载的逻辑账户；留空表示观察实例（整体替换）"),
		mapFlag("factor-task", "instance.factor_tasks", "可省略；给了就必须与现有的一致（因子任务创建时写定，含参考市场与 来源名:市场 的任务），写法同 create"),
	}
	registerRPCSpecs(
		rpcSpec{Path: []string{"strategy", "list"}, Short: "列出策略定义", Service: strategyMgr, Method: "ListStrategies", Page: rpcPagePSize},
		rpcSpec{Path: []string{"strategy", "show"}, Short: "查看一个策略定义", Service: strategyMgr, Method: "GetStrategy",
			Flags: []rpcFlag{reqFlag("strategy-id", "strategy_id", "策略 ID")}},
		rpcSpec{Path: []string{"strategy", "validate"}, Short: "试算策略 DSL（不落库）", Service: strategyMgr, Method: "ValidateStrategy",
			Flags: []rpcFlag{fileFlag("dsl-file", "dsl_yaml", "策略 DSL YAML 文件；- 为标准输入", true), mapFlag("factor-task", "factor_tasks", "用这些因子任务校验列引用并试算，写成 市场=任务 ID，可重复（混合市场策略给 spot= 和 swap= 两个；参考市场的任务按市场给；附加来源写 来源名:市场=任务 ID）")}},
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
		rpcSpec{Path: []string{"strategy", "instance", "enable"}, Destructive: true, Short: "启用策略实例（挂了账户的实例启用后先试运行：目标只记为影子目标、不下单，确认实盘后才下单；默认只预览，--yes 才执行）", Service: strategyMgr, Method: "SetStrategyInstanceEnabled", Fixed: map[string]any{"enabled": true},
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},
		rpcSpec{Path: []string{"strategy", "instance", "confirm"}, Destructive: true, Merge: pinConfirmSession, Short: "确认试运行中的实例转为实盘：下一期起按目标下单（先用 trade shadow list 看影子目标；策略池预热没完成时被拒；默认只预览，--yes 才执行）", Service: strategyMgr, Method: "ConfirmStrategyInstance",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID"), strFlag("session-id", "session_id", "只确认这次启用的会话（实例之后又重新启用过就拒绝）；不给时取实例当前的会话，预览里能看到它")}},
		rpcSpec{Path: []string{"strategy", "instance", "disable"}, Destructive: true, Short: "停用策略实例；对已停用的实例再执行一次会重新通知 Trade 关闭最近一次挂账户的会话（默认只预览，--yes 才执行）", Service: strategyMgr, Method: "SetStrategyInstanceEnabled", Fixed: map[string]any{"enabled": false},
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},

		rpcSpec{Path: []string{"strategy", "result", "list"}, Short: "列出实例每期的结果", Service: strategyMgr, Method: "ListStrategyResults", Page: rpcPagePSize,
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID"), strFlag("session-id", "session_id", "只看这次启用会话的结果")}},
		rpcSpec{Path: []string{"strategy", "result", "show"}, Short: "查看一期结果", Service: strategyMgr, Method: "GetStrategyResult",
			Flags: []rpcFlag{reqFlag("result-id", "result_id", "结果 ID")}},
		rpcSpec{Path: []string{"strategy", "target", "list"}, Short: "查看实例当前的目标权重", Service: strategyMgr, Method: "ListStrategyTargets",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},

		rpcSpec{Path: []string{"strategy", "replay", "start"}, Short: "开始一次策略历史回放（支持现货、永续与混合市场）", Service: strategyMgr, Method: "StartReplay",
			Flags: []rpcFlag{strFlag("strategy-id", "strategy_id", "策略 ID"), fileFlag("dsl-file", "dsl_yaml", "不落库的策略 DSL 文件", false),
				mapFlag("factor-task", "factor_tasks", "读取的因子计算任务，写成 市场=任务 ID，可重复：现货、永续策略各给一个（spot=…、swap=…，混合市场两个都要给），股票策略 equity=…；参考市场的任务按市场给，附加来源写 来源名:市场=任务 ID；从实例发起时可省略（读实例关联的任务）"),
				timeFlag("from", "start_time", "起点（RFC3339）", true), timeFlag("to", "end_time", "终点（RFC3339）", true),
				{Name: "spot-fee-bps", Field: "spot_fee_bps", Kind: rpcFloat, Usage: "现货与股票的单边费率（基点）"}, {Name: "swap-fee-bps", Field: "swap_fee_bps", Kind: rpcFloat, Usage: "永续的单边费率（基点）"},
				{Name: "slippage-bps", Field: "slippage_bps", Kind: rpcFloat, Usage: "滑点（基点，0～200）：成交价在收盘价上按方向加减这个比例，买入上浮、卖出下浮；不写为 0"},
				strFlag("funding-column", "funding_column", "永续持仓按 View 里的这一列结算资金费（本根累计的资金费率，例如 funding_rate）；不写不计，策略池写在池 DSL 的 fees.funding_column"),
				strFlag("instance-id", "instance_id", "沿用这个实例当前会话的策略版本与因子任务"),
				strFlag("input", "input", "主帧的因子数据来源：view（默认，读因子任务的结果 View）或 compute（因子现算：经主机网关调因子引擎，按因子机本地 K 线与回放中的参数现算，不受 View 保留根数限制）；声明了 factor_params 的策略必须用 compute；compute 不支持策略池与股票市场")}},
		rpcSpec{Path: []string{"strategy", "replay", "list"}, Short: "列出策略历史回放", Service: strategyMgr, Method: "ListReplays", Page: rpcPagePSize},
		rpcSpec{Path: []string{"strategy", "replay", "show"}, Short: "查看一次回放", Service: strategyMgr, Method: "GetReplay",
			Flags: []rpcFlag{reqFlag("replay-id", "replay_id", "回放 ID")}},
		rpcSpec{Path: []string{"strategy", "replay", "bars"}, Short: "查看回放逐期明细", Service: strategyMgr, Method: "ListReplayBars", Page: rpcPagePSize,
			Flags: []rpcFlag{reqFlag("replay-id", "replay_id", "回放 ID"), boolFlag("brief", "brief", "只返回摘要")}},
		rpcSpec{Path: []string{"strategy", "replay", "cancel"}, Short: "取消回放", Service: strategyMgr, Method: "CancelReplay",
			Flags: []rpcFlag{reqFlag("replay-id", "replay_id", "回放 ID")}},

		rpcSpec{Path: []string{"strategy", "display", "get"}, Short: "查看实例的展示设置", Service: strategyMgr, Method: "GetStrategyDisplaySettings",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID")}},
		rpcSpec{Path: []string{"strategy", "display", "set"}, Short: "设置实例要隐藏的分类、成员与列", Service: strategyMgr, Method: "SetStrategyDisplaySettings",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID"), jsonFlag("hidden", "hidden", `隐藏项 JSON，如 '{"categories":[],"members":[],"columns":[]}'`)}},
	)
}

// pinConfirmSession 在确认实盘的请求没给 session_id 时取回实例当前的会话填进去：预览里能看到要确认的是哪一次启用，
// 执行时也只确认这一次（实例之后又重新启用过就被拒绝）。把预览里的会话号带进 --session-id 再加 --yes 最稳妥。
func pinConfirmSession(ctx context.Context, inv rpcInvocation, request map[string]any) error {
	if session, ok := lookupRequestString(request, "session_id"); ok && session != "" {
		return nil
	}
	instance, err := fetchObject(ctx, inv, "GetStrategyInstance", map[string]any{"instance_id": request["instance_id"]}, "instance")
	if err != nil {
		return fmt.Errorf("读取实例的当前会话：%w", err)
	}
	session, _ := instance["session_id"].(string)
	if session == "" {
		return errors.New("实例没有运行会话（没有启用）：先启用实例")
	}
	request["session_id"] = session
	return nil
}
