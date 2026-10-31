package command

// 运维只读视图：监控、EventBus、主机与部署、主机代理、密钥（只列打码后的信息）。
// 主机与部署的增删、启停走 setup，这里只提供查询。

const (
	monitorMgr   = "trpc.moox.monitor.MonitorMgr"
	eventBusMgr  = "trpc.moox.eventbus.EventBusMgr"
	sysDeploySvc = "trpc.moox.ops.SysDeploy"
	hostAgentSvc = "trpc.moox.hostagent.HostAgentMgr"
	secretMgrSvc = "trpc.moox.ops.SecretMgr"
)

func init() {
	rpcGroupShort["eventbus"] = "EventBus：流、消费者与事件概览（调用 EventBusMgr，只读）"
	rpcGroupShort["secret"] = "密钥：只列清单与打码后的信息；新建、修改请用户在管理台“设置 › 密钥”填写"
	rpcGroupShort["host agent"] = "主机代理：主机资源采集状态（用 --host 指定主机）"

	registerRPCSpecs(
		rpcSpec{Path: []string{"monitor", "health"}, Short: "健康总览：各模块、数据新鲜度与待处理项（排障先看 doctor alerts）", Service: monitorMgr, Method: "GetHealthOverview", SpaceField: "space_id"},
		rpcSpec{Path: []string{"monitor", "arrival"}, Short: "数据到达历史：某天每个周期何时到达", Service: monitorMgr, Method: "QueryPipelineArrivalHistory", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), reqFlag("frequency", "freq", "频率，如 1m"), reqFlag("day", "day", "日期，如 2026-10-28"), strFlag("task-id", "task_id", "采集任务 ID")}},
		rpcSpec{Path: []string{"monitor", "hosts"}, Short: "列出已上报资源的主机代理", Service: monitorMgr, Method: "ListHostAgents", NoSpace: true},
		rpcSpec{Path: []string{"monitor", "host-metrics"}, Short: "主机资源指标的历史曲线", Service: monitorMgr, Method: "QueryHostMetricHistory", NoSpace: true,
			Flags: []rpcFlag{reqFlag("agent-id", "agent_id", "主机代理 ID（monitor hosts 里取）"), timeFlag("from", "start_at", "起点（RFC3339）", false), timeFlag("to", "end_at", "终点（RFC3339）", false), intFlag("limit", "limit", "最多返回多少点")}},

		rpcSpec{Path: []string{"eventbus", "overview"}, Short: "EventBus 概览：连接数、流、消费者与积压", Service: eventBusMgr, Method: "GetOverview", NoSpace: true},
		rpcSpec{Path: []string{"eventbus", "streams"}, Short: "列出流", Service: eventBusMgr, Method: "ListStreams", NoSpace: true, Page: rpcPageSize},
		rpcSpec{Path: []string{"eventbus", "consumers"}, Short: "列出消费者", Service: eventBusMgr, Method: "ListConsumers", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("stream", "stream", "只看这个流的消费者")}},
		rpcSpec{Path: []string{"eventbus", "consumer"}, Short: "查看一个消费者的积压与投递状态", Service: eventBusMgr, Method: "GetConsumer", NoSpace: true,
			Flags: []rpcFlag{reqFlag("stream", "stream", "流名称"), reqFlag("name", "name", "消费者名称")}},
		rpcSpec{Path: []string{"eventbus", "events"}, Short: "列出最近的事件", Service: eventBusMgr, Method: "ListEvents", NoSpace: true, Page: rpcPageSize},

		rpcSpec{Path: []string{"host", "list"}, Short: "列出 Admin 登记的主机（moox.toml 里的候选主机用 setup hosts）", Service: sysDeploySvc, Method: "ListHosts", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("host-id", "host_id", "只看这台主机"), strFlag("status", "status", "按状态过滤")}},
		rpcSpec{Path: []string{"host", "deployments"}, Short: "列出组件部署", Service: sysDeploySvc, Method: "ListDeployments", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("host-id", "host_id", "按主机过滤"), strFlag("component-id", "component_id", "按组件过滤"), strFlag("status", "status", "按状态过滤")}},
		rpcSpec{Path: []string{"host", "routes"}, Short: "查看一台主机的网关路由", Service: sysDeploySvc, Method: "GetHostRoutes", NoSpace: true,
			Flags: []rpcFlag{reqFlag("host-id", "host_id", "主机 ID")}},
		rpcSpec{Path: []string{"host", "catalog"}, Short: "服务目录：组件、端口与服务", Service: sysDeploySvc, Method: "GetCatalog", NoSpace: true},
		rpcSpec{Path: []string{"host", "directory"}, Short: "服务目录的路由视图：每个服务部署在哪些主机", Service: sysDeploySvc, Method: "GetDirectory", NoSpace: true},

		rpcSpec{Path: []string{"host", "agent", "status"}, Short: "主机代理的采集与上报状态", Service: hostAgentSvc, Method: "GetStatus", NoSpace: true, Host: true},
		rpcSpec{Path: []string{"host", "agent", "snapshot"}, Short: "主机代理最近一次的资源快照", Service: hostAgentSvc, Method: "GetSnapshot", NoSpace: true, Host: true},
		rpcSpec{Path: []string{"host", "agent", "run-once"}, Short: "让主机代理立即采集并上报一次", Service: hostAgentSvc, Method: "RunOnce", NoSpace: true, Host: true},

		rpcSpec{Path: []string{"secret", "list"}, Short: "列出密钥（只含打码后的信息）", Service: secretMgrSvc, Method: "ListSecrets", NoSpace: true,
			Flags: []rpcFlag{strFlag("keyword", "keyword", "关键字"), strFlag("category", "category", "分类"), strFlag("provider", "provider", "云厂商"), strFlag("status", "status", "状态"),
				intFlag("offset", "offset", "偏移"), intFlag("limit", "limit", "条数")}},
		rpcSpec{Path: []string{"secret", "show"}, Short: "查看一个密钥的元信息（值已打码）", Service: secretMgrSvc, Method: "GetSecret", NoSpace: true,
			Flags: []rpcFlag{reqFlag("secret-id", "secret_id", "密钥 ID")}},
	)
}
