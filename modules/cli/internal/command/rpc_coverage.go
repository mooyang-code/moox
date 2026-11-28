package command

const (
	walletTransferReason    = "人工结束一条钱包划转记录要先到交易所核对划转记录再判断到账与否，Agent 不代用户判断；用户确认后用 gateway call 调用"
	secretWriteReason       = "密钥的新建、修改、启停与删除由用户在管理台“系统设置 › 密钥管理”操作（明文不经过 Agent；gateway call 也拒绝这些方法）"
	sshReason               = "管理台的交互式 SSH 终端与文件管理；命令行用 host exec、host get、host put 代替"
	authReason              = "管理台的登录与会话；moox-cli 用自己的网关身份，不需要登录"
	cloudAccountWriteReason = "云账号的新建与修改在管理台“数据采集 › 采集运行”的“运行状态”页签里“函数”区的“云账号”对话框做（初始账号由 setup init 登记）；密钥本身在“系统设置 › 密钥管理”"
)

// handWrittenCoverage 记录不是规格表生成、而是手写实现的命令各自覆盖了管理台的哪些接口（接口 → 命令路径）。
var handWrittenCoverage = map[string]string{
	"trpc.moox.admin.ActivityLog/ListActivityLogs": "activity list",

	"trpc.moox.strategy.StrategyMgr/DeleteStrategyInstance": "strategy instance delete",

	"trpc.moox.collector.CollectMgr/InitPackageUpload":     "collector function upload",
	"trpc.moox.collector.CollectMgr/CompletePackageUpload": "collector function upload",
	"trpc.moox.collector.CollectMgr/ListCloudFunctions":    "collector function list",

	"trpc.moox.collector.CollectMgr/CreateTask": "collector task create",
	"trpc.moox.collector.CollectMgr/DeleteTask": "collector task delete",

	"trpc.moox.factor.FactorMgr/PublishUdf":  "udf publish",
	"trpc.moox.factor.FactorMgr/ListUdfs":    "udf list",
	"trpc.moox.factor.FactorMgr/GetUdf":      "udf show",
	"trpc.moox.factor.FactorMgr/ActivateUdf": "udf activate",

	"trpc.moox.monitor.MonitorMgr/ListNotificationChannels": "setup init",
	"trpc.moox.monitor.MonitorMgr/GetAlertMute":             "monitor mute-status",

	"trpc.moox.ops.SecretMgr/ExportSecrets": "setup backup",
	"trpc.moox.ops.SecretMgr/ImportSecrets": "setup backup",

	// 模块生命周期（设计 3.7）：管理台只读；暂停、恢复、放行只给命令行（见 cliOnlyCoverage）。
	"trpc.moox.collector.CollectMgr/GetLifecycle":      "lifecycle status",
	"trpc.moox.strategy.StrategyMgr/GetLifecycle":      "lifecycle status",
	"trpc.moox.trade.TradeConsoleService/GetLifecycle": "lifecycle status",

	"trpc.moox.storage.Metadata/AddTagMembers":    "metadata tag members",
	"trpc.moox.storage.Metadata/RemoveTagMembers": "metadata tag members",
	"trpc.moox.storage.PrimaryStore/UpsertFields": "import",
}

// cliOnlyCoverage 记录管理台不开放、只有 moox-cli 能调用的方法由哪个手写命令覆盖（接口 → 命令路径）。
var cliOnlyCoverage = map[string]string{
	"trpc.moox.ops.SecretMgr/EnsureInitialSecret": "setup init",

	"trpc.moox.monitor.MonitorMgr/ExportNotificationChannels": "setup backup",
	"trpc.moox.monitor.MonitorMgr/ImportNotificationChannels": "setup install",

	"trpc.moox.collector.CollectMgr/DeleteCloudFunctions": "collector function delete",
	"trpc.moox.collector.CollectMgr/ProbeFunctionEgress":  "collector function probe-egress",

	// 模块生命周期的写方法只给命令行：lifecycle 逐个模块，system 编排采集、因子引擎、策略、Trade。
	"trpc.moox.collector.CollectMgr/ControlLifecycle":      "lifecycle pause",
	"trpc.moox.strategy.StrategyMgr/ControlLifecycle":      "lifecycle pause",
	"trpc.moox.trade.TradeConsoleService/ControlLifecycle": "lifecycle pause",
	"trpc.moox.factor.EngineControl/GetLifecycle":          "lifecycle status",
	"trpc.moox.factor.EngineControl/ControlLifecycle":      "lifecycle pause",
	"trpc.moox.factor.EngineControl/CheckTask":             "factor task status",
	"trpc.moox.factor.FactorMgr/ImportFactorCatalog":       "factor import",
	"trpc.moox.factor.FactorMgr/PurgeResultDataset":        "factor purge-dataset",
	"trpc.moox.factor.EngineControl/WriteSeries":           "import",
	"trpc.moox.factor.EngineControl/GetSeriesFields":       "import",
	"trpc.moox.factor.EngineControl/CheckSeriesSubject":    "import",
	"trpc.moox.factor.EngineControl/GetSeriesBounds":       "import",
}

// consoleOnly 记录有意只留在管理台的接口及原因。
var consoleOnly = map[string]string{
	"trpc.moox.collector.CollectMgr/CreateCloudAccount": cloudAccountWriteReason,
	"trpc.moox.collector.CollectMgr/UpdateCloudAccount": cloudAccountWriteReason,
	"trpc.moox.infra.Auth/Login":                        authReason,
	"trpc.moox.infra.Auth/Logout":                       authReason,
	"trpc.moox.infra.Auth/GetUserInfo":                  authReason,

	"trpc.moox.ops.Ssh/CreateSession":     sshReason,
	"trpc.moox.ops.Ssh/DisconnectSession": sshReason,
	"trpc.moox.ops.Ssh/ResizeWindow":      sshReason,
	"trpc.moox.ops.Ssh/SftpList":          sshReason,
	"trpc.moox.ops.Ssh/SftpMkdir":         sshReason,
	"trpc.moox.ops.Ssh/SftpDelete":        sshReason,

	"trpc.moox.monitor.MonitorMgr/AddNotificationChannel":    "告警渠道的增删改在管理台「资源与运维 › 监控告警」的“消息通知设置”做（不经命令行传 Webhook 与签名密钥明文）",
	"trpc.moox.monitor.MonitorMgr/DeleteNotificationChannel": "告警渠道的增删改在管理台「资源与运维 › 监控告警」的“消息通知设置”做",

	"trpc.moox.ops.SecretMgr/CreateSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/UpdateSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/DeleteSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/ToggleSecretStatus": secretWriteReason,

	"trpc.moox.trade.TradeConsoleService/ResolveWalletTransfer": walletTransferReason,
	"trpc.moox.trade.TradeConsoleService/UnblockInstrument":     "解除下架禁止请用户在管理台“交易管理 › 下架名单”里查看与解除（会一并取消进行中的下架处理）：解除后策略会恢复买入，Agent 不代用户解除",
}
