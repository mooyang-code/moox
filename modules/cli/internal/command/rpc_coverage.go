package command

const (
	tradeWriteReason  = "交易模块尚未完工：不提供写操作命令，Agent 不代用户创建账户、下单、撤单或清仓"
	secretWriteReason = "密钥的新建、修改、启停与删除由用户在管理台“设置 › 密钥”操作（明文不经过 Agent；gateway call 也拒绝这些方法）"
	sshReason         = "管理台的交互式 SSH 终端与文件管理；命令行用 host exec、host get、host put 代替"
	authReason        = "管理台的登录与会话；moox-cli 用自己的网关身份，不需要登录"
	spaceDupReason    = "与 SpaceMgr 同名；管理台走 SpaceMgr（space 命令），Storage 里的空间元数据由 metadata apply 与 setup 维护"
)

// handWrittenCoverage 记录不是规格表生成、而是手写实现的命令各自覆盖了管理台的哪些接口（接口 → 命令路径）。
var handWrittenCoverage = map[string]string{
	"trpc.moox.admin.ActivityLog/ListActivityLogs": "activity list",

	"trpc.moox.admin.CollectorPublishLease/AcquireCollectorPublishLease": "cloud function create",
	"trpc.moox.admin.CollectorPublishLease/RenewCollectorPublishLease":   "cloud function create",
	"trpc.moox.admin.CollectorPublishLease/ReleaseCollectorPublishLease": "cloud function create",

	"trpc.moox.admin.Setup/ApplySetup":      "setup apply",
	"trpc.moox.admin.Setup/GetSetupStatus":  "setup status",
	"trpc.moox.admin.Setup/PutConfigBackup": "config backup",
	"trpc.moox.admin.Setup/GetConfigBackup": "config restore",

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

	"trpc.moox.cloudfunction.CloudFunctionMgr/InitPackageUpload":                          "collector function upload",
	"trpc.moox.cloudfunction.CloudFunctionMgr/CompletePackageUpload":                      "collector function upload",
	"trpc.moox.cloudfunction.CloudFunctionMgr/SubmitCreateFunctionInstances":              "cloud function create",
	"trpc.moox.cloudfunction.CloudFunctionMgr/SubmitDeployFunctionInstances":              "cloud function deploy",
	"trpc.moox.cloudfunction.CloudFunctionMgr/SubmitDeleteFunctionInstances":              "cloud function delete",
	"trpc.moox.cloudfunction.CloudFunctionMgr/SubmitUpdateFunctionInstanceRuntimeConfigs": "cloud function update-config",

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

	"trpc.moox.monitor.MonitorMgr/ListNotificationChannels":  "config notification",
	"trpc.moox.monitor.MonitorMgr/AddNotificationChannel":    "config notification",
	"trpc.moox.monitor.MonitorMgr/DeleteNotificationChannel": "config notification",
	"trpc.moox.monitor.MonitorMgr/GetAlertMute":              "monitor mute-status",
	"trpc.moox.monitor.MonitorMgr/GetDoctorContext":          "doctor diagnose",

	"trpc.moox.storage.Metadata/AddTagMembers":    "metadata tag members",
	"trpc.moox.storage.Metadata/RemoveTagMembers": "metadata tag members",
	"trpc.moox.storage.PrimaryStore/UpsertFields": "import",
}

// consoleOnly 记录有意只留在管理台的接口及原因。
var consoleOnly = map[string]string{
	"trpc.moox.infra.Auth/GetLoginSalt":          authReason,
	"trpc.moox.infra.Auth/Login":                 authReason,
	"trpc.moox.infra.Auth/Logout":                authReason,
	"trpc.moox.infra.Auth/GetUserInfo":           authReason,
	"trpc.moox.infra.Auth/IssueRawSessionTicket": authReason,

	"trpc.moox.ops.Ssh/CreateSession":     sshReason,
	"trpc.moox.ops.Ssh/DisconnectSession": sshReason,
	"trpc.moox.ops.Ssh/ResizeWindow":      sshReason,
	"trpc.moox.ops.Ssh/SftpList":          sshReason,
	"trpc.moox.ops.Ssh/SftpMkdir":         sshReason,
	"trpc.moox.ops.Ssh/SftpDelete":        sshReason,

	"trpc.moox.ops.SecretMgr/CreateSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/UpdateSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/DeleteSecret":       secretWriteReason,
	"trpc.moox.ops.SecretMgr/ToggleSecretStatus": secretWriteReason,
	"trpc.moox.ops.SecretMgr/ExportSecrets":      "密钥整包导出带口令，moox-cli 无权调用，由用户在管理台操作",
	"trpc.moox.ops.SecretMgr/ImportSecrets":      "密钥整包导入带口令，moox-cli 无权调用，由用户在管理台操作",

	"trpc.moox.cloudfunction.CloudFunctionMgr/CreateCloudAccount": secretWriteReason + "（云账户引用密钥）",
	"trpc.moox.cloudfunction.CloudFunctionMgr/UpdateCloudAccount": secretWriteReason + "（云账户引用密钥）",

	"trpc.moox.storage.Metadata/CreateSpace": spaceDupReason,
	"trpc.moox.storage.Metadata/UpdateSpace": spaceDupReason,
	"trpc.moox.storage.Metadata/DeleteSpace": spaceDupReason,
	"trpc.moox.storage.Metadata/ListSpaces":  spaceDupReason,

	"trpc.moox.trade.TradeConsoleService/CreateTradingAccount":       tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/UpdateTradingAccount":       tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/SetLeverage":                tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/SyncTradingAccount":         tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/CreateLogicalAccount":       tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/UpdateLogicalAccount":       tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/AddLogicalAccountMember":    tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/RemoveLogicalAccountMember": tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/PauseLogicalAccount":        tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/ResumeLogicalAccount":       tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/FlattenLogicalAccount":      tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/PlaceManualOrder":           tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/SubmitOrder":                tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/CancelOrder":                tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/CreatePaperSimulation":      tradeWriteReason,
	"trpc.moox.trade.TradeConsoleService/ClosePaperSimulation":       tradeWriteReason,
}
