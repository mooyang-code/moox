package command

// 采集（CollectMgr）补齐：任务查询与修改、数据类型、标的同步状态与重采样预览。
// 采集的运行状态、补采、重采样作业、标的排除、采集方式等已有专用命令（collector status/kline/resample/subjects/ingest-mode）。

const collectMgr = "trpc.moox.collector.CollectMgr"

func init() {
	rpcGroupShort["collector data-type"] = "采集数据类型配置"

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
		rpcSpec{Path: []string{"collector", "task", "inventory"}, Short: "查看采集结果清单（任务拥有的结果数据集与 View）", Service: collectMgr, Method: "GetTaskResultInventory", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("snapshot-id", "snapshot_id", "指定清单快照 ID")}},

		rpcSpec{Path: []string{"collector", "data-type", "list"}, Short: "列出采集数据类型", Service: collectMgr, Method: "GetDataTypeConfigs", NoSpace: true},
		rpcSpec{Path: []string{"collector", "data-type", "show"}, Short: "查看数据类型及其字段", Service: collectMgr, Method: "GetDataTypeConfigWithFields", NoSpace: true,
			Flags: []rpcFlag{reqFlag("data-type", "data_type", "数据类型")}},

		rpcSpec{Path: []string{"collector", "subjects", "status"}, Short: "各标签最近一次标的同步的状态", Service: collectMgr, Method: "ListTagSyncStatus", SpaceField: "space_id"},
		rpcSpec{Path: []string{"collector", "subjects", "listers"}, Short: "列出可用的标的来源（交易所列表器）", Service: collectMgr, Method: "ListSubjectListers", NoSpace: true},
		rpcSpec{Path: []string{"collector", "resample", "preview"}, Short: "预览合成的源任务与目标频率是否可行", Service: collectMgr, Method: "PreviewKlineResampleSource", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("source-task-id", "source_task_id", "源采集任务 ID"), reqFlag("target-frequency", "target_frequency", "目标频率，如 5m")}},
	)
}
