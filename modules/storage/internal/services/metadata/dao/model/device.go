package model

// StorageDevice 存储设备表结构
type StorageDevice struct {
	// ID 自增ID
	ID int `json:"_id" yaml:"id" gorm:"column:c_id;primaryKey;autoIncrement"`
	// DeviceID 存储设备ID
	DeviceID int `json:"device_id" yaml:"device_id" gorm:"column:c_device_id;uniqueIndex;not null;default:0"`
	// DeviceName 存储设备名
	DeviceName string `json:"device_name" yaml:"device_name" gorm:"column:c_device_name;type:varchar(30);not null;default:0"`
	// DeviceType 存储设备类型
	DeviceType int `json:"device_type" yaml:"device_type" gorm:"column:c_device_type;not null;default:0"`
	// ConnInfo 存储连接信息
	ConnInfo string `json:"conn_info" yaml:"conn_info" gorm:"column:c_conn_info;type:varchar(250);not null;default:''"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled" yaml:"enabled" gorm:"column:c_enabled;type:text;not null;default:'true'"`
	// CreateTime 创建时间
	CreateTime string `json:"create_time" yaml:"create_time" gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP"`
	// ModifyTime 修改时间
	ModifyTime string `json:"modify_time" yaml:"modify_time" gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP"`
}

const StorageDeviceTableName = "t_storage_device"

// TableName 指定表名
func (s *StorageDevice) TableName() string {
	return StorageDeviceTableName
}
