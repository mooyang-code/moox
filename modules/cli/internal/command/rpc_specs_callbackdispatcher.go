package command

// 事件中心（CallbackDispatcherService）：事件规则与执行记录。事件中心是全局服务，不分空间。
// 换通知地址的验证密钥没有专用接口：删掉规则重建即可（密钥只在创建时显示一次）。

const callbackDispatcher = "trpc.moox.callbackdispatcher.CallbackDispatcherService"

func init() {
	rpcGroupShort["event"] = "事件中心：事件规则（事件发生时通知你的接口）与执行记录"
	rpcGroupShort["event rules"] = "事件规则"
	rpcGroupShort["event executions"] = "执行记录"

	registerRPCSpecs(
		rpcSpec{Path: []string{"event", "catalog"}, Short: "列出可以配置规则的事件，以及每个事件可限定的范围字段", Service: callbackDispatcher, Method: "ListSubscribableEvents", NoSpace: true},

		rpcSpec{Path: []string{"event", "rules", "list"}, Short: "列出事件规则", Service: callbackDispatcher, Method: "ListEventRules", NoSpace: true, Page: rpcPageSize},
		rpcSpec{Path: []string{"event", "rules", "get"}, Short: "查看一条事件规则", Service: callbackDispatcher, Method: "GetEventRule", NoSpace: true,
			Flags: []rpcFlag{reqFlag("rule-id", "rule_id", "规则 ID")}},
		rpcSpec{Path: []string{"event", "rules", "create"}, Short: "新建通知地址规则（验证密钥只在返回里显示这一次，不要记录到对话或日志里）", Service: callbackDispatcher, Method: "CreateEventRule", NoSpace: true,
			Flags: []rpcFlag{reqFlag("name", "name", "规则名称"), reqFlag("event-name", "event_name", "触发事件，见 event catalog"),
				jsonFlag("scope", "scope", `限定范围，JSON 数组，如 [{"field":"exchange","values":["binance"]}]；不填表示不限`),
				reqFlag("notify-url", "notify_url", "通知地址（http 或 https）"), boolFlag("allow-private", "allow_private_address", "允许通知地址解析到内网地址"),
				boolFlag("enabled", "enabled", "是否启用（不填默认启用）")}},
		rpcSpec{Path: []string{"event", "rules", "update"}, Short: "修改事件规则，只改给出的字段（系统规则只能改 --enabled）", Service: callbackDispatcher, Method: "UpdateEventRule", NoSpace: true,
			Flags: []rpcFlag{reqFlag("rule-id", "rule_id", "规则 ID"), strFlag("name", "name", "规则名称"),
				jsonFlag("scope", "scope.items", "限定范围，JSON 数组（空数组表示不限）"),
				strFlag("notify-url", "notify_url", "通知地址"), boolFlag("allow-private", "allow_private_address", "允许通知地址解析到内网地址"),
				boolFlag("enabled", "enabled", "启用状态（--enabled=false 停用）")}},
		rpcSpec{Path: []string{"event", "rules", "delete"}, Destructive: true, Short: "删除事件规则（默认只预览，--yes 才执行；系统规则不能删除）", Service: callbackDispatcher, Method: "DeleteEventRule", NoSpace: true,
			Flags: []rpcFlag{reqFlag("rule-id", "rule_id", "规则 ID")}},
		rpcSpec{Path: []string{"event", "rules", "test"}, Short: "用一条示例事件向通知地址发一次，返回状态码和耗时（不写执行记录）", Service: callbackDispatcher, Method: "TestEventRule", NoSpace: true,
			Flags: []rpcFlag{reqFlag("rule-id", "rule_id", "规则 ID")}},

		rpcSpec{Path: []string{"event", "executions", "list"}, Short: "列出执行记录", Service: callbackDispatcher, Method: "ListEventExecutions", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("rule-id", "rule_id", "按规则过滤"), strFlag("status", "status", "按状态过滤：EXECUTION_STATUS_PENDING、RUNNING、SUCCEEDED、FAILED、CANCELED")}},
		rpcSpec{Path: []string{"event", "executions", "get"}, Short: "查看一条执行记录（每次尝试的状态码、耗时；下架处理的各轮步骤）", Service: callbackDispatcher, Method: "GetEventExecution", NoSpace: true,
			Flags: []rpcFlag{reqFlag("execution-id", "execution_id", "执行记录 ID")}},
		rpcSpec{Path: []string{"event", "executions", "retry"}, Short: "重新执行一条失败的记录", Service: callbackDispatcher, Method: "RetryEventExecution", NoSpace: true,
			Flags: []rpcFlag{reqFlag("execution-id", "execution_id", "执行记录 ID")}},
		rpcSpec{Path: []string{"event", "executions", "cancel"}, Destructive: true, Short: "取消还没结束的执行；对下架处理同时解除禁止买入（默认只预览，--yes 才执行）", Service: callbackDispatcher, Method: "CancelEventExecution", NoSpace: true,
			Flags: []rpcFlag{reqFlag("execution-id", "execution_id", "执行记录 ID")}},
		rpcSpec{Path: []string{"event", "executions", "ack"}, Short: "确认失败的记录已处理，清除它引起的告警", Service: callbackDispatcher, Method: "AcknowledgeEventExecution", NoSpace: true,
			Flags: []rpcFlag{reqFlag("execution-id", "execution_id", "执行记录 ID")}},
	)
}
