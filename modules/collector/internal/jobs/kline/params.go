// Package kline contains K-line collector JobItem planning and execution types.
package kline

// Params is the JobItem params shape for K-line collection.
type Params struct {
	SpaceID          string `json:"space_id"`
	TaskID           string `json:"task_id"`
	Exchange         string `json:"exchange"`
	Market           string `json:"market"`
	DataType         string `json:"data_type"`
	DatasetID        string `json:"dataset_id"`
	SubjectID        string `json:"subject_id"`
	Symbol           string `json:"symbol"`
	Interval         string `json:"interval"`
	ScheduleInterval string `json:"schedule_interval"`
	ScheduleWindow   string `json:"schedule_window"`
}
