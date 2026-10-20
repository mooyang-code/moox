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
	// 失败的 HTTP 尝试：批次里的次数与分布、偏慢单项的逐次记录。
	{Key: "attempt_trace"}, {Key: "standby_ms", Numeric: true}, {Key: "standby_fired", Numeric: true}, {Key: "standby_won", Numeric: true},
	// 函数进程与触发：定时事件迟到、错峰等待、入口到开始取数、进程存活时间、第几次调用、CPU 数、取数期间的最大调度迟滞与协程数。
	{Key: "timer_late_ms", Numeric: true}, {Key: "stagger_ms", Numeric: true}, {Key: "entry_to_start_ms", Numeric: true},
	{Key: "proc_age_ms", Numeric: true}, {Key: "invocation_seq", Numeric: true}, {Key: "num_cpu", Numeric: true},
	{Key: "sched_lag_max_ms", Numeric: true}, {Key: "goroutines_max", Numeric: true},
	{Key: "fail_total", Numeric: true}, {Key: "fail_timeout", Numeric: true}, {Key: "fail_reset", Numeric: true},
	{Key: "fail_tls", Numeric: true}, {Key: "fail_dial", Numeric: true}, {Key: "fail_status", Numeric: true}, {Key: "fail_other", Numeric: true},
	{Key: "fail_in_connect_phase", Numeric: true}, {Key: "fail_in_wait_response", Numeric: true}, {Key: "fail_in_io_phase", Numeric: true},
	{Key: "fail_on_reused_conn", Numeric: true}, {Key: "fail_ms_p50", Numeric: true}, {Key: "fail_ms_max", Numeric: true},
}
