package domain

import "time"

const (
	CheckKindHTTP = "http"
	CheckKindTCP  = "tcp"

	CheckSourceManual    = "manual"
	CheckSourceSysDeploy = "sysdeploy"
)

type Check struct {
	ID              int64      `gorm:"column:c_id;primaryKey"`
	SpaceID         string     `gorm:"column:c_space_id"`
	CheckID         string     `gorm:"column:c_check_id"`
	Name            string     `gorm:"column:c_name"`
	GroupName       string     `gorm:"column:c_group_name"`
	Kind            string     `gorm:"column:c_kind"`
	URL             string     `gorm:"column:c_url"`
	Method          string     `gorm:"column:c_method"`
	Headers         string     `gorm:"column:c_headers"`
	Body            string     `gorm:"column:c_body"`
	TCPHost         string     `gorm:"column:c_tcp_host"`
	TCPPort         int        `gorm:"column:c_tcp_port"`
	IntervalSeconds int        `gorm:"column:c_interval_seconds"`
	TimeoutMS       int        `gorm:"column:c_timeout_ms"`
	ExpectedStatus  string     `gorm:"column:c_expected_status"`
	MaxResponseMS   int        `gorm:"column:c_max_response_ms"`
	BodyContains    string     `gorm:"column:c_body_contains"`
	Enabled         bool       `gorm:"column:c_enabled"`
	Source          string     `gorm:"column:c_source"`
	Labels          string     `gorm:"column:c_labels"`
	Description     string     `gorm:"column:c_description"`
	LastCheckedAt   *time.Time `gorm:"column:c_last_checked_at"`
	NextCheckAt     *time.Time `gorm:"column:c_next_check_at"`
	IsDeleted       bool       `gorm:"column:c_is_deleted"`
	CreatedAt       time.Time  `gorm:"column:c_ctime"`
	UpdatedAt       time.Time  `gorm:"column:c_mtime"`
}

func (Check) TableName() string {
	return "t_monitor_checks"
}
