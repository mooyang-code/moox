package domain

import "time"

// ComponentHealthState records when the observed aggregate status changed.
// Probe/report timestamps are independent and must never act as status_since.
type ComponentHealthState struct {
	HostID      string    `gorm:"column:c_host_id;primaryKey"`
	ComponentID string    `gorm:"column:c_component_id;primaryKey"`
	Status      string    `gorm:"column:c_status"`
	SinceAt     time.Time `gorm:"column:c_since_at"`
	ObservedAt  time.Time `gorm:"column:c_observed_at"`
}

func (ComponentHealthState) TableName() string { return "t_monitor_component_health" }

// HealthStatus projects internal probe/reporter states onto the public contract.
func HealthStatus(status string) string {
	switch status {
	case "healthy", "ok":
		return "healthy"
	case "degraded", "stale":
		return "degraded"
	case "down", "firing":
		return "down"
	case "disabled", "unchecked":
		return status
	default:
		return "unknown"
	}
}
