package command

// 空间与元数据：空间、数据来源、标的、标签、数据集、字段、View、设备、存储节点，以及行与记录的读取。

const (
	spaceMgr    = "trpc.moox.admin.SpaceMgr"
	metadataSvc = "trpc.moox.storage.Metadata"
	dataViewSvc = "trpc.moox.storage.DataView"
)

func init() {
	rpcGroupShort["space"] = "空间：一组相互隔离的业务数据（调用 SpaceMgr）"
	rpcGroupShort["metadata source"] = "数据来源"
	rpcGroupShort["metadata subject"] = "标的"
	rpcGroupShort["metadata dataset"] = "数据集"
	rpcGroupShort["metadata field"] = "字段"
	rpcGroupShort["metadata field-group"] = "字段分组"
	rpcGroupShort["metadata view"] = "View（面向查询的宽表，由 Storage 异步构建）"
	rpcGroupShort["metadata device"] = "View 使用的存储设备"
	rpcGroupShort["data records"] = "记录型数据读取"
	rpcGroupShort["data view"] = "按 View 查询数据"

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
		strFlag("data-node-id", "dataset.data_node_id", "绑定的存储节点"),
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
	viewFlags := []rpcFlag{
		reqFlag("view-id", "view.view_id", "View ID"),
		strFlag("name", "view.name", "名称"),
		strFlag("description", "view.description", "说明"),
		strFlag("dataset-id", "view.dataset_id", "主数据集 ID"),
		listFlag("grain-key", "view.grain_keys", "主键维度，可重复"),
		strFlag("frequency", "view.freq", "频率"),
		strFlag("engine", "view.engine", "引擎：duckdb、bleve"),
	}
	registerRPCSpecs(
		rpcSpec{Path: []string{"space", "list"}, Short: "列出空间", Service: spaceMgr, Method: "ListSpaces", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("owner", "owner", "按所有者过滤"), strFlag("status", "status", "按状态过滤")}},
		rpcSpec{Path: []string{"space", "show"}, Short: "查看 Storage 里一个空间的元数据", Service: metadataSvc, Method: "GetSpace", SpaceField: "space_id"},
		rpcSpec{Path: []string{"space", "create"}, Short: "新建空间（还要在 Storage 建元数据：数据来源、数据集、字段，见 metadata apply）", Service: spaceMgr, Method: "CreateSpace", NoSpace: true, Flags: spaceFlags},
		rpcSpec{Path: []string{"space", "update"}, Merge: mergeSpace, Short: "修改空间", Service: spaceMgr, Method: "UpdateSpace", NoSpace: true, Flags: spaceFlags},
		rpcSpec{Path: []string{"space", "delete"}, Short: "删除空间（默认只预览，--yes 才执行）", Service: spaceMgr, Method: "DeleteSpace", NoSpace: true, Destructive: true,
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
			Flags: []rpcFlag{strFlag("data-source-id", "data_source_id", "按数据来源过滤"), strFlag("data-kind", "data_kind", "按数据类型过滤"), strFlag("frequency", "freq", "按频率过滤"), strFlag("data-node-id", "data_node_id", "按存储节点过滤")}},
		rpcSpec{Path: []string{"metadata", "dataset", "show"}, Short: "查看数据集", Service: metadataSvc, Method: "GetDataset", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},
		rpcSpec{Path: []string{"metadata", "dataset", "columns"}, Short: "列出数据集的列", Service: metadataSvc, Method: "ListDatasetColumns", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},
		rpcSpec{Path: []string{"metadata", "dataset", "subjects"}, Short: "列出数据集里有数据的标的", Service: metadataSvc, Method: "ListDatasetSubjects", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), strFlag("subject-id", "subject_id", "只看这个标的")}},
		rpcSpec{Path: []string{"metadata", "dataset", "activation"}, Short: "检查数据集能否激活", Service: metadataSvc, Method: "CheckDatasetActivation", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},
		rpcSpec{Path: []string{"metadata", "dataset", "create"}, Short: "创建数据集（保留期不在这里设：由 Storage 的保留策略文件按空间与频率决定）", Service: metadataSvc, Method: "CreateDataset", SpaceField: "dataset.space_id", Flags: datasetFlagsRequiringName},
		rpcSpec{Path: []string{"metadata", "dataset", "update"}, Merge: mergeViaGet("GetDataset", map[string]string{"space_id": "dataset.space_id", "dataset_id": "dataset.dataset_id"}, "dataset", "dataset", "space_id", "dataset_id", "name", "description", "attributes", "subject_tags"), Short: "修改数据集的名称、说明与状态（数据来源、类型、频率、存储节点创建后不可变，换节点用 rebind）", Service: metadataSvc, Method: "UpdateDataset", SpaceField: "dataset.space_id", Flags: datasetFlags},
		rpcSpec{Path: []string{"metadata", "dataset", "add-column"}, Short: "给数据集加列（或修改列）", Service: metadataSvc, Method: "UpsertDatasetColumn", SpaceField: "column.space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "column.dataset_id", "数据集 ID"), reqFlag("column-name", "column.column_name", "列名"), strFlag("origin-type", "column.origin_type", "来源类型"),
				strFlag("origin-id", "column.origin_id", "来源 ID（字段 ID）"), strFlag("value-type", "column.value_type", "值类型"), boolFlag("required", "column.required", "是否必填"), strFlag("status", "column.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "dataset", "activate"}, Short: "激活数据集", Service: metadataSvc, Method: "ActivateDataset", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), intFlag("expected-revision", "expected_revision", "期望的版本号（乐观锁）")}},
		rpcSpec{Path: []string{"metadata", "dataset", "rebind"}, Short: "把数据集换绑到另一个存储节点（默认只预览，--yes 才执行）", Service: metadataSvc, Method: "RebindDatasetDataNode", SpaceField: "space_id", Destructive: true,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID"), reqFlag("data-node-id", "data_node_id", "目标存储节点"), intFlag("expected-revision", "expected_revision", "期望的版本号（乐观锁）")}},
		rpcSpec{Path: []string{"metadata", "dataset", "delete"}, Short: "删除数据集的元数据（默认只预览，--yes 才执行；删除采集结果用 collector task delete-result）", Service: metadataSvc, Method: "DeleteDataset", SpaceField: "space_id", Destructive: true,
			Flags: []rpcFlag{reqFlag("dataset-id", "dataset_id", "数据集 ID")}},

		rpcSpec{Path: []string{"metadata", "field", "list"}, Short: "列出字段", Service: metadataSvc, Method: "ListFields", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("value-type", "value_type", "按值类型过滤"), strFlag("group-id", "group_id", "按分组过滤"), strFlag("status", "status", "按状态过滤"), strFlag("keyword", "keyword", "关键字"),
				boolFlag("include-descendants", "include_descendants", "含子分组"), boolFlag("ungrouped-only", "ungrouped_only", "只列未分组的")}},
		rpcSpec{Path: []string{"metadata", "field", "show"}, Short: "查看字段", Service: metadataSvc, Method: "GetField", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("field-id", "field_id", "字段 ID")}},
		rpcSpec{Path: []string{"metadata", "field", "create"}, Short: "创建字段", Service: metadataSvc, Method: "CreateField", SpaceField: "field.space_id",
			Flags: []rpcFlag{reqFlag("field-id", "field.field_id", "字段 ID"), strFlag("name", "field.name", "名称"), strFlag("description", "field.description", "说明"), strFlag("value-type", "field.value_type", "值类型"),
				strFlag("unit", "field.unit", "单位"), strFlag("group-id", "field.group_id", "分组"), strFlag("status", "field.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "field", "update"}, Merge: mergeViaGet("GetField", map[string]string{"space_id": "field.space_id", "field_id": "field.field_id"}, "field", "field"), Short: "修改字段", Service: metadataSvc, Method: "UpdateField", SpaceField: "field.space_id",
			Flags: []rpcFlag{reqFlag("field-id", "field.field_id", "字段 ID"), strFlag("name", "field.name", "名称"), strFlag("description", "field.description", "说明"), strFlag("value-type", "field.value_type", "值类型"),
				strFlag("unit", "field.unit", "单位"), strFlag("group-id", "field.group_id", "分组"), strFlag("status", "field.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "field", "batch-update"}, Short: "批量调整字段的分组或状态", Service: metadataSvc, Method: "BatchUpdateFields", SpaceField: "space_id",
			Flags: []rpcFlag{listFlag("field-id", "field_ids", "字段 ID，可重复"), strFlag("target-group-id", "target_group_id", "目标分组"), strFlag("target-status", "target_status", "目标状态")}},

		rpcSpec{Path: []string{"metadata", "field-group", "list"}, Short: "列出字段分组", Service: metadataSvc, Method: "ListFieldGroups", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("parent-group-id", "parent_group_id", "只列这个分组的子分组")}},
		rpcSpec{Path: []string{"metadata", "field-group", "show"}, Short: "查看字段分组", Service: metadataSvc, Method: "GetFieldGroup", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("group-id", "group_id", "分组 ID")}},
		rpcSpec{Path: []string{"metadata", "field-group", "create"}, Short: "创建字段分组", Service: metadataSvc, Method: "CreateFieldGroup", SpaceField: "field_group.space_id",
			Flags: []rpcFlag{reqFlag("group-id", "field_group.group_id", "分组 ID"), strFlag("name", "field_group.name", "名称"), strFlag("description", "field_group.description", "说明"), strFlag("parent-group-id", "field_group.parent_group_id", "父分组")}},
		rpcSpec{Path: []string{"metadata", "field-group", "update"}, Merge: mergeViaGet("GetFieldGroup", map[string]string{"space_id": "field_group.space_id", "group_id": "field_group.group_id"}, "field_group", "field_group"), Short: "修改字段分组", Service: metadataSvc, Method: "UpdateFieldGroup", SpaceField: "field_group.space_id",
			Flags: []rpcFlag{reqFlag("group-id", "field_group.group_id", "分组 ID"), strFlag("name", "field_group.name", "名称"), strFlag("description", "field_group.description", "说明"), strFlag("parent-group-id", "field_group.parent_group_id", "父分组")}},
		rpcSpec{Path: []string{"metadata", "field-group", "delete"}, Short: "删除字段分组（默认只预览，--yes 才执行）", Service: metadataSvc, Method: "DeleteFieldGroup", SpaceField: "space_id", Destructive: true,
			Flags: []rpcFlag{reqFlag("group-id", "group_id", "分组 ID")}},

		rpcSpec{Path: []string{"metadata", "view", "list"}, Short: "列出 View", Service: metadataSvc, Method: "ListViews", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("dataset-id", "dataset_id", "按数据集过滤"), strFlag("status", "status", "按状态过滤")}},
		rpcSpec{Path: []string{"metadata", "view", "show"}, Short: "查看 View", Service: metadataSvc, Method: "GetView", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("view-id", "view_id", "View ID")}},
		rpcSpec{Path: []string{"metadata", "view", "columns"}, Short: "列出 View 的列", Service: metadataSvc, Method: "ListViewColumns", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("view-id", "view_id", "View ID")}},
		rpcSpec{Path: []string{"metadata", "view", "create"}, Short: "创建 View（会增加存储机内存占用，先按 skill 的 storage.md 估算）", Service: metadataSvc, Method: "CreateView", SpaceField: "view.space_id",
			Flags: append(append([]rpcFlag{}, viewFlags...), boolFlag("create-only", "create_only", "只允许新建，已存在时报错"))},
		rpcSpec{Path: []string{"metadata", "view", "update"}, Merge: mergeViaGet("GetView", map[string]string{"space_id": "view.space_id", "view_id": "view.view_id"}, "view", "view", "space_id", "view_id", "name", "description", "dataset_id", "grain_keys", "freq", "engine", "attributes", "hidden_columns"), Short: "修改 View 定义", Service: metadataSvc, Method: "UpdateView", SpaceField: "view.space_id",
			Flags: append(append([]rpcFlag{}, viewFlags...), boolFlag("replace-columns", "replace_columns", "用请求里的列整体替换"))},
		rpcSpec{Path: []string{"metadata", "view", "add-column"}, Short: "给 View 加列（会增加存储机内存占用）", Service: metadataSvc, Method: "UpsertViewColumn", SpaceField: "column.space_id",
			Flags: []rpcFlag{reqFlag("view-id", "column.view_id", "View ID"), reqFlag("column-name", "column.column_name", "列名"), strFlag("origin-type", "column.origin_type", "来源类型"),
				strFlag("origin-id", "column.origin_id", "来源 ID"), strFlag("value-type", "column.value_type", "值类型")}},
		rpcSpec{Path: []string{"metadata", "view", "delete"}, Short: "删除 View（默认只预览，--yes 才执行；离线删除用 storage delete-view）", Service: metadataSvc, Method: "DeleteView", SpaceField: "space_id", Destructive: true,
			Flags: []rpcFlag{reqFlag("view-id", "view_id", "View ID")}},

		rpcSpec{Path: []string{"metadata", "device", "list"}, Short: "列出存储设备", Service: metadataSvc, Method: "ListDevices", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("engine", "engine", "按引擎过滤")}},
		rpcSpec{Path: []string{"metadata", "device", "show"}, Short: "查看存储设备", Service: metadataSvc, Method: "GetDevice", NoSpace: true,
			Flags: []rpcFlag{reqFlag("device-id", "device_id", "设备 ID")}},
		rpcSpec{Path: []string{"metadata", "device", "create"}, Short: "登记存储设备", Service: metadataSvc, Method: "CreateDevice", NoSpace: true,
			Flags: []rpcFlag{reqFlag("device-id", "device.device_id", "设备 ID"), strFlag("name", "device.name", "名称"), strFlag("engine", "device.engine", "引擎"), strFlag("endpoint", "device.endpoint", "地址"), strFlag("status", "device.status", "状态")}},
		rpcSpec{Path: []string{"metadata", "device", "update"}, Merge: mergeViaGet("GetDevice", map[string]string{"device_id": "device.device_id"}, "device", "device"), Short: "修改存储设备", Service: metadataSvc, Method: "UpdateDevice", NoSpace: true,
			Flags: []rpcFlag{reqFlag("device-id", "device.device_id", "设备 ID"), strFlag("name", "device.name", "名称"), strFlag("engine", "device.engine", "引擎"), strFlag("endpoint", "device.endpoint", "地址"), strFlag("status", "device.status", "状态")}},

		rpcSpec{Path: []string{"storage", "node", "list"}, Short: "列出存储节点登记", Service: metadataSvc, Method: "ListDataNodes", NoSpace: true, Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("status", "status", "按状态过滤")}},
		rpcSpec{Path: []string{"storage", "node", "show"}, Short: "查看存储节点登记", Service: metadataSvc, Method: "GetDataNode", NoSpace: true,
			Flags: []rpcFlag{reqFlag("node-id", "node_id", "存储节点 ID")}},

		rpcSpec{Path: []string{"data", "records", "search"}, Short: "检索记录型 View（Bleve）的数据", Service: dataViewSvc, Method: "SearchRecordRows", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("view-id", "view_id", "View ID"), strFlag("text-query", "text_query", "全文检索语句"), listFlag("column", "column_names", "返回的列，可重复"), jsonFlag("filter", "filter", "过滤条件 JSON")}},
		rpcSpec{Path: []string{"data", "view", "query"}, Short: "按 View 查询时序行（管理台“数据浏览”用的接口；selectors、过滤与排序写在 --data 或对应参数里）", Service: dataViewSvc, Method: "QueryTimeSeriesRows", SpaceField: "space_id", Page: rpcPageSize,
			Flags: []rpcFlag{reqFlag("view-id", "view_id", "View ID"), jsonFlag("selectors", "selectors", `序列选择器 JSON，如 '[{"space_id":"crypto","dataset_id":"d","subject_id":"BTC-USDT","freq":"1m"}]'`),
				timeFlag("from", "time_range.start_time", "起点（RFC3339，含）", false), timeFlag("to", "time_range.end_time", "终点（RFC3339，不含）", false), listFlag("column", "column_names", "返回的列，可重复"),
				jsonFlag("filter", "filter", "过滤条件 JSON"), jsonFlag("sorts", "sorts", `排序 JSON，如 '[{"field_name":"data_time","desc":true}]'`), intFlag("limit", "limit", "最多返回多少行")}},
		rpcSpec{Path: []string{"data", "rows", "count"}, Short: "按 View 统计时序行数（selectors 与时间范围写在 --data 里）", Service: dataViewSvc, Method: "CountTimeSeriesRows", SpaceField: "space_id",
			Flags: []rpcFlag{reqFlag("view-id", "view_id", "View ID"), jsonFlag("selectors", "selectors", `序列选择器 JSON，如 '[{"space_id":"crypto","dataset_id":"d","subject_id":"BTC-USDT","freq":"1m"}]'`),
				timeFlag("from", "time_range.start_time", "起点（RFC3339）", false), timeFlag("to", "time_range.end_time", "终点（RFC3339）", false)}},
	)
}
