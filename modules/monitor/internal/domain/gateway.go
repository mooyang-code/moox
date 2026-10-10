package domain

import "time"

// GatewayObservation caches the non-secret v2 status and the start of an
// unapplied desired snapshot. A Monitor restart must not reset that deadline.
type GatewayObservation struct {
	HostID            string     `gorm:"column:c_host_id;primaryKey"`
	HostEnabledAt     time.Time  `gorm:"column:c_host_enabled_at"`
	FirstObservedAt   time.Time  `gorm:"column:c_first_observed_at"`
	ObservedAt        *time.Time `gorm:"column:c_observed_at"`
	LastAttemptAt     time.Time  `gorm:"column:c_last_attempt_at"`
	StatusJSON        string     `gorm:"column:c_status_json"`
	ExpectedHash      string     `gorm:"column:c_expected_hash"`
	AppliedHash       string     `gorm:"column:c_applied_hash"`
	HashMismatchSince *time.Time `gorm:"column:c_hash_mismatch_since"`
	ReadError         string     `gorm:"column:c_read_error"`
}

func (GatewayObservation) TableName() string { return "t_monitor_gateway_observations" }
