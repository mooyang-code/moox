package tencent

// MarketFetchLogIndexFields 是 SCF 采集函数上报 CLS 的日志字段（失败项明细 market_fetch_item_failure 与批次摘要
// market_fetch_batch），需要建立键值索引才能按字段检索与 SQL 统计。字段由 collector 的 cls_report.go 写入，
// collector 测试保证两边一致；增删字段时两处一起改。
var MarketFetchLogIndexFields = []CLSIndexField{
	{Key: "log_type"}, {Key: "space_id"}, {Key: "function"}, {Key: "batch_kind"}, {Key: "request_id"},
	{Key: "dataset_id"}, {Key: "frequency"}, {Key: "subject_id"}, {Key: "symbol"}, {Key: "provider"},
	{Key: "outcome"}, {Key: "error_type"}, {Key: "error"}, {Key: "window_start"}, {Key: "window_end"},
	{Key: "attempts", Numeric: true}, {Key: "fetch_ms", Numeric: true}, {Key: "planned", Numeric: true},
	{Key: "failed", Numeric: true}, {Key: "window_left_ms", Numeric: true},
}
