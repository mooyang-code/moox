package sysdeploy

import "time"

type HostRecord struct {
	HostID         string `gorm:"column:c_host_id;primaryKey"`
	Label          string `gorm:"column:c_label"`
	Address        string `gorm:"column:c_address"`
	PrivateAddress string `gorm:"column:c_private_address"`
	Region         string `gorm:"column:c_region"`
	Status         string `gorm:"column:c_status"`
	Reachability   string `gorm:"column:c_reachability"`
	ExternalEntry  bool   `gorm:"column:c_external_entry"`
	Description    string `gorm:"column:c_description"`
	// SSHPort 为 0 表示控制台不能 SSH；指纹由 SSH 服务首次连接时写回，同步不会改它。
	SSHPort        int    `gorm:"column:c_ssh_port"`
	SSHUser        string `gorm:"column:c_ssh_user"`
	SSHFingerprint string `gorm:"column:c_ssh_fingerprint"`
	// SignPublicKey 是这台主机的签名公钥（base64）；主机同步不改它，只有签发身份包时写入。
	SignPublicKey string    `gorm:"column:c_sign_public_key"`
	CreatedAt     time.Time `gorm:"column:c_ctime;autoCreateTime"`
	UpdatedAt     time.Time `gorm:"column:c_mtime;autoUpdateTime"`
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

// HostGatewayStatus 是网关最近一次拉取快照时记下的状态：拉取兼作心跳，每次都写。
type HostGatewayStatus struct {
	HostID       string     `gorm:"column:c_host_id;primaryKey"`
	ExpectedHash string     `gorm:"column:c_expected_hash"`
	AppliedHash  string     `gorm:"column:c_applied_hash"`
	LastSeenAt   *time.Time `gorm:"column:c_last_seen_at"`
	// RelaySessions 是 control 网关带来的反向连接会话（JSON 数组），其他主机为空。
	RelaySessions string `gorm:"column:c_relay_sessions"`
}

func (HostGatewayStatus) TableName() string { return "t_host_gateway_status" }

// HostSpec 是 moox.toml 里一台主机的完整声明，含启停状态：部署时整份覆盖 Admin 的主机与部署。
type HostSpec struct {
	HostID string
	// Enabled 为 false 时主机记为停用。
	Enabled        bool
	Label          string
	Address        string
	PrivateAddress string
	// Reachability 为空或 direct 表示可直达，outbound 表示只能出站。
	Reachability string
	// ExternalEntry 表示这台主机的网关开外部入口（11004）。
	ExternalEntry bool
	Region        string
	Description   string
	Components    []string
	// DisabledComponents 是 Components 里记为停用的组件（[deployments_disabled]）。
	DisabledComponents []string
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
