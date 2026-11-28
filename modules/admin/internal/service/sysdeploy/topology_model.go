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
