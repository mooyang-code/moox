package command

import (
	"os"
	"os/user"
)

// 采集（CollectMgr）规格命令：任务、运行状态、采集计划、K 线检查与补采、K 线合成、标的排除与状态、历史补齐、下架登记、数据类型、云账号与代码包。
// 另有含本地逻辑的手写命令：collector task create|delete|delete-result、collector fetch-plan pause、
// collector subjects sync、collector ingest-mode get|set、collector function package|upload|list|delete|probe-egress|inspect|attach-vpc|invoke。
// 云账号的新建与修改在管理台做（初始账号由 setup init 登记）。

const collectMgr = "trpc.moox.collector.CollectMgr"

// cliOperatorName 是写进采集名单排除与活动日志的操作人标识：本机用户与主机名。
func cliOperatorName() string {
	name := "unknown"
	if current, err := user.Current(); err == nil && current.Username != "" {
		name = current.Username
	}
	host, _ := os.Hostname()
	return "moox-cli:" + name + "@" + host
}

func init() {
	rpcGroupShort["collector data-type"] = "采集数据类型配置"
	rpcGroupShort["collector delist"] = "标的下架：手工登记（自动发现由 Collector 的下架检测负责）"
	rpcGroupShort["collector account"] = "云账号（只登记密钥 ID 与 COS 桶；新建与修改在管理台“数据采集 › 采集运行”的“运行状态”页签里的“云账号”对话框）"
	rpcGroupShort["collector package"] = "云函数代码包（上传用 collector function upload）"
	rpcGroupShort["collector kline"] = "K 线完整性检查与补采"
	rpcGroupShort["collector fetch-plan"] = "采集计划：查看、暂停、恢复与重新登记"
	rpcGroupShort["collector resample"] = "K 线合成：预览与历史合成作业"
	rpcGroupShort["collector history-seed"] = "首次接入的历史补齐：查看与手动放行"

	operatorFlag := rpcFlag{Name: "operator", Field: "operator", Kind: rpcString, Default: cliOperatorName(), Usage: "操作人（默认本机用户与主机名）"}
	exclusionSubjectsFlag := rpcFlag{Name: "subjects", Field: "subject_ids", Kind: rpcStrings, Required: true, Usage: "标的 ID，逗号分隔，例如 BTC-USDT,ETH-USDT"}

	registerRPCSpecs(
		rpcSpec{Path: []string{"collector", "status"}, Short: "查看空间的采集运行状态：总体健康、各数据集的自动巡检进度与缺口、补采汇总、首次接入的历史补齐进度", Service: collectMgr, Method: "GetCollectorStatus", SpaceField: "space_id"},

		rpcSpec{Path: []string{"collector", "fetch-plan", "show"}, Short: "查看空间的采集计划：函数分配与变更进度", Service: collectMgr, Method: "GetCollectorStatus", SpaceField: "space_id"},
		rpcSpec{Path: []string{"collector", "fetch-plan", "resume"}, Short: "恢复空间的采集：重新启用 SCF 定时器并继续采集计划对账与补采", Service: collectMgr, Method: "SetFetchPlanPaused", SpaceField: "space_id",
			Fixed: map[string]any{"paused": false}},
		rpcSpec{Path: []string{"collector", "fetch-plan", "reregister"}, Destructive: true, Short: "Storage 数据被清空后，让采集计划重新登记序列快照并重置补采状态（默认只预览，--yes 才执行）", Service: collectMgr, Method: "ReregisterFetchPlan", SpaceField: "space_id"},

		rpcSpec{Path: []string{"collector", "kline", "check"}, Short: "按采集计划检查时间范围内的 K 线是否齐全，输出缺口", Service: collectMgr, Method: "CheckKline", SpaceField: "space_id",
			Flags: []rpcFlag{strFlag("dataset-id", "dataset_id", "只处理指定数据集；留空表示空间内全部"), strFlag("subject", "subject_id", "只处理指定标的"),
				timeFlag("from", "from", "起始时间（含），RFC3339 或 UTC 日期", true), timeFlag("to", "to", "结束时间（不含），RFC3339 或 UTC 日期", true),
				boolFlag("deep", "deep", "校验每根 K 线的字段齐全")}},
		rpcSpec{Path: []string{"collector", "kline", "backfill"}, Short: "补采缺失的 K 线（默认补检查出的缺口，--force 强制重拉整个范围）", Service: collectMgr, Method: "SubmitKlineBackfill", SpaceField: "space_id",
			Flags: []rpcFlag{strFlag("dataset-id", "dataset_id", "只处理指定数据集；留空表示空间内全部"), strFlag("subject", "subject_id", "只处理指定标的"),
				timeFlag("from", "from", "起始时间（含），RFC3339 或 UTC 日期", true), timeFlag("to", "to", "结束时间（不含），RFC3339 或 UTC 日期", true),
				boolFlag("force", "force", "不检查缺口，范围内应有的 K 线全部重拉")}},
		rpcSpec{Path: []string{"collector", "kline", "backfill-list"}, Short: "列出空间最近的 K 线补采作业", Service: collectMgr, Method: "ListKlineBackfills", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("source", "source", "按来源筛选：manual、auto、seed、close_refetch 或 catchup"),
				boolFlag("attention", "attention", "只看未结束或有失败的作业"), strFlag("dataset-id", "dataset_id", "只看涉及该数据集的作业")}},
		rpcSpec{Path: []string{"collector", "kline", "backfill-status"}, Short: "查看单个 K 线补采作业的进度与失败分块", Service: collectMgr, Method: "GetKlineBackfill", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "补采作业 ID")}},
		rpcSpec{Path: []string{"collector", "kline", "backfill-cancel"}, Short: "取消补采作业（运行中的分块等其结束）", Service: collectMgr, Method: "CancelKlineBackfill", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "补采作业 ID")}},
		rpcSpec{Path: []string{"collector", "kline", "backfill-retry"}, Short: "重试已失败补采作业的失败分块", Service: collectMgr, Method: "RetryKlineBackfill", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "补采作业 ID")}},
		rpcSpec{Path: []string{"collector", "kline", "absences-clear"}, Short: "清掉数据集已确认的“行情源缺失”（行情源后来补上数据时），之后的检查与补采重新判断这些 K 线", Service: collectMgr, Method: "ClearSourceAbsences", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), reqFlag("frequency", "frequency", "频率，如 1m")}},

		rpcSpec{Path: []string{"collector", "resample", "preview"}, Short: "预览合成的源任务与目标频率是否可行", Service: collectMgr, Method: "PreviewKlineResampleSource", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("source-task-id", "source_task_id", "源采集任务 ID"), reqFlag("target-frequency", "target_frequency", "目标频率，如 5m")}},
		rpcSpec{Path: []string{"collector", "resample", "backfill"}, Short: "提交历史合成作业：把时间范围内的目标 K 线按源数据重新合成", Service: collectMgr, Method: "SubmitKlineResampleJob", SpaceField: "space_id", AutoIDs: []string{"request_id"},
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "合成任务 ID"),
				timeFlag("from", "start", "起始时间（含），RFC3339 或 UTC 日期", true), timeFlag("to", "end", "结束时间（不含），RFC3339 或 UTC 日期", true),
				listFlag("subjects", "subjects", "只合成这些标的（逗号分隔）；不指定表示全部"),
				strFlag("request-id", "request_id", "幂等键；同一个任务内重复提交返回已有作业；不指定时自动生成")}},
		rpcSpec{Path: []string{"collector", "resample", "jobs"}, Short: "列出空间的历史合成作业", Service: collectMgr, Method: "ListKlineResampleJobs", SpaceField: "space_id",
			Flags: []rpcFlag{strFlag("task-id", "task_id", "只看该合成任务的作业"), strFlag("status", "status", "只看该状态：queued、running、succeeded、failed、cancelled"),
				intFlag("limit", "limit", "最多列出多少个")}},
		rpcSpec{Path: []string{"collector", "resample", "show"}, Short: "查看单个历史合成作业的进度与跳过样例", Service: collectMgr, Method: "GetKlineResampleJob", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "作业 ID")}},
		rpcSpec{Path: []string{"collector", "resample", "cancel"}, Short: "取消排队或运行中的历史合成作业（已写入的保留）", Service: collectMgr, Method: "CancelKlineResampleJob", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "作业 ID")}},

		rpcSpec{Path: []string{"collector", "history-seed", "list"}, Short: "列出首次接入的历史补齐记录（--space-id 为空表示全部空间）", Service: collectMgr, Method: "ListHistorySeeds", NoSpace: true,
			Flags: []rpcFlag{strFlag("space-id", "space_id", "空间 ID（留空表示全部空间）"), boolFlag("only-running", "only_running", "只列出补齐中的数据集")}},
		rpcSpec{Path: []string{"collector", "history-seed", "skip"}, Short: "不等历史补齐了，立即放行下游：取消剩余分块，状态记为已手动放行，已经补进去的数据保留", Service: collectMgr, Method: "SkipHistorySeed", SpaceField: "space_id",
			Flags: []rpcFlag{strFlag("dataset-id", "dataset_id", "只放行指定数据集；留空表示空间内全部补齐中的数据集")}},

		rpcSpec{Path: []string{"collector", "task", "list"}, Short: "列出采集任务", Service: collectMgr, Method: "GetTaskList", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("data-type", "data_type", "按数据类型过滤"), boolFlag("enabled", "enabled", "按启用状态过滤（--enabled=false 只列停用的）"), strFlag("task-id", "task_id", "只看这个任务"), strFlag("keyword", "keyword", "关键字")}},
		rpcSpec{Path: []string{"collector", "task", "show"}, Short: "查看一个采集任务", Service: collectMgr, Method: "GetTaskDetail", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID")}},
		rpcSpec{Path: []string{"collector", "task", "update"}, Merge: mergeViaGet("GetTaskDetail", map[string]string{"space_id": "space_id", "task_id": "task_id"}, "task", "task", "task_id", "task_name", "description", "enabled"), Short: "修改采集任务的名称、说明或启用状态", Service: collectMgr, Method: "UpdateTask", SpaceField: "space_id",
			Flags: []rpcFlag{{Name: "task-id", Field: "task_id", AlsoField: "task.task_id", Kind: rpcString, Usage: "任务 ID", Required: true},
				strFlag("name", "task.task_name", "任务名称"), strFlag("description", "task.description", "说明"), boolFlag("enabled", "task.enabled", "启用状态（--enabled=false 停用）")}},
		rpcSpec{Path: []string{"collector", "task", "disable"}, Destructive: true, Short: "停用采集任务（默认只预览，--yes 才执行）", Service: collectMgr, Method: "DisableTask", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID")}},
		rpcSpec{Path: []string{"collector", "task", "inventory"}, Short: "查看采集结果清单（任务拥有的结果数据集）", Service: collectMgr, Method: "GetTaskResultInventory", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("snapshot-id", "snapshot_id", "指定清单快照 ID")}},

		rpcSpec{Path: []string{"collector", "data-type", "list"}, Short: "列出采集数据类型", Service: collectMgr, Method: "GetDataTypeConfigs", NoSpace: true},
		rpcSpec{Path: []string{"collector", "data-type", "show"}, Short: "查看数据类型及其字段", Service: collectMgr, Method: "GetDataTypeConfigWithFields", NoSpace: true,
			Flags: []rpcFlag{reqFlag("data-type", "data_type", "数据类型")}},

		rpcSpec{Path: []string{"collector", "subjects", "status"}, Short: "各标签最近一次标的同步的状态", Service: collectMgr, Method: "ListTagSyncStatus", SpaceField: "space_id"},
		rpcSpec{Path: []string{"collector", "subjects", "listers"}, Short: "列出可用的标的来源（交易所列表器）", Service: collectMgr, Method: "ListSubjectListers", NoSpace: true},
		rpcSpec{Path: []string{"collector", "subjects", "exclude"}, Short: "把标的从采集计划里临时去掉（交易所公告下架、数据异常等）", Service: collectMgr, Method: "SetSubjectExclusions", SpaceField: "space_id",
			Flags: []rpcFlag{exclusionSubjectsFlag, strFlag("market", "market_type", "市场（spot、swap 等）；不填表示该空间所有市场"),
				reqFlag("reason", "reason", "排除原因（必填）"), afterNowFlag("expires-in", "expires_at", "多久后自动失效，例如 72h；不填表示不过期"), operatorFlag}},
		rpcSpec{Path: []string{"collector", "subjects", "unexclude"}, Short: "解除排除：标的从新的生效周期起回到采集（期间的历史需要时用补采）", Service: collectMgr, Method: "ClearSubjectExclusions", SpaceField: "space_id",
			Flags: []rpcFlag{exclusionSubjectsFlag, strFlag("market", "market_type", "市场（spot、swap 等）；不填表示该空间所有市场"), operatorFlag}},
		rpcSpec{Path: []string{"collector", "subjects", "exclusions"}, Short: "列出空间当前生效的排除（设置人、原因、设置与到期时间）", Service: collectMgr, Method: "ListSubjectExclusions", SpaceField: "space_id"},

		rpcSpec{Path: []string{"collector", "delist", "register"}, Destructive: true, NoSpace: true, Short: "手工登记标的下架，补漏报或演练（会发布“标的即将下架”事件，Trade 随即禁止买入，到 T−24h 自动卖出策略持有的仓位；默认只预览，--yes 前向用户确认标的和下架时间；演练用 --paper-only）", Service: collectMgr, Method: "RegisterDelistNotice",
			Flags: []rpcFlag{reqFlag("exchange", "exchange", "交易所，小写：binance、okx"), reqFlag("market", "market", "市场：spot 现货、swap 永续"), reqFlag("symbol", "exchange_symbol", "交易所代码，如 FOOUSDT、FOO-USDT-SWAP"),
				instantFlag("delist-at", "delist_at", "下架时刻（RFC3339，如 2026-12-10T08:00:00Z）", true), boolFlag("paper-only", "paper_only", "仅模拟盘：只处理模拟盘组合，用于演练")}},
		rpcSpec{Path: []string{"collector", "account", "list"}, Short: "列出云账号（不含密钥明文）", Service: collectMgr, Method: "ListCloudAccounts", NoSpace: true},
		rpcSpec{Path: []string{"collector", "account", "regions"}, Short: "列出腾讯云函数可用的地域", Service: collectMgr, Method: "ListCloudRegions", NoSpace: true},
		rpcSpec{Path: []string{"collector", "account", "delete"}, Destructive: true, NoSpace: true, Service: collectMgr, Method: "DeleteCloudAccount",
			Short: "删除云账号登记（默认只预览，--yes 才执行；还被 scf_plan 引用、还有代码包或名下还有函数时拒绝。旧账号下建的函数 collector function delete 删不到，要用 collector function inspect 查出来再到腾讯云控制台手工删）",
			Flags: []rpcFlag{reqFlag("account-id", "account_id", "云账号 ID")}},
		rpcSpec{Path: []string{"collector", "package", "list"}, Short: "列出空间的云函数代码包（按上传先后倒序，每个包名保留最新 3 个）", Service: collectMgr, Method: "ListFunctionPackages", SpaceField: "space_id"},
		rpcSpec{Path: []string{"collector", "package", "delete"}, Destructive: true, Short: "删除代码包（先删 COS 对象再删记录；默认只预览，--yes 才执行）", Service: collectMgr, Method: "DeleteFunctionPackage", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("package-id", "package_id", "代码包 ID")}},
	)
}
