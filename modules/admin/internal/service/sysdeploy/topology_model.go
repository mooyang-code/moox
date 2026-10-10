package sysdeploy

import "time"

const GatewayConflictWindow = 5 * time.Minute

type HostRecord struct {
	HostID         string    `gorm:"column:c_host_id;primaryKey"`
	Address        string    `gorm:"column:c_address"`
	PrivateAddress string    `gorm:"column:c_private_address"`
	Region         string    `gorm:"column:c_region"`
	Status         string    `gorm:"column:c_status"`
	Description    string    `gorm:"column:c_description"`
	CreatedAt      time.Time `gorm:"column:c_ctime;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:c_mtime;autoUpdateTime"`
}

func (HostRecord) TableName() string { return "t_hosts" }

type PlacementRecord struct {
	HostID      string    `gorm:"column:c_host_id;primaryKey"`
	ComponentID string    `gorm:"column:c_component_id;primaryKey"`
	Status      string    `gorm:"column:c_status"`
	CreatedAt   time.Time `gorm:"column:c_ctime;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime;autoUpdateTime"`
}

func (PlacementRecord) TableName() string { return "t_placements" }

type HostGatewayStatus struct {
	HostID             string     `gorm:"column:c_host_id;primaryKey"`
	InstanceID         string     `gorm:"column:c_instance_id"`
	Version            string     `gorm:"column:c_version"`
	ExpectedHash       string     `gorm:"column:c_expected_hash"`
	AppliedHash        string     `gorm:"column:c_applied_hash"`
	RouteCount         int64      `gorm:"column:c_route_count"`
	LastSeenAt         *time.Time `gorm:"column:c_last_seen_at"`
	LastError          string     `gorm:"column:c_last_error"`
	PreviousInstanceID string     `gorm:"column:c_previous_instance_id"`
	ReplacedAt         *time.Time `gorm:"column:c_replaced_at"`
	ConflictInstanceID string     `gorm:"column:c_conflict_instance_id"`
	ConflictSeenAt     *time.Time `gorm:"column:c_conflict_seen_at"`
}

func (HostGatewayStatus) TableName() string { return "t_host_gateway_status" }

// ClearExpiredConflict gives readers the same expiry even when an offline
// gateway sends no further heartbeat. The next status write persists it.
func (s *HostGatewayStatus) ClearExpiredConflict(now time.Time) {
	if s.ConflictSeenAt != nil && !now.Before(s.ConflictSeenAt.Add(GatewayConflictWindow)) {
		s.ConflictInstanceID, s.ConflictSeenAt = "", nil
	}
}

// HostSpec contains deployment input, not runtime status. Synchronization keeps
// the existing host and placement enablement values.
type HostSpec struct {
	HostID         string
	Address        string
	PrivateAddress string
	Region         string
	Description    string
	Components     []string
}
