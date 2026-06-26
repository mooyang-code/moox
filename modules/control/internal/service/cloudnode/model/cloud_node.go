package model

import (
	"strings"
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
	NodeTypeSCFEvent = "scf-event" // 云函数节点（事件型）
	NodeTypeSCFWeb   = "scf-web"   // 云函数节点（Web型）
	NodeTypeServer   = "server"    // 服务器节点
)

// SCFFunctionType 根据NodeType返回腾讯云SCF函数类型
// scf-event -> "Event", scf-web -> "HTTP", 其他 -> ""
func SCFFunctionType(nodeType string) string {
	switch nodeType {
	case NodeTypeSCFEvent:
		return "Event"
	case NodeTypeSCFWeb:
		return "HTTP"
	default:
		return ""
	}
}

// BizTypeLabel 根据业务类型返回用于节点ID的标记名
// 将下划线分隔的snake_case转为PascalCase，如 "data_collector" -> "DataCollector"
func BizTypeLabel(bizType string) string {
	if bizType == "" {
		return "DataCollector"
	}
	parts := strings.Split(bizType, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

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
	// SpaceID 空间ID（硬隔离维度）
	SpaceID string `gorm:"column:c_space_id;index:idx_cloud_nodes_space_id;size:100;not null;default:''" json:"space_id"`
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
	// NodeType 节点类型（scf-event=云函数事件型，scf-web=云函数Web型，server=服务器）
	NodeType string `gorm:"column:c_node_type;type:text;not null;default:'scf-event'" json:"node_type"`
	// BizType 业务类型（data_collector=数据采集, factor_calculator=因子计算）
	BizType string `gorm:"column:c_biz_type;size:50;not null;default:'data_collector'" json:"biz_type"`
	// Region 部署地区
	Region string `gorm:"column:c_region;size:50;not null;default:''" json:"region"`
	// Tag 标签（国内/海外）
	Tag string `gorm:"column:c_tag;size:20;not null;default:''" json:"tag"`
	// IPAddress IP地址
	IPAddress string `gorm:"column:c_ip_address;size:50;not null;default:''" json:"ip_address"`
	// SupportedCollectors 支持的采集器类型（JSON数组）
	SupportedCollectors string `gorm:"column:c_supported_collectors;type:text;not null;default:'[]'" json:"supported_collectors"`
	// Metadata 节点额外信息（JSON格式）
	Metadata string `gorm:"column:c_metadata;type:text;not null;default:'{}'" json:"metadata"`
	// TimeoutThreshold 超时阈值（秒），0表示使用全局默认值
	TimeoutThreshold int `gorm:"column:c_timeout_threshold;default:0" json:"timeout_threshold"`
	// HeartbeatInterval 心跳间隔（秒），0表示使用全局默认值
	HeartbeatInterval int `gorm:"column:c_heartbeat_interval;default:10" json:"heartbeat_interval"`
	// ProbeEnabled 是否启用探测
	ProbeEnabled bool `gorm:"column:c_probe_enabled;default:true" json:"probe_enabled"`
	// ProbeURL 探测URL
	ProbeURL string `gorm:"column:c_probe_url;default:''" json:"probe_url"`
	// RunningVersion 当前运行版本（来自心跳上报）
	RunningVersion string `gorm:"column:c_running_version;size:50;not null;default:''" json:"running_version"`
	// Invalid 删除标记
	Invalid int `gorm:"column:c_invalid;not null;default:0" json:"invalid"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
	// LastHeartbeat 最后心跳时间（来自心跳表联表查询）
	LastHeartbeat *time.Time `gorm:"column:c_last_heartbeat;->" json:"last_heartbeat,omitempty"`
}

// TableName 指定表名
func (n *CloudNode) TableName() string {
	return "t_cloud_nodes"
}
