// Package placement 管理部署主机、部署和主机网关状态（t_hosts、t_placements、t_host_gateway_status）。
//
// 组件定义来自代码中的组件目录（packages/servicecatalog）；这里只记录哪个组件部署在哪台主机、是否启用。
// 每次修改都在一个事务里载入完整状态、应用修改、按组件目录整体校验，任何一项不通过就整体拒绝。
package placement

import "time"

// 主机与部署的状态。
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
)

// Host 对应 t_hosts。
type Host struct {
	HostID         string    `gorm:"column:c_host_id;primaryKey"`
	Address        string    `gorm:"column:c_address;not null"`
	PrivateAddress string    `gorm:"column:c_private_address;not null;default:''"`
	Region         string    `gorm:"column:c_region;not null;default:''"`
	Status         string    `gorm:"column:c_status;not null;default:'enabled'"`
	Description    string    `gorm:"column:c_description;not null;default:''"`
	CreatedAt      time.Time `gorm:"column:c_ctime;autoCreateTime:false"`
	UpdatedAt      time.Time `gorm:"column:c_mtime;autoUpdateTime:false"`
}

// TableName 返回表名。
func (Host) TableName() string { return "t_hosts" }

// Placement 对应 t_placements。
type Placement struct {
	HostID      string    `gorm:"column:c_host_id;primaryKey"`
	ComponentID string    `gorm:"column:c_component_id;primaryKey"`
	Status      string    `gorm:"column:c_status;not null;default:'enabled'"`
	CreatedAt   time.Time `gorm:"column:c_ctime;autoCreateTime:false"`
	UpdatedAt   time.Time `gorm:"column:c_mtime;autoUpdateTime:false"`
}

// TableName 返回表名。
func (Placement) TableName() string { return "t_placements" }

// GatewayStatus 对应 t_host_gateway_status，由主机网关的心跳写入。
type GatewayStatus struct {
	HostID             string     `gorm:"column:c_host_id;primaryKey"`
	InstanceID         string     `gorm:"column:c_instance_id;not null;default:''"`
	Version            string     `gorm:"column:c_version;not null;default:''"`
	ExpectedHash       string     `gorm:"column:c_expected_hash;not null;default:''"`
	AppliedHash        string     `gorm:"column:c_applied_hash;not null;default:''"`
	RouteCount         int32      `gorm:"column:c_route_count;not null;default:0"`
	LastSeenAt         *time.Time `gorm:"column:c_last_seen_at"`
	LastError          string     `gorm:"column:c_last_error;not null;default:''"`
	PreviousInstanceID string     `gorm:"column:c_previous_instance_id;not null;default:''"`
	ReplacedAt         *time.Time `gorm:"column:c_replaced_at"`
	ConflictInstanceID string     `gorm:"column:c_conflict_instance_id;not null;default:''"`
	ConflictSeenAt     *time.Time `gorm:"column:c_conflict_seen_at"`
	MismatchSince      *time.Time `gorm:"column:c_mismatch_since"`
	// CertificateNotAfter 是主机网关在心跳中上报的服务端证书到期时间。
	CertificateNotAfter *time.Time `gorm:"column:c_certificate_not_after"`
}

// TableName 返回表名。
func (GatewayStatus) TableName() string { return "t_host_gateway_status" }

// HostSpec 是 CLI 按 moox.toml 登记的一台主机。
type HostSpec struct {
	HostID         string
	Address        string
	PrivateAddress string
	Region         string
	Description    string
}

// SyncResult 是一次 SyncHostPlacements 的结果。
type SyncResult struct {
	HostCreated bool
	Added       []string
	Removed     []string
	Kept        []string
}

// GatewayReport 是主机网关的一次心跳。
type GatewayReport struct {
	HostID      string
	InstanceID  string
	Version     string
	AppliedHash string
	RouteCount  int32
	LastError   string
	// CertificateNotAfter 为空表示网关没有上报证书（例如测试环境）。
	CertificateNotAfter *time.Time
}

// 网关状态的判定窗口（设计文档 3.8、3.10）。
const (
	// AlternationWindow 内被替换掉的旧实例又上报，记为冲突；冲突在这段时间内不再交替就清除。
	AlternationWindow = 5 * time.Minute
	// OfflineAfter 内没有心跳视为失联。
	OfflineAfter = 2 * time.Minute
)

// 主机网关的展示状态。
const (
	GatewayOnline        = "online"
	GatewayOffline       = "offline"
	GatewayNeverReported = "never_reported"
	GatewayConflict      = "conflict"
)

// State 计算主机网关的展示状态。
func (s GatewayStatus) State(now time.Time) string {
	switch {
	case s.LastSeenAt == nil:
		return GatewayNeverReported
	case s.ConflictSeenAt != nil && now.Sub(*s.ConflictSeenAt) < AlternationWindow:
		return GatewayConflict
	case now.Sub(*s.LastSeenAt) > OfflineAfter:
		return GatewayOffline
	default:
		return GatewayOnline
	}
}

// Synced 判断已应用的快照是否就是期望的快照。
func (s GatewayStatus) Synced() bool {
	return s.ExpectedHash != "" && s.AppliedHash == s.ExpectedHash
}
