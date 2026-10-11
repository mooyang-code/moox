package domain

import (
	"fmt"
	"net/url"
	"time"
)

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
	RawError     string    `gorm:"column:c_raw_error"`
	BodyExcerpt  string    `gorm:"column:c_body_excerpt"`
	CheckedAt    time.Time `gorm:"column:c_checked_at"`
	CreatedAt    time.Time `gorm:"column:c_ctime"`
}

func (CheckResult) TableName() string {
	return "t_monitor_check_results"
}

// ScopedResultID 生成采样结果 ID。c_result_id 全局唯一而检查 ID 不含空间：
// 两个空间的同名检查在同一轮采样里共用同一时刻，必须把空间编码进 ID，否则后插入者会被静默忽略。
func ScopedResultID(spaceID, checkID string, at time.Time) string {
	return fmt.Sprintf("%s/%s-%d", url.PathEscape(spaceID), checkID, at.UnixNano())
}
