package command

// 数据归档（ArchiveMgr）：总览、任务、运行与分区。

const archiveMgrService = "trpc.moox.archive.ArchiveMgr"

func init() {
	rpcGroupShort["archive"] = "数据归档：任务、运行与分区（调用 ArchiveMgr）"
	rpcGroupShort["archive tasks"] = "归档任务"
	rpcGroupShort["archive runs"] = "归档运行"
	rpcGroupShort["archive partitions"] = "归档分区"

	datasetID := reqFlag("dataset-id", "dataset_id", "数据集 ID")
	runID := reqFlag("run-id", "run_id", "运行 ID")

	registerRPCSpecs(
		rpcSpec{Path: []string{"archive", "overview"}, Short: "worker、正在执行的运行与告警总览", Service: archiveMgrService, Method: "GetArchiveOverview", NoSpace: true},

		rpcSpec{Path: []string{"archive", "tasks", "list"}, Short: "列出归档任务（--space-id 为空表示全部空间）", Service: archiveMgrService, Method: "ListArchiveTasks", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("space-id", "space_id", "空间 ID（留空表示全部空间）")}},
		rpcSpec{Path: []string{"archive", "tasks", "prepare"}, Short: "查看数据集的频率、保留期、最早回补起点与建议的目录名", Service: archiveMgrService, Method: "PrepareArchiveTask", SpaceField: "space_id", SpaceRequired: true,
			Flags: []rpcFlag{datasetID, strFlag("dir-name", "dir_name", "检查这个目录名是否已被同空间同用途的其他任务使用；为空检查建议的目录名")}},
		rpcSpec{Path: []string{"archive", "tasks", "create"}, Short: "创建归档任务（之后由 worker 每分钟按 Storage 变更日志本地跟随；首次运行从回补起点整段读到现在）", Service: archiveMgrService, Method: "CreateArchiveTask", SpaceField: "space_id", SpaceRequired: true,
			Flags: []rpcFlag{datasetID,
				timeFlag("backfill-from", "backfill_from", "回补起点（归档起点），RFC3339，建好后不能改；为空取线上保留期起点；保留期为 forever 时必填", false),
				boolFlag("disabled", "disabled", "创建后先停用"),
				strFlag("dir-name", "dir_name", "目录名（小写字母、数字与连字符，最长 64）；为空取 prepare 给出的建议值。建好后不能改")}},
		rpcSpec{Path: []string{"archive", "tasks", "update"}, Short: "启停归档任务", Service: archiveMgrService, Method: "UpdateArchiveTask", SpaceField: "space_id", SpaceRequired: true,
			Flags: []rpcFlag{datasetID,
				strFlag("status", "status", "enabled 或 disabled；重新启用后从原位点继续跟随")}},
		rpcSpec{Path: []string{"archive", "tasks", "delete"}, Short: "删除没有分区的任务（默认只预览，--yes 才执行）", Service: archiveMgrService, Method: "DeleteArchiveTask", SpaceField: "space_id", SpaceRequired: true, Destructive: true,
			Flags: []rpcFlag{datasetID}},
		rpcSpec{Path: []string{"archive", "tasks", "run"}, Short: "整段重读线上保留期内的全部数据（之后每分钟的跟随分段读完，数据量大时要几个小时）", Service: archiveMgrService, Method: "RunArchiveTaskNow", SpaceField: "space_id", SpaceRequired: true,
			Flags: []rpcFlag{datasetID}},
		rpcSpec{Path: []string{"archive", "tasks", "backfill"}, Short: "补拉指定数据时间区间", Service: archiveMgrService, Method: "BackfillArchiveRange", SpaceField: "space_id", SpaceRequired: true,
			Flags: []rpcFlag{datasetID,
				timeFlag("from", "range_start", "起始数据时间（含），RFC3339", true),
				timeFlag("to", "range_end", "结束数据时间（不含），RFC3339", true)}},
		rpcSpec{Path: []string{"archive", "tasks", "clear-unrecoverable"}, Short: "清除任务累积的无法补齐区间（--start 与 --end 都留空清空全部，都填写则删掉整段落在 [start, end) 里的区间；默认只预览，--yes 才执行）", Service: archiveMgrService, Method: "ClearArchiveUnrecoverable", SpaceField: "space_id", SpaceRequired: true, Destructive: true,
			Flags: []rpcFlag{datasetID,
				timeFlag("start", "start", "起点（含），RFC3339；与 --end 同时留空表示清空全部", false),
				timeFlag("end", "end", "终点（不含），RFC3339；与 --start 同时留空表示清空全部", false)}},

		rpcSpec{Path: []string{"archive", "runs", "list"}, Short: "列出归档运行记录（manual、range、import）", Service: archiveMgrService, Method: "ListArchiveRuns", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("space-id", "space_id", "空间 ID（留空表示全部空间）"), strFlag("dataset-id", "dataset_id", "数据集 ID"),
				strFlag("status", "status", "queued、running、succeeded、failed、cancelled")}},
		rpcSpec{Path: []string{"archive", "runs", "get"}, Short: "查看一次运行（含无法补齐的区间）", Service: archiveMgrService, Method: "GetArchiveRun", NoSpace: true,
			Flags: []rpcFlag{runID}},
		rpcSpec{Path: []string{"archive", "runs", "cancel"}, Short: "取消排队中或运行中的运行", Service: archiveMgrService, Method: "CancelArchiveRun", NoSpace: true,
			Flags: []rpcFlag{runID}},

		rpcSpec{Path: []string{"archive", "partitions", "list"}, Short: "列出分区", Service: archiveMgrService, Method: "ListArchivePartitions", SpaceField: "space_id", SpaceRequired: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("dataset-id", "dataset_id", "数据集 ID"), strFlag("from-month", "from_month", "起始月份 YYYY-MM"), strFlag("to-month", "to_month", "结束月份 YYYY-MM")}},
		rpcSpec{Path: []string{"archive", "partitions", "get"}, Short: "查看分区详情（状态、文件、sha256、预览）", Service: archiveMgrService, Method: "GetArchivePartition", SpaceField: "space_id", SpaceRequired: true,
			Flags: []rpcFlag{datasetID, reqFlag("partition-key", "partition_key", "分区键（日分区 2006-01-02，月分区 2006-01）")}},
	)
}
