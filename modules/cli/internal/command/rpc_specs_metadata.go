package command

// 空间与元数据：空间、数据来源、标的、标签、数据集与列，以及按数据集读取行与记录（每个数据集恰好一个查询索引 View）。

const (
	metadataSvc = "trpc.moox.storage.Metadata"
	dataViewSvc = "trpc.moox.storage.DataView"
)

func init() {
	rpcGroupShort["space"] = "空间：一组相互隔离的业务数据（只登记在 Storage）"
	rpcGroupShort["metadata source"] = "数据来源"
	rpcGroupShort["metadata subject"] = "标的"
	rpcGroupShort["metadata dataset"] = "数据集"
	rpcGroupShort["data records"] = "记录型数据读取"
	rpcGroupShort["data view"] = "按数据集查询数据（读它的查询索引 View）"

	spaceFlags := []rpcFlag{
		reqFlag("space-id", "space.space_id", "空间 ID"),
		strFlag("name", "space.name", "名称"),
		strFlag("description", "space.description", "说明"),
		strFlag("owner", "space.owner", "所有者"),
		strFlag("market", "space.market", "市场：crypto、cn……"),
		strFlag("timezone", "space.timezone", "时区，如 Asia/Shanghai"),
		strFlag("status", "space.status", "状态：active、disabled"),
	}
	datasetFlags := []rpcFlag{
		reqFlag("dataset-id", "dataset.dataset_id", "数据集 ID"),
		strFlag("data-source-id", "dataset.data_source_id", "数据来源 ID"),
		strFlag("name", "dataset.name", "名称"),
		strFlag("description", "dataset.description", "说明"),
		strFlag("data-kind", "dataset.data_kind", "数据类型，如 DATA_KIND_TIME_SERIES"),
		strFlag("frequency", "dataset.freq", "频率"),
		strFlag("status", "dataset.status", "状态"),
		intFlag("maintenance-priority", "dataset.maintenance_priority", "查询索引维护的先后：越小越先，0（默认）最先"),
	}
	datasetFlagsRequiringName := func() []rpcFlag {
		flags := append([]rpcFlag{}, datasetFlags...)
		for i := range flags {
			if flags[i].Name == "name" {
				flags[i].Required = true
			}
		}
		return flags
	}()
	registerRPCSpecs(
		rpcSpec{Path: []string{"space", "list"}, Short: "列出空间", Service: metadataSvc, Method: "ListSpaces", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("owner", "owner", "按所有者过滤"), strFlag("status", "status", "按状态过滤：active、disabled")}},
		rpcSpec{Path: []string{"space", "show"}, Short: "查看一个空间", Service: metadataSvc, Method: "GetSpace", SpaceField: "space_id"},
		rpcSpec{Path: []string{"space", "create"}, Short: "新建空间（建好即可建数据来源、数据集等元数据，见 metadata apply）", Service: metadataSvc, Method: "CreateSpace", NoSpace: true, Flags: spaceFlags},
		rpcSpec{Path: []string{"space", "update"}, Merge: mergeViaGet("GetSpace", map[string]string{"space_id": "space.space_id"}, "space", "space"), Short: "修改空间（停用后 Collector 不再采它）", Service: metadataSvc, Method: "UpdateSpace", NoSpace: true, Flags: spaceFlags},
		rpcSpec{Path: []string{"space", "delete"}, Short: "删除空间：会清空该空间全部数据（数据集、查询索引与全部行情、因子数据），不能恢复；采集任务、交易、策略里按该空间存的记录不跟着删（默认只预览，--yes 才执行）", Service: metadataSvc, Method: "DeleteSpace", NoSpace: true, Destructive: true,
			Flags: []rpcFlag{reqFlag("space-id", "space_id", "空间 ID")}},

		rpcSpec{Path: []string{"metadata", "source", "list"}, Short: "列出数据来源", Service: metadataSvc, Method: "ListDataSources", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("keyword", "keyword", "关键字")}},
		rpcSpec{Path: []string{"metadata", "source", "show"}, Short: "查看数据来源", Service: metadataSvc, Method: "GetDataSource", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("data-source-id", "data_source_id", "数据来源 ID")}},
		rpcSpec{Path: []string{"metadata", "source", "create"}, Short: "创建数据来源", Service: metadataSvc, Method: "CreateDataSource", SpaceField: "data_source.space_id",
			Flags: []rpcFlag{reqFlag("data-source-id", "data_source.data_source_id", "数据来源 ID"), strFlag("name", "data_source.name", "名称"), strFlag("status", "data_source.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "source", "update"}, Merge: mergeViaGet("GetDataSource", map[string]string{"space_id": "data_source.space_id", "data_source_id": "data_source.data_source_id"}, "data_source", "data_source"), Short: "修改数据来源", Service: metadataSvc, Method: "UpdateDataSource", SpaceField: "data_source.space_id",
			Flags: []rpcFlag{reqFlag("data-source-id", "data_source.data_source_id", "数据来源 ID"), strFlag("name", "data_source.name", "名称"), strFlag("status", "data_source.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "source", "delete"}, Short: "删除数据来源（默认只预览，--yes 才执行）", Service: metadataSvc, Method: "DeleteDataSource", SpaceField: "space_id", Destructive: true,
			Flags: []rpcFlag{reqFlag("data-source-id", "data_source_id", "数据来源 ID")}},

		rpcSpec{Path: []string{"metadata", "subject", "list"}, Short: "列出标的", Service: metadataSvc, Method: "ListSubjects", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{listFlag("subject-id", "subject_ids", "只列这些标的，可重复"), strFlag("keyword", "keyword", "关键字")}},
		rpcSpec{Path: []string{"metadata", "subject", "show"}, Short: "查看标的", Service: metadataSvc, Method: "GetSubject", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("subject-id", "subject_id", "标的 ID")}},
		rpcSpec{Path: []string{"metadata", "subject", "upsert"}, Short: "新建或修改标的", Service: metadataSvc, Method: "UpsertSubject", SpaceField: "subject.space_id",
			Flags: []rpcFlag{reqFlag("subject-id", "subject.subject_id", "标的 ID"), strFlag("name", "subject.name", "名称"), strFlag("timezone", "subject.timezone", "时区"), strFlag("status", "subject.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "subject", "resolve"}, Short: "把标签展开成标的", Service: metadataSvc, Method: "ResolveSubjects", SpaceField: "space_id",
			Flags: []rpcFlag{listFlag("tag-id", "tag_ids", "标签 ID，可重复")}},

		rpcSpec{Path: []string{"metadata", "tag", "list"}, Short: "列出标签", Service: metadataSvc, Method: "ListTags", SpaceField: "space_id", Page: rpcPageSize},
		rpcSpec{Path: []string{"metadata", "tag", "show"}, Short: "查看标签", Service: metadataSvc, Method: "GetTag", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("tag-id", "tag_id", "标签 ID")}},
		rpcSpec{Path: []string{"metadata", "tag", "upsert"}, Short: "新建或修改标签", Service: metadataSvc, Method: "UpsertTag", SpaceField: "tag.space_id",
			Flags: []rpcFlag{reqFlag("tag-id", "tag.tag_id", "标签 ID"), strFlag("name", "tag.tag_name", "名称"), strFlag("description", "tag.description", "说明"), strFlag("mode", "tag.mode", "模式：manual 等"),
				boolFlag("create-only", "create_only", "只允许新建，已存在时报错")}},
		rpcSpec{Path: []string{"metadata", "tag", "delete"}, Short: "删除标签（被数据集引用时会被拒绝；默认只预览，--yes 才执行）", Service: metadataSvc, Method: "DeleteTag", SpaceField: "space_id", Destructive: true,
			Flags: []rpcFlag{reqFlag("tag-id", "tag_id", "标签 ID")}},
		rpcSpec{Path: []string{"metadata", "tag", "list-members"}, Short: "列出标签成员", Service: metadataSvc, Method: "ListTagMembers", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("tag-id", "tag_id", "标签 ID"), strFlag("status", "status", "按成员状态过滤"), strFlag("keyword", "keyword", "关键字")}},
		rpcSpec{Path: []string{"metadata", "tag", "member-status"}, Short: "启用或停用标签成员（--status active|inactive）", Service: metadataSvc, Method: "SetTagMemberStatus", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("tag-id", "tag_id", "标签 ID"), listFlag("subject-id", "subject_ids", "标的，可重复"), reqFlag("status", "status", "状态：active、inactive")}},

		rpcSpec{Path: []string{"metadata", "dataset", "list"}, Short: "列出数据集", Service: metadataSvc, Method: "ListDatasets", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("data-source-id", "data_source_id", "按数据来源过滤"), strFlag("data-kind", "data_kind", "按数据类型过滤"), strFlag("frequency", "freq", "按频率过滤"), strFlag("status", "status", "按状态过滤：active、disabled")}},
		rpcSpec{Path: []string{"metadata", "dataset", "show"}, Short: "查看数据集", Service: metadataSvc, Method: "GetDataset", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},
		rpcSpec{Path: []string{"metadata", "dataset", "columns"}, Short: "列出数据集的列", Service: metadataSvc, Method: "ListDatasetColumns", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},
		rpcSpec{Path: []string{"metadata", "dataset", "subjects"}, Short: "列出数据集里有数据的标的", Service: metadataSvc, Method: "ListDatasetSubjects", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), strFlag("subject-id", "subject_id", "只看这个标的")}},
		rpcSpec{Path: []string{"metadata", "dataset", "create"}, Short: "创建数据集（保留期不在这里设：由 Storage 的保留策略文件按空间与频率决定）", Service: metadataSvc, Method: "CreateDataset", SpaceField: "dataset.space_id", Flags: datasetFlagsRequiringName},
		rpcSpec{Path: []string{"metadata", "dataset", "update"}, Merge: mergeViaGet("GetDataset", map[string]string{"space_id": "dataset.space_id", "dataset_id": "dataset.dataset_id"}, "dataset", "dataset", "space_id", "dataset_id", "name", "description", "attributes", "subject_tags", "alignment", "allowed_lateness_seconds", "maintenance_priority"), Short: "修改数据集的名称、说明、状态与维护先后（数据来源、类型、频率创建后不可变；停用即摘掉它的查询索引，索引文件保留）", Service: metadataSvc, Method: "UpdateDataset", SpaceField: "dataset.space_id", Flags: datasetFlags},
		rpcSpec{Path: []string{"metadata", "dataset", "add-column"}, Short: "给数据集加列（或修改列）", Service: metadataSvc, Method: "UpsertDatasetColumn", SpaceField: "column.space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "column.dataset_id", "数据集 ID"), reqFlag("column-name", "column.column_name", "列名"), strFlag("display-name", "column.display_name", "中文显示名（新建列必填）"),
				strFlag("group", "column.group", "分组名（中文，一级）"), strFlag("value-type", "column.value_type", "值类型（建列后不能修改）"), boolFlag("required", "column.required", "是否必填"), strFlag("status", "column.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "dataset", "delete"}, Short: "删除数据集（服务端先删元数据再删行，查询索引由 storage-view 下一轮维护删掉；默认只预览，--yes 才执行；删除采集结果用 collector task delete-result）", Service: metadataSvc, Method: "DeleteDataset", SpaceField: "space_id", Destructive: true,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},

		rpcSpec{Path: []string{"data", "records", "search"}, Short: "检索记录型数据集（Bleve 索引）的数据", Service: dataViewSvc, Method: "SearchRecordRows", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), strFlag("text-query", "text_query", "全文检索语句"), listFlag("column", "column_names", "返回的列，可重复"), jsonFlag("filter", "filter", "过滤条件 JSON")}},
		rpcSpec{Path: []string{"data", "view", "query"}, Short: "按数据集查询时序行（读它的查询索引；管理台看采集结果、因子结果用的接口；selectors、过滤与排序写在 --data 或对应参数里）", Service: dataViewSvc, Method: "QueryTimeSeriesRows", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), jsonFlag("selectors", "selectors", `序列选择器 JSON，如 '[{"space_id":"crypto","dataset_id":"d","subject_id":"BTC-USDT","freq":"1m"}]'`),
				timeFlag("from", "time_range.start_time", "起点（RFC3339，含）", false), timeFlag("to", "time_range.end_time", "终点（RFC3339，不含）", false), listFlag("column", "column_names", "返回的列，可重复"),
				jsonFlag("filter", "filter", "过滤条件 JSON"), jsonFlag("sorts", "sorts", `排序 JSON，如 '[{"field_name":"data_time","desc":true}]'`), intFlag("limit", "limit", "最多返回多少行")}},
		rpcSpec{Path: []string{"data", "view", "state"}, Short: "查看数据集查询索引（View）的状态：活动索引、代次、已建列、覆盖范围与跟随变更日志的进度", Service: dataViewSvc, Method: "GetViewState", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},
		rpcSpec{Path: []string{"data", "view", "wait-applied"}, Short: "等数据集的查询索引跟上此刻的变更日志头部（之前的写入都已可查）", Service: dataViewSvc, Method: "WaitViewApplied", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), intFlag("wait-timeout-ms", "wait_timeout_ms", "最多等待的毫秒数，0 取 10000，上限 30000")}},
		rpcSpec{Path: []string{"data", "rows", "count"}, Short: "按数据集统计查询索引里的时序行数（selectors 与时间范围写在 --data 里）", Service: dataViewSvc, Method: "CountTimeSeriesRows", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), jsonFlag("selectors", "selectors", `序列选择器 JSON，如 '[{"space_id":"crypto","dataset_id":"d","subject_id":"BTC-USDT","freq":"1m"}]'`),
				timeFlag("from", "time_range.start_time", "起点（RFC3339）", false), timeFlag("to", "time_range.end_time", "终点（RFC3339）", false)}},
	)
}
