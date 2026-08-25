package sysdeploy

import (
	"fmt"
	"strings"
)

// defaultPublicHost records the current bootstrap deployment host only.
// New deployments should update public rows through SysDeploy/UI after the admin plane is reachable.
const defaultPublicHost = "106.53.107.122"

const storageMetadataGatewayMethods = "[\"CreateSpace\",\"UpdateSpace\",\"GetSpace\",\"ListSpaces\",\"CreateView\",\"UpdateView\",\"RequestViewRebuild\",\"GetView\",\"ListViews\",\"UpsertViewColumn\",\"ListViewColumns\",\"ListViewRebuildLogs\",\"CreateDataSource\",\"UpdateDataSource\",\"GetDataSource\",\"ListDataSources\",\"UpsertSubject\",\"UpsertSubjectSymbol\",\"RegisterDataSubject\",\"GetSubject\",\"ListSubjects\",\"ListSubjectSymbols\",\"CreateDataset\",\"UpdateDataset\",\"GetDataset\",\"ListDatasets\",\"BindDatasetSubject\",\"ListDatasetSubjects\",\"CreateFieldGroup\",\"UpdateFieldGroup\",\"GetFieldGroup\",\"ListFieldGroups\",\"CreateField\",\"UpdateField\",\"GetField\",\"ListFields\",\"BatchUpdateFields\",\"DeleteFieldGroup\",\"CreateFactor\",\"UpdateFactor\",\"GetFactor\",\"ListFactors\",\"UpsertDatasetColumn\",\"ListDatasetColumns\",\"GetDataNode\",\"ListDataNodes\",\"UpdateDataNode\",\"DeleteDataNode\",\"CheckDatasetActivation\",\"ActivateDataset\",\"RebindDatasetDataNode\",\"RegisterArchiveFile\",\"ListArchiveFiles\"]"
const storagePrimaryGatewayMethods = "[\"UpsertFields\",\"ReadFields\",\"ReadTimeSeriesRows\",\"ReadRecordRows\",\"ReportDatasetPeriodCollected\",\"AppendDatasetSyncPoint\",\"WaitViewSyncPoint\",\"ReportFactorPeriodComputed\",\"GetFactorPeriodComputed\"]"
const storageViewGatewayMethods = "[\"QueryTimeSeriesRows\",\"SearchRecordRows\"]"
const storageMetadataGatewayCallers = "[\"admin-gateway\",\"collector\",\"factor\",\"monitor\",\"archive\",\"moox-cli\",\"storage-view\"]"
const storagePrimaryGatewayCallers = "[\"admin-gateway\",\"collector\",\"factor\",\"monitor\",\"archive\",\"storage-view\"]"
const storageViewGatewayCallers = "[\"admin-gateway\",\"collector\",\"factor\",\"monitor\"]"

var obsoleteTradeDeploymentNames = []string{
	"trade_account",
	"trade_balance",
	"trade_fund",
	"trade_apikey",
	"trade_channel",
	"trade_tradeop",
	"trade_order",
	"trade_tradeq",
	"trade_position",
	"trade_rebalance",
	"trade_ops",
	"trade_exchange_account",
	"trade_execution",
	"trade_logical_account",
}

func DefaultDeployments(nodeID string) []Deployment {
	rows := []Deployment{
		withExtra(deployment("admin_gateway", "gateway", "https", defaultPublicHost, 9527, "/api/admin", "public", "Caddy 管理台 HTTPS 入口，浏览器同源访问 /api/admin/*"), `{"health_url":"http://127.0.0.1:11010/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("web_host", "frontend", "https", defaultPublicHost, 9527, "", "public", "Caddy 管理台 HTTPS 页面入口；web-host 上游仅绑定 loopback"), `{"health_url":"http://127.0.0.1:19527/readyz","health_kind":"readiness","monitor_enabled":true}`),
		deployment("service_gateway", "gateway", "https", defaultPublicHost, 11001, "/api/service", "public", "后台和 SCF HTTPS 服务入口，使用 HMAC 鉴权访问 /api/service/*"),
		deployment("service_gateway_native", "gateway", "trpc", defaultPublicHost, 11003, "", "public", "公网原生服务网关，供 SCF 访问 Storage"),
		withExtra(deployment("storage-primary", "storage", "http", defaultPublicHost, 20200, "trpc.moox.storage.Metadata", "public", "moox-storage PrimaryStore + Metadata 服务"), storagePrimaryExtraConfig()),
		withExtra(deployment("storage-view", "storage", "http", defaultPublicHost, 20202, "trpc.moox.storage.DataView", "public", "moox-storage DataView + ViewBuilder 服务"), storageViewExtraConfig()),
		deployment("admin_auth", "admin_rpc", "http", "127.0.0.1", 11100, "trpc.moox.infra.Auth", "internal", "认证 RPC 服务"),
		deployment("dnsproxy", "admin_rpc", "http", "127.0.0.1", 11101, "trpc.moox.infra.Dns", "internal", "DNS 代理 RPC 服务"),
		withExtra(deployment("moox_monitor", "monitor", "http", "127.0.0.1", 11410, "trpc.moox.monitor.MonitorMgr", "internal", "独立服务监控模块，承载 HTTP/TCP 探测和告警"), `{"health_url":"http://127.0.0.1:11409/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("eventbus", "eventbus", "http", "127.0.0.1", 11420, "trpc.moox.eventbus.EventBusMgr", "internal", "MooX 统一 NATS JetStream EventBus 服务"), `{"health_url":"http://127.0.0.1:11419/readyz","health_kind":"readiness","monitor_enabled":true,"nats_url":"tls://127.0.0.1:4222"}`),
		withExtra(deployment("moox_gateway", "gateway", "http", "127.0.0.1", 11002, "", "internal", "节点服务网关，承载服务路由与路由表刷新"), `{"health_url":"http://127.0.0.1:11012/readyz","health_kind":"readiness","health_body_contains":"ready","monitor_enabled":true}`),
		withExtra(deployment("moox_collector", "collector", "http", "127.0.0.1", 11402, "trpc.moox.collector.CollectMgr", "internal", "独立采集管理服务，承载采集规则、任务实例和 planner"), `{"health_url":"http://127.0.0.1:11412/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_cloudnode", "cloudnode", "http", "127.0.0.1", 11401, "trpc.moox.cloudnode.CloudNodeMgr", "internal", "独立云节点执行平台，承载云节点、代码包、异步 JobItem 队列和同步调用"), `{"health_url":"http://127.0.0.1:11411/readyz","health_kind":"readiness","monitor_enabled":true,"timeout_ms":120000,"max_body_bytes":33554432}`),
		withExtra(deployment("moox_factor", "factor", "http", "127.0.0.1", 11404, "trpc.moox.factor.FactorMgr", "internal", "因子计算服务，承载因子定义、绑定、补算与结果写回"), `{"health_url":"http://127.0.0.1:11414/readyz","health_kind":"readiness","monitor_enabled":true,"timeout_ms":120000,"gateway_methods":["CreateFactor","UpdateFactor","GetFactor","ListFactors","SetFactorStatus","DeleteFactor","UpsertBinding","ListBindings","DeleteBinding","RecalcFactor","GetEngineStatus"],"gateway_callers":["admin-gateway","moox-cli"]}`),
		withExtra(deployment("moox_strategy", "strategy", "http", "127.0.0.1", 11430, "trpc.moox.strategy.StrategyMgr", "internal", "交易策略运行、目标和绩效查询服务"), `{"health_url":"http://127.0.0.1:11431/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_archive", "archive", "http", "127.0.0.1", 11416, "", "internal", "事件归档和物化服务"), `{"health_url":"http://127.0.0.1:11416/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_hostagent", "hostagent", "http", "127.0.0.1", 11426, "trpc.moox.hostagent.HostAgentMgr", "internal", "主机指标采集代理"), `{"health_url":"http://127.0.0.1:11425/readyz","health_kind":"readiness","monitor_enabled":true}`),
		withExtra(deployment("moox_trade", "trade", "http", "127.0.0.1", 11210, "", "internal", "量化交易执行服务"), `{"health_url":"http://127.0.0.1:11210/readyz","health_kind":"readiness","monitor_enabled":true}`),
		deployment("ssh", "admin_rpc", "http", "127.0.0.1", 11106, "trpc.moox.ops.Ssh", "internal", "SSH 管理 RPC 服务"),
		deployment("space", "admin_rpc", "http", "127.0.0.1", 11107, "trpc.moox.admin.SpaceMgr", "internal", "空间管理 RPC 服务"),
		deployment("secret", "admin_rpc", "http", "127.0.0.1", 11108, "trpc.moox.ops.SecretMgr", "internal", "秘钥管理 RPC 服务"),
		deployment("sysdeploy", "admin_rpc", "http", "127.0.0.1", 11109, "trpc.moox.ops.SysDeploy", "internal", "系统服务部署信息 RPC 服务"),
		deployment("trade_console", "trade", "http", "127.0.0.1", 11200, "trpc.moox.trade.TradeConsoleService", "internal", "统一交易控制台与执行服务"),
		withExtra(deployment("trade_dns_resolver", "trade", "http", "127.0.0.1", 11203, "trpc.moox.trade.TradeDNSResolverService", "internal", "交易节点 DNS 解析与连通性探测服务"), `{"gateway_methods":["ResolveDomains"],"gateway_callers":["collector"]}`),
	}
	canonical := map[string]string{
		"moox_collector": "collectmgr", "moox_cloudnode": "cloudnode", "moox_factor": "factormgr", "moox_strategy": "strategymgr", "moox_monitor": "monitor", "moox_hostagent": "hostagent", "sysdeploy": "sysdeploy", "secret": "secret",
	}
	for i := range rows {
		rows[i].NodeID = nodeID
		if rows[i].ServiceName == "moox_hostagent" {
			rows[i].Status = "disabled"
		}
		switch rows[i].ServiceName {
		case "storage-primary", "storage-view":
			// Storage BFF traffic enters the node gateway; keep both independently
			// deployed storage roles in the route snapshot by default.
			rows[i].Host = "127.0.0.1"
			rows[i].GatewayServiceID = rows[i].ServiceName
			rows[i].GatewayEnabled = true
		case "trade_dns_resolver":
			rows[i].GatewayServiceID = rows[i].ServiceName
			rows[i].GatewayEnabled = true
		}
		if serviceID, ok := canonical[rows[i].ServiceName]; ok && rows[i].GatewayPath != "" {
			rows[i].GatewayServiceID, rows[i].GatewayEnabled = serviceID, true
		}
		if rows[i].ServiceName == "sysdeploy" {
			rows[i].ExtraConfig = `{"gateway_methods":["ListActiveServiceDeployments","ListServiceDeployments"],"gateway_callers":["admin-gateway","collector","monitor","moox-cli"]}`
		}
		if rows[i].ServiceName == "secret" {
			rows[i].ExtraConfig = `{"gateway_methods":["ListSecrets","GetSecretValue"],"gateway_callers":["admin-gateway","cloudnode","moox-cli","trade"]}`
		}
		if rows[i].GatewayEnabled && !strings.Contains(rows[i].ExtraConfig, `"gateway_methods"`) {
			rows[i].ExtraConfig = strings.TrimSuffix(rows[i].ExtraConfig, "}") + `,"gateway_methods":["*"],"gateway_callers":["*"]}`
		}
	}
	return rows
}

func withExtra(item Deployment, extra string) Deployment {
	item.ExtraConfig = extra
	normalizeDeployment(&item)
	return item
}

func storagePrimaryExtraConfig() string {
	return fmt.Sprintf(`{"health_url":"http://127.0.0.1:20210/readyz","health_kind":"readiness","monitor_enabled":true,"gateway_methods":%s,"gateway_callers":%s,"gateway_routes":[{"service_path":"trpc.moox.storage.Metadata","port":20100,"gateway_methods":["DeleteSpace"],"gateway_callers":["admin-gateway","moox-cli"]},{"service_path":"trpc.moox.storage.Metadata","port":20100,"gateway_methods":["ClaimViewIndexBuild","UpdateViewIndexBuild","ActivateViewIndex","FailViewIndexBuild","CreateViewRebuildLog","UpdateViewRebuildLog","UpsertSkippedViewRebuildLog"],"gateway_callers":["storage-view"]},{"service_path":"trpc.moox.storage.PrimaryStore","port":20102,"gateway_methods":%s,"gateway_callers":%s}]}`, storageMetadataGatewayMethods, storageMetadataGatewayCallers, storagePrimaryGatewayMethods, storagePrimaryGatewayCallers)
}

func storageViewExtraConfig() string {
	return fmt.Sprintf(`{"health_url":"http://127.0.0.1:20211/readyz","health_kind":"readiness","monitor_enabled":true,"gateway_methods":%s,"gateway_callers":%s}`, storageViewGatewayMethods, storageViewGatewayCallers)
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
