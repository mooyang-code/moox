package observability

// businessCheckIdentity 给 Monitor 替生产者判断的业务检查补上检查 ID 与名称（关注项自带，不经这里）。
// 这些检查会逐项改成生产者上报的关注项，改完一项删一项。
func businessCheckIdentity(business BusinessStatus) (checkID, name string) {
	switch business.Kind {
	case "market_fetch":
		if business.Module == "collector_stream_missing" {
			return "market_fetch:" + business.Module, "行情推送连续缺失"
		}
		return "market_fetch:" + business.Module, "行情采集 " + business.Module + " 协调"
	case "factor_params":
		return "factor_params:" + business.Module, "策略参数投递"
	case "data_delivery":
		return "data_delivery:" + business.Module, "数据投递队列"
	case "view_file":
		return "view_file:" + business.Module, "View 索引文件大小"
	case "metrics_ingest":
		return "metrics_ingest:" + business.Module, "指标序列数量保护"
	case "worker_presence":
		return "worker_presence:" + business.Module, WorkerPresenceName(business.Module)
	case "archive_object_store":
		return "archive_object_store:" + business.Module, ArchiveObjectStoreCheckName
	case "archive_task":
		return "archive_task:" + business.Module, ArchiveTaskCheckName(business.Module)
	case "archive_coverage":
		return "archive_coverage:" + business.Module, ArchiveCoverageCheckName
	}
	return "balance:" + business.Module, "账户余额同步 " + business.Module
}
