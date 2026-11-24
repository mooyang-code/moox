package command

const (
	walletTransferReason    = "人工结束一条钱包划转记录要先到交易所核对划转记录再判断到账与否，Agent 不代用户判断；用户确认后用 gateway call 调用"
	secretWriteReason       = "密钥的新建、修改、启停与删除由用户在管理台“设置 › 密钥”操作（明文不经过 Agent；gateway call 也拒绝这些方法）"
	sshReason               = "管理台的交互式 SSH 终端与文件管理；命令行用 host exec、host get、host put 代替"
	authReason              = "管理台的登录与会话；moox-cli 用自己的网关身份，不需要登录"
	cloudAccountWriteReason = "云账号的新建与修改在管理台运行状态页“函数”区的“云账号”对话框做（初始账号由 setup init 登记）；密钥本身在“设置 › 密钥”"
	spaceDupReason          = "与 SpaceMgr 同名；管理台走 SpaceMgr（space 命令），Storage 里的空间元数据由 metadata apply 与 setup 维护"
)

// handWrittenCoverage 记录不是规格表生成、而是手写实现的命令各自覆盖了管理台的哪些接口（接口 → 命令路径）。
var handWrittenCoverage = map[string]string{
	"trpc.moox.admin.ActivityLog/ListActivityLogs": "activity list",

	"trpc.moox.admin.Setup/ApplySetup":      "setup init",
	"trpc.moox.admin.Setup/GetSetupStatus":  "setup status",
	"trpc.moox.admin.Setup/PutConfigBackup": "config backup",
	"trpc.moox.admin.Setup/GetConfigBackup": "config restore",

	"trpc.moox.strategy.StrategyMgr/DeleteStrategyInstance": "strategy instance delete",

	"trpc.moox.archive.ArchiveMgr/GetArchiveOverview":     "archive overview",
	"trpc.moox.archive.ArchiveMgr/ListArchiveTasks":       "archive tasks list",
	"trpc.moox.archive.ArchiveMgr/PrepareArchiveTask":     "archive tasks prepare",
	"trpc.moox.archive.ArchiveMgr/PreviewArchiveSchedule": "archive tasks preview",
	"trpc.moox.archive.ArchiveMgr/CreateArchiveTask":      "archive tasks create",
	"trpc.moox.archive.ArchiveMgr/UpdateArchiveTask":      "archive tasks update",
	"trpc.moox.archive.ArchiveMgr/DeleteArchiveTask":      "archive tasks delete",
	"trpc.moox.archive.ArchiveMgr/RunArchiveTaskNow":      "archive tasks run",
	"trpc.moox.archive.ArchiveMgr/BackfillArchiveRange":   "archive tasks backfill",
	"trpc.moox.archive.ArchiveMgr/CancelArchiveRun":       "archive runs cancel",
	"trpc.moox.archive.ArchiveMgr/ListArchiveRuns":        "archive runs list",
	"trpc.moox.archive.ArchiveMgr/GetArchiveRun":          "archive runs get",
	"trpc.moox.archive.ArchiveMgr/ListArchivePartitions":  "archive partitions list",
	"trpc.moox.archive.ArchiveMgr/GetArchivePartition":    "archive partitions get",

	"trpc.moox.collector.CollectMgr/InitPackageUpload":     "collector function upload",
	"trpc.moox.collector.CollectMgr/CompletePackageUpload": "collector function upload",
	"trpc.moox.collector.CollectMgr/ListCloudFunctions":    "collector function list",

	"trpc.moox.collector.CollectMgr/CreateTask":             "collector task create",
	"trpc.moox.collector.CollectMgr/DeleteTask":             "collector task delete",
	"trpc.moox.collector.CollectMgr/GetCollectorStatus":     "collector status",
	"trpc.moox.collector.CollectMgr/CheckKline":             "collector kline check",
	"trpc.moox.collector.CollectMgr/SubmitKlineBackfill":    "collector kline backfill",
	"trpc.moox.collector.CollectMgr/GetKlineBackfill":       "collector kline backfill-status",
	"trpc.moox.collector.CollectMgr/ListKlineBackfills":     "collector kline backfill-status",
	"trpc.moox.collector.CollectMgr/CancelKlineBackfill":    "collector kline backfill-cancel",
	"trpc.moox.collector.CollectMgr/RetryKlineBackfill":     "collector kline backfill-retry",
	"trpc.moox.collector.CollectMgr/ConfirmHistoryGaps":     "collector kline history-confirm",
	"trpc.moox.collector.CollectMgr/ClearSourceAbsences":    "collector kline absences-clear",
	"trpc.moox.collector.CollectMgr/SubmitKlineResampleJob": "collector resample backfill",
	"trpc.moox.collector.CollectMgr/GetKlineResampleJob":    "collector resample jobs",
	"trpc.moox.collector.CollectMgr/ListKlineResampleJobs":  "collector resample jobs",
	"trpc.moox.collector.CollectMgr/CancelKlineResampleJob": "collector resample cancel",
	"trpc.moox.collector.CollectMgr/GetIngestMode":          "collector ingest-mode get",
	"trpc.moox.collector.CollectMgr/SetSubjectExclusions":   "collector subjects exclude",
	"trpc.moox.collector.CollectMgr/ClearSubjectExclusions": "collector subjects unexclude",
	"trpc.moox.collector.CollectMgr/ListSubjectExclusions":  "collector subjects exclusions",

	"trpc.moox.factor.FactorMgr/PublishUdf":  "udf publish",
	"trpc.moox.factor.FactorMgr/ListUdfs":    "udf list",
	"trpc.moox.factor.FactorMgr/GetUdf":      "udf show",
	"trpc.moox.factor.FactorMgr/ActivateUdf": "udf activate",

	"trpc.moox.monitor.MonitorMgr/ListNotificationChannels": "setup init",
	"trpc.moox.monitor.MonitorMgr/AddNotificationChannel":   "setup init",
	"trpc.moox.monitor.MonitorMgr/GetAlertMute":             "monitor mute-status",
	"trpc.moox.monitor.MonitorMgr/GetDoctorContext":         "doctor diagnose",

	// 模块生命周期（设计 3.7）：管理台只读；暂停、恢复、放行只给命令行（见 cliOnlyCoverage）。
	"trpc.moox.collector.CollectMgr/GetLifecycle":      "lifecycle status",
	"trpc.moox.strategy.StrategyMgr/GetLifecycle":      "lifecycle status",
	"trpc.moox.trade.TradeConsoleService/GetLifecycle": "lifecycle status",
	"trpc.moox.factor.FactorMgr/GetLifecycle":          "lifecycle status",
	"trpc.moox.archive.ArchiveMgr/GetLifecycle":        "lifecycle status",

	"trpc.moox.storage.Metadata/AddTagMembers":    "metadata tag members",
	"trpc.moox.storage.Metadata/RemoveTagMembers": "metadata tag members",
	"trpc.moox.storage.PrimaryStore/UpsertFields": "import",
}

// cliOnlyCoverage 记录管理台不开放、只有 moox-cli 能调用的方法由哪个手写命令覆盖（接口 → 命令路径）。
var cliOnlyCoverage = map[string]string{
	"trpc.moox.collector.CollectMgr/DeleteCloudFunctions": "collector function delete",
	"trpc.moox.collector.CollectMgr/ProbeFunctionEgress":  "collector function probe-egress",

	// 模块生命周期的写方法只给命令行：lifecycle 逐个模块，system 编排采集、因子引擎、策略、Trade。
	"trpc.moox.collector.CollectMgr/ControlLifecycle":      "lifecycle pause",
	"trpc.moox.strategy.StrategyMgr/ControlLifecycle":      "lifecycle pause",
	"trpc.moox.trade.TradeConsoleService/ControlLifecycle": "lifecycle pause",
	"trpc.moox.factor.FactorMgr/ControlLifecycle":          "lifecycle pause",
	"trpc.moox.archive.ArchiveMgr/ControlLifecycle":        "lifecycle pause",
	"trpc.moox.factor.EngineControl/GetLifecycle":          "lifecycle status",
	"trpc.moox.factor.EngineControl/ControlLifecycle":      "lifecycle pause",
	"trpc.moox.factor.EngineControl/CheckTask":             "factor task status",
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

	"trpc.moox.callbackdispatcher.CallbackDispatcherService/CreateEventRule": "返回验证密钥明文，请在管理台「事件中心」的“订阅”页签新建，密钥只在那里显示一次，不经过 Agent",

	"trpc.moox.monitor.MonitorMgr/DeleteNotificationChannel": "告警渠道的增删改在管理台「监控 › 告警渠道」做；初始渠道由 setup init 在 Monitor 还没有渠道时写入一次",

	"trpc.moox.ops.SecretMgr/CreateSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/UpdateSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/DeleteSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/ToggleSecretStatus": secretWriteReason,
	"trpc.moox.ops.SecretMgr/ExportSecrets":      "密钥整包导出带口令，moox-cli 无权调用，由用户在管理台操作",
	"trpc.moox.ops.SecretMgr/ImportSecrets":      "密钥整包导入带口令，moox-cli 无权调用，由用户在管理台操作",

	"trpc.moox.storage.Metadata/CreateSpace": spaceDupReason,
	"trpc.moox.storage.Metadata/UpdateSpace": spaceDupReason,
	"trpc.moox.storage.Metadata/DeleteSpace": spaceDupReason,
	"trpc.moox.storage.Metadata/ListSpaces":  spaceDupReason,

	"trpc.moox.trade.TradeConsoleService/ResolveWalletTransfer": walletTransferReason,
	"trpc.moox.trade.TradeConsoleService/UnblockInstrument":     "解除下架禁止请用户在管理台交易页的下架名单里查看与解除（事件中心取消执行也会解除）：解除后策略会恢复买入，Agent 不代用户解除",
}
