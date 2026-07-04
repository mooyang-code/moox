package kline

// Result is the execution summary for a K-line JobItem.
type Result struct {
	RowsWritten int    `json:"rows_written"`
	Symbol      string `json:"symbol"`
	Interval    string `json:"interval"`
}
