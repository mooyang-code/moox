package model

import (
	"time"
)

// 节点状态常量
const (
	NodeStatusOffline     = 0 // 离线
	NodeStatusOnline      = 1 // 在线
	NodeStatusMaintenance = 2 // 维护中
)

// 节点类型常量
const (
	NodeTypeSCF    = "scf"    // 云函数节点
	NodeTypeServer = "server" // 服务器节点
)

// Invalid常量
const (
	InvalidNo  = 0 // 有效
	InvalidYes = 1 // 无效
)

// CloudNodeTableName 表名常量
const CloudNodeTableName = "t_cloud_nodes"

// CloudNode 云节点信息（包括云函数、服务器等类型）
type CloudNode struct {
	// ID 自增主键
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// NodeID 节点唯一标识
	NodeID string `gorm:"column:c_node_id;uniqueIndex:idx_node_id;size:100;not null;default:''" json:"node_id"`
	// CloudAccountID 云账户ID
	CloudAccountID string `gorm:"column:c_cloud_account_id;size:100;not null;default:''" json:"cloud_account_id"`
	// PackageID 代码包ID，记录该节点当前部署的代码包(11位随机字符串)
	PackageID string `gorm:"column:c_package_id;size:50;default:''" json:"package_id"`
	// PackageVersion 代码包版本信息（用于API返回，不存储到数据库）
	PackageVersion string `gorm:"-" json:"package_version,omitempty"`
	// Namespace 命名空间
	Namespace string `gorm:"column:c_namespace;size:200;not null;default:''" json:"namespace"`
	// NodeType 节点类型（scf=云函数，server=服务器）
	NodeType string `gorm:"column:c_node_type;size:50;not null;default:'scf'" json:"node_type"`
	// Region 部署地区
	Region string `gorm:"column:c_region;size:50;not null;default:''" json:"region"`
	// IPAddress IP地址
	IPAddress string `gorm:"column:c_ip_address;size:50;not null;default:''" json:"ip_address"`
	// SupportedCollectors 支持的采集器类型（JSON数组）
	SupportedCollectors string `gorm:"column:c_supported_collectors;type:text;not null;default:'[]'" json:"supported_collectors"`
	// Metadata 节点额外信息（JSON格式）
	Metadata string `gorm:"column:c_metadata;type:text;not null;default:'{}'" json:"metadata"`
	// Invalid 删除标记
	Invalid int `gorm:"column:c_invalid;not null;default:0" json:"invalid"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (n *CloudNode) TableName() string {
	return "t_cloud_nodes"
}
