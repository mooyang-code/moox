package command

// 因子（FactorMgr）：定义、分类、计算任务、成员、补算与状态。

const factorMgr = "trpc.moox.factor.FactorMgr"

func init() {
	rpcGroupShort["factor"] = "因子定义、分类、计算任务、成员与补算（调用 FactorMgr）"
	rpcGroupShort["factor category"] = "因子分类"
	rpcGroupShort["factor task"] = "因子计算任务"
	rpcGroupShort["factor member"] = "计算任务的因子成员"
	rpcGroupShort["factor recalc"] = "历史补算作业"

	factorFlags := []rpcFlag{
		strFlag("factor-id", "factor.factor_id", "因子 ID"),
		strFlag("name", "factor.name", "因子名称"),
		strFlag("category-id", "factor.category_id", "所属分类 ID"),
		fileFlag("spec-file", "factor.spec", "因子表达式 YAML 文件；- 为标准输入", false),
		boolFlag("allow-partial-universe", "factor.allow_partial_universe", "截面因子允许标的不完整"),
	}
	registerRPCSpecs(
		rpcSpec{Path: []string{"factor", "list"}, Short: "列出因子定义", Service: factorMgr, Method: "ListFactors", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("task-id", "task_id", "只列这个计算任务的成员因子"), strFlag("status", "status", "按状态过滤"), boolFlag("include-spec", "include_spec", "返回表达式原文")}},
		rpcSpec{Path: []string{"factor", "show"}, Short: "查看一个因子定义", Service: factorMgr, Method: "GetFactor",
			Flags: []rpcFlag{reqFlag("factor-id", "factor_id", "因子 ID")}},
		rpcSpec{Path: []string{"factor", "create"}, Short: "创建因子定义（表达式写在 --spec-file 里，写法见 skill 的 factor-dsl）", Service: factorMgr, Method: "CreateFactor", Flags: factorFlags},
		rpcSpec{Path: []string{"factor", "update"}, Merge: mergeViaGet("GetFactor", map[string]string{"factor_id": "factor.factor_id"}, "factor", "factor", "factor_id", "name", "factor_type", "input_columns", "outputs", "lookback_periods", "allow_partial_universe", "spec", "category_id", "params", "udfs"), Short: "修改因子定义（被启用成员引用的定义不能改）", Service: factorMgr, Method: "UpdateFactor", Flags: factorFlags},
		rpcSpec{Path: []string{"factor", "delete"}, Short: "删除因子定义（默认只预览，--yes 才执行）", Service: factorMgr, Method: "DeleteFactor", Destructive: true,
			Flags: []rpcFlag{reqFlag("factor-id", "factor_id", "因子 ID")}},
		rpcSpec{Path: []string{"factor", "validate"}, Short: "线上校验因子表达式（不落库）", Service: factorMgr, Method: "ValidateFactorSpec",
			Flags: []rpcFlag{fileFlag("spec-file", "spec", "因子表达式 YAML 文件；- 为标准输入", true), strFlag("factor-id", "factor_id", "因子 ID（校验修改时带上）"),
				boolFlag("allow-partial-universe", "allow_partial_universe", "截面因子允许标的不完整"), jsonFlag("udf-versions", "udf_versions", "自定义算子版本 JSON，如 '{\"name\":\"hash\"}'")}},
		rpcSpec{Path: []string{"factor", "preview"}, Short: "在真实数据上试算因子表达式（不落库）", Service: factorMgr, Method: "PreviewFactor",
			Flags: []rpcFlag{strFlag("task-id", "task_id", "取数据的因子计算任务"), fileFlag("spec-file", "spec", "因子表达式 YAML 文件；- 为标准输入", true),
				listFlag("subject", "subjects", "展示的标的，可重复或逗号分隔"), intFlag("periods", "periods", "最近多少期"), strFlag("factor-id", "factor_id", "因子 ID"),
				boolFlag("allow-partial-universe", "allow_partial_universe", "截面因子允许标的不完整"), jsonFlag("params", "params", "成员参数 JSON"), jsonFlag("udf-versions", "udf_versions", "自定义算子版本 JSON")}},
		rpcSpec{Path: []string{"factor", "functions"}, Short: "列出表达式里可用的函数", Service: factorMgr, Method: "ListFactorFunctions"},
		rpcSpec{Path: []string{"factor", "status"}, Short: "因子运行状态（各任务的预热、排队与暂停）", Service: factorMgr, Method: "GetStatus"},

		rpcSpec{Path: []string{"factor", "category", "list"}, Short: "列出因子分类", Service: factorMgr, Method: "ListFactorCategories"},
		rpcSpec{Path: []string{"factor", "category", "upsert"}, Short: "新建或修改因子分类", Service: factorMgr, Method: "UpsertFactorCategory",
			Flags: []rpcFlag{reqFlag("category-id", "category.category_id", "分类 ID"), reqFlag("name", "category.name", "分类名称"), intFlag("sort-order", "category.sort_order", "排序")}},
		rpcSpec{Path: []string{"factor", "category", "delete"}, Short: "删除因子分类（默认只预览，--yes 才执行）", Service: factorMgr, Method: "DeleteFactorCategory", Destructive: true,
			Flags: []rpcFlag{reqFlag("category-id", "category_id", "分类 ID")}},
		rpcSpec{Path: []string{"factor", "category", "assign"}, Short: "把因子归入分类", Service: factorMgr, Method: "SetFactorCategory",
			Flags: []rpcFlag{reqFlag("factor-id", "factor_id", "因子 ID"), reqFlag("category-id", "category_id", "分类 ID")}},

		rpcSpec{Path: []string{"factor", "task", "list"}, Short: "列出因子计算任务", Service: factorMgr, Method: "ListFactorTasks", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("status", "status", "按状态过滤：enabled、disabled")}},
		rpcSpec{Path: []string{"factor", "task", "show"}, Short: "查看一个计算任务", Service: factorMgr, Method: "GetFactorTask",
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID")}},
		rpcSpec{Path: []string{"factor", "task", "create"}, Short: "创建计算任务（源数据集 + 频率 → 结果数据集）；批量配置优先写进 moox.toml 后用 setup factors", Service: factorMgr, Method: "CreateFactorTask",
			SpaceField: "factor_task.space_id",
			Flags:      []rpcFlag{reqFlag("source-dataset-id", "factor_task.source_dataset_id", "源数据集 ID"), reqFlag("frequency", "factor_task.freq", "频率：1m、5m、1h……"), strFlag("name", "factor_task.name", "任务名称")}},
		rpcSpec{Path: []string{"factor", "task", "enable"}, Short: "启用计算任务", Service: factorMgr, Method: "SetFactorTaskStatus", Fixed: map[string]any{"status": "enabled"},
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID")}},
		rpcSpec{Path: []string{"factor", "task", "disable"}, Destructive: true, Short: "停用计算任务（默认只预览，--yes 才执行）", Service: factorMgr, Method: "SetFactorTaskStatus", Fixed: map[string]any{"status": "disabled"},
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID")}},
		rpcSpec{Path: []string{"factor", "task", "delete"}, Short: "删除计算任务（--purge 连结果一起删，不可恢复；默认只预览，--yes 才执行）", Service: factorMgr, Method: "DeleteFactorTask", Destructive: true,
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID"), boolFlag("purge", "purge", "连同结果数据一起删除")}},
		rpcSpec{Path: []string{"factor", "task", "params"}, Short: "查看计算任务各成员当前的参数", Service: factorMgr, Method: "GetFactorParams",
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID")}},

		rpcSpec{Path: []string{"factor", "member", "add"}, Short: "给计算任务加因子成员（固定参数用 --param 名=数值）", Service: factorMgr, Method: "AddTaskMember",
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID"), reqFlag("factor-id", "factor_id", "因子 ID"), {Name: "kind", Field: "kind", Kind: rpcString, Usage: "成员类型：fixed（固定参数，默认）或 var（可变参数，参数范围由因子定义给出）", Default: "fixed"},
				{Name: "param", Field: "params", Kind: rpcNumberMap, Usage: "固定参数 名=数值，可重复"}}},
		rpcSpec{Path: []string{"factor", "member", "remove"}, Short: "移除计算任务的因子成员（默认只预览，--yes 才执行）", Service: factorMgr, Method: "RemoveTaskMember", Destructive: true,
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID"), reqFlag("member-key", "member_key", "成员键")}},
		rpcSpec{Path: []string{"factor", "member", "enable"}, Short: "启用因子成员", Service: factorMgr, Method: "SetTaskMemberStatus", Fixed: map[string]any{"status": "enabled"},
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID"), reqFlag("member-key", "member_key", "成员键")}},
		rpcSpec{Path: []string{"factor", "member", "disable"}, Destructive: true, Short: "停用因子成员（默认只预览，--yes 才执行）", Service: factorMgr, Method: "SetTaskMemberStatus", Fixed: map[string]any{"status": "disabled"},
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID"), reqFlag("member-key", "member_key", "成员键")}},

		rpcSpec{Path: []string{"factor", "recalc", "submit"}, Short: "提交历史补算（时间左闭右开；不给 --request-id 时自动生成）", Service: factorMgr, Method: "RecalcFactors", AutoIDs: []string{"request_id"},
			Flags: []rpcFlag{reqFlag("task-id", "task_id", "任务 ID"), listFlag("member", "members", "只补这些成员键，可重复"), listFlag("subject", "subjects", "只补这些标的，可重复"),
				timeFlag("from", "start_time", "起点（RFC3339，含）", true), timeFlag("to", "end_time", "终点（RFC3339，不含）", true), strFlag("request-id", "request_id", "幂等请求 ID")}},
		rpcSpec{Path: []string{"factor", "recalc", "list"}, Short: "列出补算作业", Service: factorMgr, Method: "ListRecalcJobs", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("task-id", "task_id", "任务 ID"), listFlag("status", "statuses", "按状态过滤，可重复")}},
		rpcSpec{Path: []string{"factor", "recalc", "show"}, Short: "查看一个补算作业", Service: factorMgr, Method: "GetRecalcJob",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "作业 ID")}},
		rpcSpec{Path: []string{"factor", "recalc", "cancel"}, Short: "取消补算作业", Service: factorMgr, Method: "CancelRecalcJob",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "作业 ID")}},
	)
}
