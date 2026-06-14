// Package constants 提供系统中使用的常量定义
package constants

// PublisherType 消息发布器类型
type PublisherType string

// NATSPublisherType 预定义发布器类型
const (
	NATSPublisherType PublisherType = "nats"
	// 将来可以添加其他发布器类型，如Kafka、RabbitMQ等
)

// ServerType 消息服务器类型
type ServerType string

// NATSServerType 预定义服务器类型
const (
	NATSServerType ServerType = "nats"
	// 将来可以添加其他服务器类型，如Kafka、RabbitMQ等
)

// 字段路由相关常量
const (
	// AllFieldsMarker 表示"所有字段"的标记值
	// 使用较大的值避免与实际字段ID冲突，也避免0值在修改写入时被忽略的问题
	AllFieldsMarker = 999999999
)

// 常用常量
const (
	EnabledValue  = "true"
	DisabledValue = "false"
)

// Separator 本系统中默认的多值分隔符
const Separator string = "+"

// SystemFieldIDStart 系统字段ID起始值
const SystemFieldIDStart = 100000

// SystemFieldInfo 系统字段信息
type SystemFieldInfo struct {
	FieldID     uint32 // 字段ID
	FieldType   int    // 字段类型
	Description string // 字段描述
}

// SystemFields 系统字段列表（保持向后兼容）
var (
	SystemFields = map[string]bool{
		"_row_id":            true,
		"_ctime":             true,
		"_mtime":             true,
		"_replay_timestamps": true,
		"_times":             true,
		"_deleted":           true,
		"_deleted_time":      true,
		"_extended_data":     true,
		"_metadata":          true,
	}

	// SystemFieldInfoMap 系统字段详细信息映射
	SystemFieldInfoMap = map[string]SystemFieldInfo{
		"_row_id": {
			FieldID:     SystemFieldIDStart + 1, // 100001
			FieldType:   1,
			Description: "数据行唯一标识符",
		},
		"_ctime": {
			FieldID:     SystemFieldIDStart + 2, // 100002
			FieldType:   4,
			Description: "数据创建时间",
		},
		"_mtime": {
			FieldID:     SystemFieldIDStart + 3, // 100003
			FieldType:   4,
			Description: "数据修改时间",
		},
		"_replay_timestamps": {
			FieldID:     SystemFieldIDStart + 4, // 100004
			FieldType:   1,
			Description: "重放时间戳",
		},
		"_times": {
			FieldID:     SystemFieldIDStart + 5, // 100005
			FieldType:   4,
			Description: "时序数据时间字段",
		},
		"_deleted": {
			FieldID:     SystemFieldIDStart + 6, // 100006
			FieldType:   2,
			Description: "软删除标记",
		},
		"_deleted_time": {
			FieldID:     SystemFieldIDStart + 7, // 100007
			FieldType:   4,
			Description: "删除时间",
		},
		"_extended_data": {
			FieldID:     SystemFieldIDStart + 8, // 100008
			FieldType:   1,
			Description: "扩展数据字段",
		},
		"_metadata": {
			FieldID:     SystemFieldIDStart + 9, // 100009
			FieldType:   1,
			Description: "元数据字段",
		},
	}

	// SystemFieldName2ID 系统字段名到字段ID的映射
	SystemFieldName2ID map[string]uint32

	// SystemFieldID2Name 系统字段ID到字段名的映射
	SystemFieldID2Name map[uint32]string
)

// init 初始化系统字段映射
func init() {
	SystemFieldName2ID = make(map[string]uint32)
	SystemFieldID2Name = make(map[uint32]string)

	for fieldName, fieldInfo := range SystemFieldInfoMap {
		SystemFieldName2ID[fieldName] = fieldInfo.FieldID
		SystemFieldID2Name[fieldInfo.FieldID] = fieldName
	}
}
