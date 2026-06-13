package cache

import (
	"fmt"
	"strconv"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
)

const TBDev = "t_storage_device"

// StorageDevice 存储设备表结构
type StorageDevice struct {
	// DeviceID 存储设备ID
	DeviceID int `json:"device_id"`
	// DeviceName 存储设备名
	DeviceName string `json:"device_name"`
	// DeviceType 存储设备类型
	DeviceType int `json:"device_type"`
	// ConnInfo 存储连接信息
	ConnInfo string `json:"conn_info"`
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `json:"enabled"`
	// AccessUrl 访问该表的接口url
	AccessUrl string
}

// SchemaID 实现接口TableCacher
func (StorageDevice) SchemaID() string {
	return TBDev
}

// URL 实现接口TableCacher
func (s StorageDevice) URL() string {
	return s.AccessUrl
}

// SearchFields 实现接口TableCacher
func (StorageDevice) SearchFields() map[string]string {
	return map[string]string{TBDev: "device_id"}
}

// FilterKey 实现接口TableCacher
func (StorageDevice) FilterKey() string {
	return "enabled=true"
}

// GetStorageDeviceInfo 获取存储设备配置缓存
func GetStorageDeviceInfo(deviceID int) *StorageDevice {
	appField, ok := QueryDataItem(TBDev, "device_id="+strconv.Itoa(deviceID)).(*StorageDevice)
	if !ok {
		return nil
	}
	return appField
}

// GetAllStorageDeviceInfo 获取所有存储设备配置缓存
func GetAllStorageDeviceInfo() []*StorageDevice {
	appFields, ok := GetAll(TBDev).([]*StorageDevice)
	if !ok {
		return nil
	}
	return appFields
}

// GetAllStorageDevices 获取所有存储设备列表
// 当前实现：所有存储实体一视同仁，其都可以读写所有存储设备
func GetAllStorageDevices() ([]*StorageDevice, error) {
	allDevices := GetAllStorageDeviceInfo()
	if allDevices == nil {
		return nil, fmt.Errorf("获取存储设备列表失败")
	}

	var result []*StorageDevice
	for _, device := range allDevices {
		if device.Enabled == constants.EnabledValue { // 只返回启用的设备
			result = append(result, device)
		}
	}
	return result, nil
}
