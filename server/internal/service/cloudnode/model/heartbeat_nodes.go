package model

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
)

// HeartbeatNode 心跳记录数据模型
type HeartbeatNode struct {
	// 基本信息
	ID            int64            `gorm:"column:c_id;primaryKey;autoIncrement"` // 记录ID
	NodeID        string           `gorm:"column:c_node_id;not null"`            // 节点ID
	NodeType      string           `gorm:"column:c_node_type;not null"`          // 节点类型
	SourceService string           `gorm:"column:c_source_service;default:''"`   // 来源服务
	Status        types.NodeStatus `gorm:"column:c_status;default:0"`            // 节点状态

	// 时间信息
	LastHeartbeat  *time.Time `gorm:"column:c_last_heartbeat"`  // 最后心跳时间
	FirstHeartbeat *time.Time `gorm:"column:c_first_heartbeat"` // 首次心跳时间

	// 心跳配置
	HeartbeatInterval int `gorm:"column:c_heartbeat_interval;default:10"` // 心跳间隔（秒）
	TimeoutThreshold  int `gorm:"column:c_timeout_threshold;default:30"`  // 超时阈值（秒）

	// 统计数据
	ConsecutiveTimeouts int `gorm:"column:c_consecutive_timeouts;default:0"` // 连续超时次数
	TotalTimeouts       int `gorm:"column:c_total_timeouts;default:0"`       // 累计超时次数
	TotalHeartbeats     int `gorm:"column:c_total_heartbeats;default:0"`     // 累计心跳次数

	// 扩展数据
	Metadata JSONMap `gorm:"column:c_metadata;type:text;default:'{}'"` // 元数据

	// 探测配置
	ProbeEnabled    bool       `gorm:"column:c_probe_enabled;default:true"`      // 是否启用探测
	ProbeURL        string     `gorm:"column:c_probe_url;default:''"`            // 探测URL
	LastProbeTime   *time.Time `gorm:"column:c_last_probe_time"`                 // 最后探测时间
	LastProbeResult bool       `gorm:"column:c_last_probe_result;default:false"` // 最后探测结果

	// 审计字段
	Invalid   int       `gorm:"column:c_invalid;default:0"`    // 删除标记
	CreatedAt time.Time `gorm:"column:c_ctime;autoCreateTime"` // 创建时间
	UpdatedAt time.Time `gorm:"column:c_mtime;autoUpdateTime"` // 更新时间
}

func (HeartbeatNode) TableName() string {
	return "t_heartbeat_nodes"
}

// ToTypesRecord 转换为types包中的记录类型
func (m *HeartbeatNode) ToTypesRecord() *types.HeartbeatNode {
	var metadata map[string]interface{}

	if m.Metadata != nil {
		metadata = map[string]interface{}(m.Metadata)
	}

	return &types.HeartbeatNode{
		ID:                  m.ID,
		NodeID:              m.NodeID,
		NodeType:            m.NodeType,
		SourceService:       m.SourceService,
		Status:              m.Status,
		LastHeartbeat:       m.LastHeartbeat,
		FirstHeartbeat:      m.FirstHeartbeat,
		HeartbeatInterval:   m.HeartbeatInterval,
		TimeoutThreshold:    m.TimeoutThreshold,
		ConsecutiveTimeouts: m.ConsecutiveTimeouts,
		TotalTimeouts:       m.TotalTimeouts,
		TotalHeartbeats:     m.TotalHeartbeats,
		Metadata:            metadata,
		ProbeEnabled:        m.ProbeEnabled,
		ProbeURL:            m.ProbeURL,
		LastProbeTime:       m.LastProbeTime,
		LastProbeResult:     m.LastProbeResult,
		CreatedAt:           m.CreatedAt,
		UpdatedAt:           m.UpdatedAt,
	}
}

// FromTypesRecord 从types包中的记录类型转换
func (m *HeartbeatNode) FromTypesRecord(record *types.HeartbeatNode) {
	m.ID = record.ID
	m.NodeID = record.NodeID
	m.NodeType = record.NodeType
	m.SourceService = record.SourceService
	m.Status = record.Status
	m.LastHeartbeat = record.LastHeartbeat
	m.FirstHeartbeat = record.FirstHeartbeat
	m.HeartbeatInterval = record.HeartbeatInterval
	m.TimeoutThreshold = record.TimeoutThreshold
	m.ConsecutiveTimeouts = record.ConsecutiveTimeouts
	m.TotalTimeouts = record.TotalTimeouts
	m.TotalHeartbeats = record.TotalHeartbeats

	if record.Metadata != nil {
		m.Metadata = JSONMap(record.Metadata)
	}

	m.ProbeEnabled = record.ProbeEnabled
	m.ProbeURL = record.ProbeURL
	m.LastProbeTime = record.LastProbeTime
	m.LastProbeResult = record.LastProbeResult
	m.CreatedAt = record.CreatedAt
	m.UpdatedAt = record.UpdatedAt
}

// JSONMap 自定义JSON映射类型
type JSONMap map[string]interface{}

// Value 实现driver.Valuer接口
func (j JSONMap) Value() (driver.Value, error) {
	if j == nil {
		return "{}", nil
	}
	return json.Marshal(j)
}

// Scan 实现sql.Scanner接口
func (j *JSONMap) Scan(value interface{}) error {
	if value == nil {
		*j = make(map[string]interface{})
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		*j = make(map[string]interface{})
		return nil
	}

	return json.Unmarshal(bytes, j)
}
