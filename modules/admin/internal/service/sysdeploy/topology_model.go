package sysdeploy

import "time"

const GatewayConflictWindow = 5 * time.Minute

type HostRecord struct {
	HostID         string `gorm:"column:c_host_id;primaryKey"`
	Label          string `gorm:"column:c_label"`
	Address        string `gorm:"column:c_address"`
	PrivateAddress string `gorm:"column:c_private_address"`
	Region         string `gorm:"column:c_region"`
	Status         string `gorm:"column:c_status"`
	Reachability   string `gorm:"column:c_reachability"`
	Description    string `gorm:"column:c_description"`
	// SSHPort 为 0 表示控制台不能 SSH；指纹由 SSH 服务首次连接时写回，同步不会改它。
	SSHPort        int       `gorm:"column:c_ssh_port"`
	SSHUser        string    `gorm:"column:c_ssh_user"`
	SSHFingerprint string    `gorm:"column:c_ssh_fingerprint"`
	CreatedAt      time.Time `gorm:"column:c_ctime;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:c_mtime;autoUpdateTime"`
}

func (HostRecord) TableName() string { return "t_hosts" }

type DeploymentRecord struct {
	HostID      string    `gorm:"column:c_host_id;primaryKey"`
	ComponentID string    `gorm:"column:c_component_id;primaryKey"`
	Status      string    `gorm:"column:c_status"`
	CreatedAt   time.Time `gorm:"column:c_ctime;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime;autoUpdateTime"`
}

func (DeploymentRecord) TableName() string { return "t_deployments" }

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
	// RelaySessions 是 control 网关上报的反向连接会话（JSON 数组），其他主机为空。
	RelaySessions string `gorm:"column:c_relay_sessions"`
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
// the existing host and deployment enablement values.
type HostSpec struct {
	HostID         string
	Label          string
	Address        string
	PrivateAddress string
	// Reachability 为空或 direct 表示可直达，outbound 表示只能出站。
	Reachability string
	Region       string
	Description  string
	Components   []string
	// SSH 为空表示控制台不能 SSH 到这台主机，已存的凭据同时被清除。
	SSH *SSHSpec
}

// SSHSpec 是主机的 SSH 登录信息。凭据明文只在写入事务里使用，加密后存进 t_secrets。
type SSHSpec struct {
	Port       int
	User       string
	Password   string
	PrivateKey string
	Passphrase string
}
