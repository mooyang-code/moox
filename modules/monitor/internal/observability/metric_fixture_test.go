package observability

import "gorm.io/gorm/clause"

// metricLatestUpsert 让测试分两次写同一条序列（先目录、再最新值）时，第二次只补上最新值的各列。
var metricLatestUpsert = clause.OnConflict{
	Columns:   []clause.Column{{Name: "c_series_id"}},
	DoUpdates: clause.AssignmentColumns([]string{"c_value", "c_observed_at", "c_interval_seconds", "c_message_id", "c_producer_node_id", "c_producer_version"}),
}
