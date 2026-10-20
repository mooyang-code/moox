package tencent

// MarketFetchLogIndexFields 是 SCF 采集函数上报 CLS 的日志字段（失败项明细 market_fetch_item_failure、批次摘要
// market_fetch_batch、偏慢或重试过的单项 market_fetch_item_slow 与写入 Storage 的耗时 market_fetch_commit），需要建立键值索引才能按字段检索与 SQL 统计。字段由 collector 的 cls_report.go 写入，
// collector 测试保证两边一致；增删字段时两处一起改。
var MarketFetchLogIndexFields = []CLSIndexField{
	{Key: "log_type"}, {Key: "space_id"}, {Key: "function"}, {Key: "batch_kind"}, {Key: "request_id"},
	{Key: "dataset_id"}, {Key: "frequency"}, {Key: "subject_id"}, {Key: "symbol"}, {Key: "provider"},
	{Key: "outcome"}, {Key: "error_type"}, {Key: "error"}, {Key: "window_start"}, {Key: "window_end"},
	{Key: "attempts", Numeric: true}, {Key: "fetch_ms", Numeric: true}, {Key: "planned", Numeric: true},
	{Key: "failed", Numeric: true}, {Key: "window_left_ms", Numeric: true}, {Key: "used_weight", Numeric: true},
	// 取数阶段耗时（批次摘要与偏慢单项）：since_close_* 是相对收盘的毫秒偏移，其余是各阶段耗时。
	{Key: "endpoint"}, {Key: "remote_addr"},
	{Key: "since_close_start_ms", Numeric: true}, {Key: "since_close_fetch_end_ms", Numeric: true}, {Key: "since_close_done_ms", Numeric: true},
	{Key: "item_p50_ms", Numeric: true}, {Key: "item_p90_ms", Numeric: true}, {Key: "item_max_ms", Numeric: true},
	{Key: "queue_ms", Numeric: true}, {Key: "queue_max_ms", Numeric: true},
	{Key: "dns_ms", Numeric: true}, {Key: "dns_max_ms", Numeric: true},
	{Key: "connect_ms", Numeric: true}, {Key: "connect_p90_ms", Numeric: true}, {Key: "connect_max_ms", Numeric: true},
	{Key: "tls_ms", Numeric: true}, {Key: "tls_p90_ms", Numeric: true}, {Key: "tls_max_ms", Numeric: true},
	{Key: "ttfb_ms", Numeric: true}, {Key: "ttfb_p50_ms", Numeric: true}, {Key: "ttfb_p90_ms", Numeric: true}, {Key: "ttfb_max_ms", Numeric: true},
	{Key: "attempts_total", Numeric: true}, {Key: "retried", Numeric: true}, {Key: "http_requests", Numeric: true},
	{Key: "conn_new", Numeric: true}, {Key: "conn_reused", Numeric: true}, {Key: "http_status", Numeric: true},
	{Key: "rows", Numeric: true}, {Key: "datasets", Numeric: true}, {Key: "commit_ms", Numeric: true},
}
