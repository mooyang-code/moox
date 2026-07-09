package domain

import "time"

const (
	InstanceStatusUnknown = "unknown"
	InstanceStatusActive  = "active"
	InstanceStatusDown    = "down"
)

type MonitorInstance struct {
	ID         int64      `gorm:"column:c_id;primaryKey"`
	InstanceID string     `gorm:"column:c_instance_id"`
	BaseURL    string     `gorm:"column:c_base_url"`
	Status     string     `gorm:"column:c_status"`
	LastSeenAt *time.Time `gorm:"column:c_last_seen_at"`
	Snapshot   string     `gorm:"column:c_snapshot"`
	IsLocal    bool       `gorm:"column:c_is_local"`
	CreatedAt  time.Time  `gorm:"column:c_ctime"`
	UpdatedAt  time.Time  `gorm:"column:c_mtime"`
}

func (MonitorInstance) TableName() string {
	return "t_monitor_instances"
}

type PeerSnapshot struct {
	ID         int64     `gorm:"column:c_id;primaryKey"`
	InstanceID string    `gorm:"column:c_instance_id"`
	BaseURL    string    `gorm:"column:c_base_url"`
	Status     string    `gorm:"column:c_status"`
	Snapshot   string    `gorm:"column:c_snapshot"`
	CheckedAt  time.Time `gorm:"column:c_checked_at"`
	CreatedAt  time.Time `gorm:"column:c_ctime"`
	UpdatedAt  time.Time `gorm:"column:c_mtime"`
}

func (PeerSnapshot) TableName() string {
	return "t_monitor_peer_snapshots"
}
