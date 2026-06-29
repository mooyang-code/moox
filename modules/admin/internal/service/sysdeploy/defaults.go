package sysdeploy

// defaultPublicHost records the current bootstrap deployment host only.
// New deployments should update public rows through SysDeploy/UI after the admin plane is reachable.
const defaultPublicHost = "106.53.107.122"

func DefaultDeployments() []Deployment {
	rows := []Deployment{
		deployment("admin_gateway", "gateway", "http", defaultPublicHost, 11000, "/api/admin", "public", "管理台请求统一入口，前端直接访问 /api/admin/*"),
		deployment("service_gateway", "gateway", "http", defaultPublicHost, 11000, "/api/service", "public", "后台/SCF 请求统一入口，使用 HMAC 鉴权访问 /api/service/*"),
		deployment("web_host", "frontend", "http", defaultPublicHost, 9527, "", "public", "管理台静态资源服务，仅承载前端页面，不代理 API"),
		deployment("storage_metadata", "storage", "http", defaultPublicHost, 20200, "trpc.moox.storage.Metadata", "public", "moox-storage 元数据 HTTP 服务"),
		deployment("storage_access", "storage", "http", defaultPublicHost, 20201, "trpc.moox.storage.Access", "public", "moox-storage 数据写入/读取 HTTP 服务，SCF 采集写入优先直连"),
		deployment("storage_view", "storage", "http", defaultPublicHost, 20202, "trpc.moox.storage.DataView", "public", "moox-storage 数据视图 HTTP 服务"),
		deployment("storage_metadata_trpc", "storage_rpc", "trpc", defaultPublicHost, 20100, "trpc.moox.storage.Metadata", "public", "moox-storage 元数据 tRPC 服务"),
		deployment("storage_primary_trpc", "storage_rpc", "trpc", defaultPublicHost, 20101, "trpc.moox.storage.PrimaryStore", "public", "moox-storage PrimaryStore tRPC 服务"),
		deployment("storage_access_trpc", "storage_rpc", "trpc", defaultPublicHost, 20102, "trpc.moox.storage.Access", "public", "moox-storage Access tRPC 服务"),
		deployment("storage_view_trpc", "storage_rpc", "trpc", defaultPublicHost, 20103, "trpc.moox.storage.DataView", "public", "moox-storage DataView tRPC 服务"),
		deployment("collector_api", "service_api", "http", "127.0.0.1", 11001, "trpc.moox.api.stdhttp", "internal", "采集控制 API 内部服务"),
		deployment("admin_auth", "admin_rpc", "http", "127.0.0.1", 11100, "trpc.moox.infra.Auth", "internal", "认证 RPC 服务"),
		deployment("dnsproxy", "admin_rpc", "http", "127.0.0.1", 11101, "trpc.moox.infra.Dns", "internal", "DNS 代理 RPC 服务"),
		deployment("asynctask", "admin_rpc", "http", "127.0.0.1", 11102, "trpc.moox.infra.AsyncTask", "internal", "异步任务 RPC 服务"),
		deployment("monitor", "admin_rpc", "http", "127.0.0.1", 11103, "trpc.moox.ops.Monitor", "internal", "资源监控 RPC 服务"),
		deployment("collectmgr", "admin_rpc", "http", "127.0.0.1", 11104, "trpc.moox.collect.CollectMgr", "internal", "采集管理 RPC 服务"),
		deployment("cloudnode", "admin_rpc", "http", "127.0.0.1", 11105, "trpc.moox.collect.CloudNodeMgr", "internal", "云节点 RPC 服务"),
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
	}
	return rows
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
