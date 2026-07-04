// Package symbol contains symbol collector JobItem planning and execution types.
package symbol

// Params is the JobItem params shape for symbol collection.
type Params struct {
	SpaceID          string `json:"space_id"`
	TaskID           string `json:"task_id"`
	Exchange         string `json:"exchange"`
	Market           string `json:"market"`
	DataType         string `json:"data_type"`
	DatasetID        string `json:"dataset_id"`
	ScheduleInterval string `json:"schedule_interval"`
	ScheduleTimezone string `json:"schedule_timezone"`
	ScheduleWindow   string `json:"schedule_window"`
}
