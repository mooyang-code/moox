package symbol

// Result is the execution summary for a symbol JobItem.
type Result struct {
	RowsWritten int    `json:"rows_written"`
	Market      string `json:"market"`
}
