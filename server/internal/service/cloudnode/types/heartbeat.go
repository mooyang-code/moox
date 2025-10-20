package types

import "time"

// NodeStatus 节点状态
type NodeStatus int

const (
	NodeStatusOffline  NodeStatus = 0 // 离线
	NodeStatusOnline   NodeStatus = 1 // 在线
	NodeStatusTimeout  NodeStatus = 2 // 超时
	NodeStatusAbnormal NodeStatus = 3 // 异常
)

// NodeType 节点类型
const (
	NodeTypeSCF       = "scf"       // 云函数
	NodeTypeServer    = "server"    // 服务器
	NodeTypeContainer = "container" // 容器
	NodeTypeCustom    = "custom"    // 自定义
)

// HeartbeatNode 心跳记录
type HeartbeatNode struct {
	// 基本信息
	ID            int64      `json:"id" gorm:"column:c_id;primaryKey"`              // 记录ID
	NodeID        string     `json:"node_id" gorm:"column:c_node_id"`               // 节点ID（如云函数名称）
	NodeType      string     `json:"node_type" gorm:"column:c_node_type"`           // 节点类型（scf/server/container）
	SourceService string     `json:"source_service" gorm:"column:c_source_service"` // 来源服务
	Status        NodeStatus `json:"status" gorm:"column:c_status"`                 // 节点状态（0离线/1在线/2超时/3异常）

	// 时间信息
	LastHeartbeat  *time.Time `json:"last_heartbeat" gorm:"column:c_last_heartbeat"`   // 最后心跳时间
	FirstHeartbeat *time.Time `json:"first_heartbeat" gorm:"column:c_first_heartbeat"` // 首次心跳时间

	// 心跳配置
	HeartbeatInterval int `json:"heartbeat_interval" gorm:"column:c_heartbeat_interval"` // 心跳间隔（秒）
	TimeoutThreshold  int `json:"timeout_threshold" gorm:"column:c_timeout_threshold"`   // 超时阈值（秒）

	// 统计数据
	ConsecutiveTimeouts int `json:"consecutive_timeouts" gorm:"column:c_consecutive_timeouts"` // 连续超时次数
	TotalTimeouts       int `json:"total_timeouts" gorm:"column:c_total_timeouts"`             // 累计超时次数
	TotalHeartbeats     int `json:"total_heartbeats" gorm:"column:c_total_heartbeats"`         // 累计心跳次数

	// 扩展数据
	Metadata map[string]interface{} `json:"metadata" gorm:"column:c_metadata;type:text"` // 元数据（JSON格式，存储扩展信息）

	// 探测配置
	ProbeEnabled    bool       `json:"probe_enabled" gorm:"column:c_probe_enabled"`         // 是否启用探测
	ProbeURL        string     `json:"probe_url" gorm:"column:c_probe_url"`                 // 探测URL
	LastProbeTime   *time.Time `json:"last_probe_time" gorm:"column:c_last_probe_time"`     // 最后探测时间
	LastProbeResult bool       `json:"last_probe_result" gorm:"column:c_last_probe_result"` // 最后探测结果

	// 审计字段
	Invalid   int       `json:"-" gorm:"column:c_invalid"`        // 删除标记（0有效/1已删除）
	CreatedAt time.Time `json:"created_at" gorm:"column:c_ctime"` // 创建时间
	UpdatedAt time.Time `json:"updated_at" gorm:"column:c_mtime"` // 更新时间
}

func (HeartbeatNode) TableName() string {
	return "t_heartbeat_nodes"
}

// ReportHeartbeatRequest 上报心跳请求
type ReportHeartbeatRequest struct {
	NodeID        string                 `json:"node_id" binding:"required"`
	NodeType      string                 `json:"node_type" binding:"required"`
	SourceService string                 `json:"source_service"`
	Timestamp     *time.Time             `json:"timestamp"`
	Metrics       map[string]interface{} `json:"metrics"`
	Metadata      map[string]interface{} `json:"metadata"`
}

// BatchReportHeartbeatRequest 批量上报心跳请求
type BatchReportHeartbeatRequest struct {
	Heartbeats []ReportHeartbeatRequest `json:"heartbeats" binding:"required"`
}

// RegisterNodeRequest 注册节点请求
type RegisterNodeRequest struct {
	NodeID            string                 `json:"node_id" binding:"required"`
	NodeType          string                 `json:"node_type" binding:"required"`
	SourceService     string                 `json:"source_service"`
	HeartbeatInterval int                    `json:"heartbeat_interval"`
	TimeoutThreshold  int                    `json:"timeout_threshold"`
	ProbeEnabled      bool                   `json:"probe_enabled"`
	ProbeURL          string                 `json:"probe_url"`
	Metadata          map[string]interface{} `json:"metadata"`
}

// UpdateNodeConfigRequest 更新节点配置请求
type UpdateNodeConfigRequest struct {
	NodeID            string  `json:"node_id" binding:"required"`
	NodeType          string  `json:"node_type" binding:"required"`
	HeartbeatInterval *int    `json:"heartbeat_interval"`
	TimeoutThreshold  *int    `json:"timeout_threshold"`
	ProbeEnabled      *bool   `json:"probe_enabled"`
	ProbeURL          *string `json:"probe_url"`
}

// NodeFilter 节点过滤器
type NodeFilter struct {
	NodeIDs       []string    `json:"node_ids"`
	NodeTypes     []string    `json:"node_types"`
	SourceService *string     `json:"source_service"`
	Status        *NodeStatus `json:"status"`
	ProbeEnabled  *bool       `json:"probe_enabled"`
	Keyword       string      `json:"keyword"`
	Page          int         `json:"page"`
	PageSize      int         `json:"page_size"`
	SortBy        string      `json:"sort_by"`
	SortOrder     string      `json:"sort_order"`
}

// GetPage 获取页码
func (f *NodeFilter) GetPage() int {
	if f.Page <= 0 {
		return 1
	}
	return f.Page
}

// GetPageSize 获取页大小
func (f *NodeFilter) GetPageSize() int {
	if f.PageSize <= 0 {
		return 20
	}
	if f.PageSize > 100 {
		return 100
	}
	return f.PageSize
}
