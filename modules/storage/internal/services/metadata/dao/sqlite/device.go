package sqlite

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// Device 存储设备表定义
type Device model.StorageDevice

// TableName 指定表名
func (d Device) TableName() string {
	return model.StorageDeviceTableName
}

// GetDeviceList 获取所有存储设备列表
func (d *dataDBImpl) GetDeviceList() ([]model.StorageDevice, error) {
	var devices []model.StorageDevice
	result := d.db.Where("c_enabled = ?", constants.EnabledValue).Find(&devices)
	if result.Error != nil {
		log.Errorf("GetDeviceList err[%v]", result.Error)
		return nil, result.Error
	}
	return devices, nil
}

// GetDeviceByID 根据ID获取存储设备
func (d *dataDBImpl) GetDeviceByID(deviceID int) (*model.StorageDevice, error) {
	var device model.StorageDevice
	result := d.db.Where("c_device_id = ? AND c_enabled = ?", deviceID, constants.EnabledValue).First(&device)
	if result.Error != nil {
		log.Errorf("GetDeviceByID err[%v]", result.Error)
		return nil, result.Error
	}
	return &device, nil
}

// AddDevice 添加新的存储设备
func (d *dataDBImpl) AddDevice(device *model.StorageDevice) error {
	if device.Enabled == "" {
		device.Enabled = constants.EnabledValue
	}
	result := d.db.Create(device)
	if result.Error != nil {
		log.Errorf("AddDevice err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// UpdateDevice 更新存储设备信息
func (d *dataDBImpl) UpdateDevice(device *model.StorageDevice) error {
	result := d.db.Model(&model.StorageDevice{}).Where("c_device_id = ?", device.DeviceID).
		Updates(map[string]interface{}{
			"c_device_name": device.DeviceName,
			"c_device_type": device.DeviceType,
			"c_conn_info":   device.ConnInfo,
		})
	if result.Error != nil {
		log.Errorf("UpdateDevice err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// DeleteDevice 逻辑删除存储设备
func (d *dataDBImpl) DeleteDevice(deviceID int) error {
	result := d.db.Model(&model.StorageDevice{}).Where("c_device_id = ?", deviceID).
		Update("c_enabled", constants.DisabledValue)
	if result.Error != nil {
		log.Errorf("DeleteDevice err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// GetMaxDeviceID 获取当前最大的存储设备ID
func (d *dataDBImpl) GetMaxDeviceID() (int, error) {
	var maxID int
	result := d.db.Model(&model.StorageDevice{}).Select("COALESCE(MAX(c_device_id), 0)").Scan(&maxID)
	if result.Error != nil {
		log.Errorf("GetMaxDeviceID err[%v]", result.Error)
		return 0, result.Error
	}
	return maxID, nil
}

// IsDeviceReferencedByFieldRoute 检查存储设备是否被字段路由引用
func (d *dataDBImpl) IsDeviceReferencedByFieldRoute(deviceID int) (bool, error) {
	var count int64
	result := d.db.Model(&model.FieldRoute{}).Where("c_device_id = ? AND c_enabled = ?",
		deviceID, constants.EnabledValue).Count(&count)
	if result.Error != nil {
		log.Errorf("IsDeviceReferencedByFieldRoute err[%v]", result.Error)
		return false, result.Error
	}
	return count > 0, nil
}
