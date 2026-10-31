package command

// 云函数（CloudFunctionMgr）：云账户、函数实例与作业、SCF 导入、代码包。
// 云账户的新建与修改会带密钥，不做命令（请用户在管理台“设置 › 密钥”与云账户页填写）；代码包上传用 collector function upload。
// 函数实例的创建、部署、删除、改运行配置要持有采集发布租约，见 cloud_function.go。

const cloudFunctionMgr = "trpc.moox.cloudfunction.CloudFunctionMgr"

// 云函数服务按请求头里的空间 ID 取上下文（函数实例、代码包与作业都按空间管理），所有命令都带 --space-id（默认 crypto）。
func init() {
	rpcGroupShort["cloud"] = "云函数：云账户、函数实例、SCF 导入与代码包（调用 CloudFunctionMgr）"
	rpcGroupShort["cloud account"] = "云账户（新建与修改带密钥，在管理台操作）"
	rpcGroupShort["cloud function"] = "函数实例与作业"
	rpcGroupShort["cloud scf"] = "从云上导入已有的 SCF 函数"
	rpcGroupShort["cloud package"] = "函数代码包"

	registerRPCSpecs(
		rpcSpec{Path: []string{"cloud", "account", "list"}, Short: "列出云账户（不含密钥明文）", Service: cloudFunctionMgr, Method: "ListCloudAccounts",
			Flags: []rpcFlag{strFlag("provider", "provider", "按云厂商过滤，如 tencent")}},
		rpcSpec{Path: []string{"cloud", "account", "regions"}, Short: "列出云厂商可用的地域", Service: cloudFunctionMgr, Method: "ListCloudRegions",
			Flags: []rpcFlag{strFlag("provider", "provider", "云厂商，如 tencent")}},
		rpcSpec{Path: []string{"cloud", "account", "delete"}, Short: "删除云账户登记（默认只预览，--yes 才执行）", Service: cloudFunctionMgr, Method: "DeleteCloudAccount", Destructive: true,
			Flags: []rpcFlag{reqFlag("account-id", "account_id", "云账户 ID")}},

		rpcSpec{Path: []string{"cloud", "function", "list"}, Short: "列出函数实例", Service: cloudFunctionMgr, Method: "ListFunctionInstances", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("instance-id", "instance_id", "实例 ID"), strFlag("account-id", "cloud_account_id", "云账户 ID"), strFlag("namespace", "namespace", "命名空间"), strFlag("region", "region", "地域"),
				strFlag("function-type", "function_type", "函数类型，如 scf-event"), strFlag("workload-type", "workload_type", "负载类型，如 market_fetcher"), strFlag("trigger-type", "trigger_type", "触发类型"),
				strFlag("keyword", "keyword", "关键字"), boolFlag("refresh-readback", "refresh_readback", "回读云侧状态")}},
		rpcSpec{Path: []string{"cloud", "function", "job"}, Short: "查看函数作业进度", Service: cloudFunctionMgr, Method: "GetFunctionJob",
			Flags: []rpcFlag{reqFlag("job-id", "job_id", "作业 ID")}},
		rpcSpec{Path: []string{"cloud", "function", "invoke"}, Destructive: true, Short: "调用一个函数实例（会真实执行函数；默认只预览事件内容，--yes 才执行）", Service: cloudFunctionMgr, Method: "InvokeFunction",
			Flags: []rpcFlag{reqFlag("instance-id", "instance_id", "实例 ID"), jsonFlag("event", "event_data", "事件 JSON；@文件 读取"), strFlag("invoke-type", "scf_invoke_type", "SCF_INVOKE_TYPE_REQUEST_RESPONSE 或 SCF_INVOKE_TYPE_EVENT"), strFlag("qualifier", "qualifier", "版本或别名")}},

		rpcSpec{Path: []string{"cloud", "scf", "preview"}, Short: "预览云上有哪些 SCF 函数可以导入", Service: cloudFunctionMgr, Method: "PreviewSCFFunctions",
			Flags: []rpcFlag{reqFlag("account-id", "account_id", "云账户 ID")}},
		rpcSpec{Path: []string{"cloud", "scf", "import"}, Short: "把云上已有的 SCF 函数导入登记（函数列表写在 --functions 里；默认只预览，--yes 才执行）", Service: cloudFunctionMgr, Method: "ImportSCFFunctions", Destructive: true,
			Flags: []rpcFlag{reqFlag("account-id", "account_id", "云账户 ID"), {Name: "functions", Field: "functions", Kind: rpcJSON, Usage: `函数引用 JSON，如 '[{"region":"ap-guangzhou","namespace":"default","function_name":"f"}]'；@文件 读取`, Required: true}}},

		rpcSpec{Path: []string{"cloud", "package", "list"}, Short: "列出函数代码包", Service: cloudFunctionMgr, Method: "GetPackageList", Page: rpcPageSize,
			Flags: []rpcFlag{strFlag("name", "package_name", "包名"), strFlag("runtime", "runtime", "运行时"), strFlag("package-type", "package_type", "PACKAGE_TYPE_COLLECTOR 等"), strFlag("workload-type", "workload_type", "负载类型"), strFlag("status", "status", "PACKAGE_STATUS_AVAILABLE 等")}},
		rpcSpec{Path: []string{"cloud", "package", "show"}, Short: "查看代码包", Service: cloudFunctionMgr, Method: "GetPackageDetail",
			Flags: []rpcFlag{reqFlag("package-id", "package_id", "代码包 ID")}},
		rpcSpec{Path: []string{"cloud", "package", "url"}, Short: "取代码包的下载地址", Service: cloudFunctionMgr, Method: "GetPackageDownloadURL",
			Flags: []rpcFlag{reqFlag("package-id", "package_id", "代码包 ID")}},
		rpcSpec{Path: []string{"cloud", "package", "delete"}, Short: "删除代码包（默认只预览，--yes 才执行）", Service: cloudFunctionMgr, Method: "DeletePackage", Destructive: true,
			Flags: []rpcFlag{reqFlag("package-id", "package_id", "代码包 ID")}},
	)
}
