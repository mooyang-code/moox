package command

// 采集（CollectMgr）补齐：任务查询与修改、数据类型、标的同步状态、重采样预览、云账号与代码包。
// 采集的运行状态、补采、重采样作业、标的排除、采集方式、云函数等已有专用命令（collector status/kline/resample/subjects/ingest-mode/function）。
// 云账号的新建与修改在管理台做（初始账号由 setup init 登记），代码包上传用 collector function upload。

const collectMgr = "trpc.moox.collector.CollectMgr"

func init() {
	rpcGroupShort["collector data-type"] = "采集数据类型配置"
	rpcGroupShort["collector delist"] = "标的下架：手工登记（自动发现由 Collector 的下架检测负责）"
	rpcGroupShort["collector account"] = "云账号（只登记密钥 ID 与 COS 桶；新建与修改在管理台“数据采集 › 采集运行”的“运行状态”页签里的“云账号”对话框）"
	rpcGroupShort["collector package"] = "云函数代码包（上传用 collector function upload）"

	registerRPCSpecs(
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
		rpcSpec{Path: []string{"collector", "delist", "register"}, Destructive: true, NoSpace: true, Short: "手工登记标的下架，补漏报或演练（会发布“标的即将下架”事件，Trade 随即禁止买入，到 T−24h 自动卖出策略持有的仓位；默认只预览，--yes 前向用户确认标的和下架时间；演练用 --paper-only）", Service: collectMgr, Method: "RegisterDelistNotice",
			Flags: []rpcFlag{reqFlag("exchange", "exchange", "交易所，小写：binance、okx"), reqFlag("market", "market", "市场：spot 现货、swap 永续"), reqFlag("symbol", "exchange_symbol", "交易所代码，如 FOOUSDT、FOO-USDT-SWAP"),
				instantFlag("delist-at", "delist_at", "下架时刻（RFC3339，如 2026-12-10T08:00:00Z）", true), boolFlag("paper-only", "paper_only", "仅模拟盘：只处理模拟盘组合，用于演练")}},
		rpcSpec{Path: []string{"collector", "account", "list"}, Short: "列出云账号（不含密钥明文）", Service: collectMgr, Method: "ListCloudAccounts", NoSpace: true},
		rpcSpec{Path: []string{"collector", "account", "regions"}, Short: "列出腾讯云函数可用的地域", Service: collectMgr, Method: "ListCloudRegions", NoSpace: true},
		rpcSpec{Path: []string{"collector", "account", "delete"}, Destructive: true, NoSpace: true, Service: collectMgr, Method: "DeleteCloudAccount",
			Short: "删除云账号登记（默认只预览，--yes 才执行；还被 scf_plan 引用、还有代码包或名下还有函数时拒绝。旧账号下建的函数 collector function delete 删不到，要用 setup inspect-scf 查出来再到腾讯云控制台手工删）",
			Flags: []rpcFlag{reqFlag("account-id", "account_id", "云账号 ID")}},
		rpcSpec{Path: []string{"collector", "package", "list"}, Short: "列出空间的云函数代码包（按上传先后倒序，每个包名保留最新 3 个）", Service: collectMgr, Method: "ListFunctionPackages", SpaceField: "space_id"},
		rpcSpec{Path: []string{"collector", "package", "delete"}, Destructive: true, Short: "删除代码包（先删 COS 对象再删记录；默认只预览，--yes 才执行）", Service: collectMgr, Method: "DeleteFunctionPackage", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("package-id", "package_id", "代码包 ID")}},
		rpcSpec{Path: []string{"collector", "resample", "preview"}, Short: "预览合成的源任务与目标频率是否可行", Service: collectMgr, Method: "PreviewKlineResampleSource", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("source-task-id", "source_task_id", "源采集任务 ID"), reqFlag("target-frequency", "target_frequency", "目标频率，如 5m")}},
	)
}
