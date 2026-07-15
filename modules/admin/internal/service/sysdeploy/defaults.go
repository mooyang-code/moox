package sysdeploy

// defaultPublicHost records the current bootstrap deployment host only.
// New deployments should update public rows through SysDeploy/UI after the admin plane is reachable.
const defaultPublicHost = "106.53.107.122"

func DefaultDeployments() []Deployment {
	rows := []Deployment{
		withExtra(deployment("admin_gateway", "gateway", "https", defaultPublicHost, 9527, "/api/admin", "public", "Caddy 管理台 HTTPS 入口，浏览器同源访问 /api/admin/*"), `{"health_url":"http://127.0.0.1:11010/readyz","health_kind":"readiness","monitor_enabled":true}`),
		deployment("service_gateway", "gateway", "https", defaultPublicHost, 11001, "/api/service", "public", "后台/SCF HTTPS 入口，使用 HMAC 鉴权访问 /api/service/*"),
		deployment("service_gateway_internal", "gateway", "http", "127.0.0.1", 11002, "/api/service", "internal", "同机后台请求入口，使用 HMAC 鉴权访问 /api/service/*"),
		withExtra(deployment("web_host", "frontend", "https", defaultPublicHost, 9527, "", "public", "Caddy 管理台 HTTPS 页面入口；web-host 上游仅绑定 loopback"), `{"health_url":"http://127.0.0.1:19527/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("storage_metadata", "storage", "http", defaultPublicHost, 20200, "trpc.moox.storage.Metadata", "public", "moox-storage 元数据 HTTP 服务"), `{"health_url":"http://127.0.0.1:20210/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("storage_access", "storage", "http", defaultPublicHost, 20201, "trpc.moox.storage.Access", "public", "moox-storage 数据写入/读取 HTTP 服务，SCF 采集写入优先直连"), `{"health_url":"http://127.0.0.1:20210/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("storage_view", "storage", "http", defaultPublicHost, 20202, "trpc.moox.storage.DataView", "public", "moox-storage 数据视图 HTTP 服务"), `{"health_url":"http://127.0.0.1:20212/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("storage_view_builder", "storage", "http", "127.0.0.1", 20211, "", "internal", "moox-storage View 独立进程中的物化职责健康检查"), `{"health_url":"http://127.0.0.1:20211/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("storage_view_query", "storage", "http", "127.0.0.1", 20202, "trpc.moox.storage.DataView", "internal", "moox-storage View 独立进程中的查询职责"), `{"health_url":"http://127.0.0.1:20212/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("storage_view_index", "storage_rpc", "trpc", "127.0.0.1", 20104, "trpc.moox.storage.ViewIndex", "internal", "moox-storage View 索引文件唯一 owner"), `{"health_url":"http://127.0.0.1:20213/readyz","health_kind":"readiness","monitor_enabled":true}`),
		deployment("storage_metadata_trpc", "storage_rpc", "trpc", defaultPublicHost, 20100, "trpc.moox.storage.Metadata", "public", "moox-storage 元数据 tRPC 服务"),
		deployment("storage_primary_trpc", "storage_rpc", "trpc", defaultPublicHost, 20101, "trpc.moox.storage.PrimaryStore", "public", "moox-storage PrimaryStore tRPC 服务"),
		deployment("storage_access_trpc", "storage_rpc", "trpc", defaultPublicHost, 20102, "trpc.moox.storage.Access", "public", "moox-storage Access tRPC 服务"),
		deployment("storage_view_trpc", "storage_rpc", "trpc", defaultPublicHost, 20103, "trpc.moox.storage.DataView", "public", "moox-storage DataView tRPC 服务"),
		deployment("admin_auth", "admin_rpc", "http", "127.0.0.1", 11100, "trpc.moox.infra.Auth", "internal", "认证 RPC 服务"),
		deployment("dnsproxy", "admin_rpc", "http", "127.0.0.1", 11101, "trpc.moox.infra.Dns", "internal", "DNS 代理 RPC 服务"),
		withExtra(deployment("moox_monitor", "monitor", "http", "127.0.0.1", 11410, "trpc.moox.monitor.MonitorMgr", "internal", "独立服务监控模块，承载 HTTP/TCP 探测、告警和多实例协同"), `{"health_url":"http://127.0.0.1:11409/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("eventbus", "eventbus", "http", "127.0.0.1", 11420, "trpc.moox.eventbus.EventBusMgr", "internal", "MooX 统一 NATS JetStream EventBus 服务"), `{"health_url":"http://127.0.0.1:11419/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_collector", "collector", "http", "127.0.0.1", 11402, "trpc.moox.collector.CollectMgr", "internal", "独立采集管理服务，承载采集规则、任务实例和 planner"), `{"health_url":"http://127.0.0.1:11412/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_cloudnode", "cloudnode", "http", "127.0.0.1", 11401, "trpc.moox.cloudnode.CloudNodeMgr", "internal", "独立云节点执行平台，承载云节点、代码包、异步 JobItem 队列和同步调用"), `{"health_url":"http://127.0.0.1:11411/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_factor", "factor", "http", "127.0.0.1", 11404, "trpc.moox.factor.FactorMgr", "internal", "因子计算服务，承载因子定义、绑定、补算与结果写回"), `{"health_url":"http://127.0.0.1:11414/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_strategy", "strategy", "http", "127.0.0.1", 11430, "trpc.moox.strategy.StrategyMgr", "internal", "交易策略运行、目标和绩效查询服务"), `{"health_url":"http://127.0.0.1:11431/readyz","health_kind":"readiness","monitor_enabled":false}`),
		withExtra(deployment("moox_archive", "archive", "http", "127.0.0.1", 11416, "", "internal", "事件归档和物化服务"), `{"health_url":"http://127.0.0.1:11416/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_hostagent", "hostagent", "http", "127.0.0.1", 11426, "trpc.moox.hostagent.HostAgentMgr", "internal", "主机指标采集代理"), `{"health_url":"http://127.0.0.1:11425/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_trade", "trade", "http", "127.0.0.1", 11210, "", "internal", "交易执行和账本服务"), `{"health_url":"http://127.0.0.1:11210/readyz","health_kind":"readiness","monitor_enabled":true}`),
		deployment("ssh", "admin_rpc", "http", "127.0.0.1", 11106, "trpc.moox.ops.Ssh", "internal", "SSH 管理 RPC 服务"),
		deployment("space", "admin_rpc", "http", "127.0.0.1", 11107, "trpc.moox.admin.SpaceMgr", "internal", "空间管理 RPC 服务"),
		deployment("secret", "admin_rpc", "http", "127.0.0.1", 11108, "trpc.moox.ops.SecretMgr", "internal", "秘钥管理 RPC 服务"),
		deployment("sysdeploy", "admin_rpc", "http", "127.0.0.1", 11109, "trpc.moox.ops.SysDeploy", "internal", "系统服务部署信息 RPC 服务"),
		deployment("trade_account", "trade", "http", "127.0.0.1", 11200, "trpc.moox.trade.AccountSvc", "internal", "交易账户服务"),
		deployment("trade_balance", "trade", "http", "127.0.0.1", 11201, "trpc.moox.trade.BalanceSvc", "internal", "交易余额服务"),
		deployment("trade_fund", "trade", "http", "127.0.0.1", 11202, "trpc.moox.trade.FundSvc", "internal", "交易资金服务"),
		deployment("trade_apikey", "trade", "http", "127.0.0.1", 11203, "trpc.moox.trade.ApiKeySvc", "internal", "交易 API Key 服务"),
		deployment("trade_channel", "trade", "http", "127.0.0.1", 11204, "trpc.moox.trade.ChannelSvc", "internal", "交易通道服务"),
		deployment("trade_tradeop", "trade", "http", "127.0.0.1", 11205, "trpc.moox.trade.TradeOpSvc", "internal", "交易操作服务"),
		deployment("trade_order", "trade", "http", "127.0.0.1", 11206, "trpc.moox.trade.OrderSvc", "internal", "订单服务"),
		deployment("trade_tradeq", "trade", "http", "127.0.0.1", 11207, "trpc.moox.trade.TradeQuerySvc", "internal", "交易查询服务"),
		deployment("trade_position", "trade", "http", "127.0.0.1", 11208, "trpc.moox.trade.PositionSvc", "internal", "持仓服务"),
		deployment("trade_rebalance", "trade", "http", "127.0.0.1", 11211, "trpc.moox.trade.RebalanceSvc", "internal", "目标仓位调仓服务"),
		deployment("trade_ops", "trade", "http", "127.0.0.1", 11212, "trpc.moox.trade.TradeOpsSvc", "internal", "交易暂停、对账与 Saga 运维服务"),
	}
	return rows
}

func withExtra(item Deployment, extra string) Deployment {
	item.ExtraConfig = extra
	normalizeDeployment(&item)
	return item
}

func deployment(name, kind, protocol, host string, port int32, gatewayPath, scope, description string) Deployment {
	item := Deployment{
		ServiceName: name,
		ServiceKind: kind,
		Protocol:    protocol,
		Host:        host,
		Port:        port,
		GatewayPath: gatewayPath,
		Scope:       scope,
		Status:      "active",
		Description: description,
		ExtraConfig: "{}",
	}
	normalizeDeployment(&item)
	return item
}
