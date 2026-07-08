package domain

import "time"

const (
	CheckStatusOK       = "ok"
	CheckStatusDegraded = "degraded"
	CheckStatusDown     = "down"
)

type CheckResult struct {
	ID           int64     `gorm:"column:c_id;primaryKey"`
	ResultID     string    `gorm:"column:c_result_id"`
	SpaceID      string    `gorm:"column:c_space_id"`
	CheckID      string    `gorm:"column:c_check_id"`
	InstanceID   string    `gorm:"column:c_instance_id"`
	Success      bool      `gorm:"column:c_success"`
	Status       string    `gorm:"column:c_status"`
	HTTPStatus   int       `gorm:"column:c_http_status"`
	Connected    bool      `gorm:"column:c_connected"`
	LatencyMS    int64     `gorm:"column:c_latency_ms"`
	ErrorMessage string    `gorm:"column:c_error_message"`
	BodyExcerpt  string    `gorm:"column:c_body_excerpt"`
	CheckedAt    time.Time `gorm:"column:c_checked_at"`
	CreatedAt    time.Time `gorm:"column:c_ctime"`
}

func (CheckResult) TableName() string {
	return "t_monitor_check_results"
}
