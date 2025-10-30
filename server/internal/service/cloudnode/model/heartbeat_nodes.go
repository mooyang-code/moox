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
	ID            int64  `gorm:"column:c_id;primaryKey;autoIncrement"` // 记录ID
	NodeID        string `gorm:"column:c_node_id;not null"`            // 节点ID
	NodeType      string `gorm:"column:c_node_type;not null"`          // 节点类型
	SourceService string `gorm:"column:c_source_service;default:''"`   // 来源服务

	// 时间信息
	LastHeartbeat  *time.Time `gorm:"column:c_last_heartbeat"`  // 最后心跳时间
	FirstHeartbeat *time.Time `gorm:"column:c_first_heartbeat"` // 首次心跳时间

	// 统计数据
	ConsecutiveTimeouts int `gorm:"column:c_consecutive_timeouts;default:0"` // 连续超时次数
	TotalTimeouts       int `gorm:"column:c_total_timeouts;default:0"`       // 累计超时次数
	TotalHeartbeats     int `gorm:"column:c_total_heartbeats;default:0"`     // 累计心跳次数

	// 扩展数据
	Metadata JSONMap `gorm:"column:c_metadata;type:text;default:'{}'"` // 元数据

	// 探测配置（从节点表获取的配置信息，仅保留探测结果相关）
	LastProbeTime   *time.Time `gorm:"column:c_last_probe_time"`              // 最后探测时间
	LastProbeResult string     `gorm:"column:c_last_probe_result;default:''"` // 最后探测结果

	// 审计字段
	Invalid    int       `gorm:"column:c_invalid;default:0"`    // 删除标记
	CreateTime time.Time `gorm:"column:c_ctime;autoCreateTime"` // 创建时间
	ModifyTime time.Time `gorm:"column:c_mtime;autoUpdateTime"` // 更新时间
}

func (HeartbeatNode) TableName() string {
	return "t_heartbeat_nodes"
}

// ToDTO 转换为DTO对象
func (m *HeartbeatNode) ToDTO() *types.HeartbeatNode {
	var metadata map[string]interface{}

	if m.Metadata != nil {
		metadata = m.Metadata
	}

	return &types.HeartbeatNode{
		ID:                  m.ID,
		NodeID:              m.NodeID,
		NodeType:            m.NodeType,
		SourceService:       m.SourceService,
		LastHeartbeat:       m.LastHeartbeat,
		FirstHeartbeat:      m.FirstHeartbeat,
		ConsecutiveTimeouts: m.ConsecutiveTimeouts,
		TotalTimeouts:       m.TotalTimeouts,
		TotalHeartbeats:     m.TotalHeartbeats,
		Metadata:            metadata,
		LastProbeTime:       m.LastProbeTime,
		LastProbeResult:     m.LastProbeResult,
		CreateTime:          m.CreateTime,
		ModifyTime:          m.ModifyTime,
	}
}

// FromDTO 从DTO对象转换
func (m *HeartbeatNode) FromDTO(dto *types.HeartbeatNode) {
	m.ID = dto.ID
	m.NodeID = dto.NodeID
	m.NodeType = dto.NodeType
	m.SourceService = dto.SourceService
	m.LastHeartbeat = dto.LastHeartbeat
	m.FirstHeartbeat = dto.FirstHeartbeat
	m.ConsecutiveTimeouts = dto.ConsecutiveTimeouts
	m.TotalTimeouts = dto.TotalTimeouts
	m.TotalHeartbeats = dto.TotalHeartbeats

	if dto.Metadata != nil {
		m.Metadata = JSONMap(dto.Metadata)
	}

	m.LastProbeTime = dto.LastProbeTime
	m.LastProbeResult = dto.LastProbeResult
	m.CreateTime = dto.CreateTime
	m.ModifyTime = dto.ModifyTime
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
