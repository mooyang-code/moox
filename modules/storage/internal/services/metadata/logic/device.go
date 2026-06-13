package logic

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/errors"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// CreateStorageDevice 创建存储设备
func (x *MetaServicerImpl) CreateStorageDevice(ctx context.Context, req *pb.CreateStorageDeviceReq) (*pb.CreateStorageDeviceRsp, error) {
	log.InfoContextf(ctx, "CreateStorageDevice enter:%+v", req)
	rsp := &pb.CreateStorageDeviceRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "创建存储设备成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "CreateStorageDevice failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetDeviceName() == "" {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备名不能为空"))
		return rsp, nil
	}
	if req.GetDeviceType() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备类型必须大于0"))
		return rsp, nil
	}
	if req.GetConnInfo() == "" {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储连接信息不能为空"))
		return rsp, nil
	}

	// 生成新的存储设备ID
	maxDeviceID, err := x.dbDAO.GetMaxDeviceID()
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}
	newDeviceID := maxDeviceID + 1

	// 构建存储设备模型
	device := &model.StorageDevice{
		DeviceID:   newDeviceID,
		DeviceName: req.GetDeviceName(),
		DeviceType: int(req.GetDeviceType()),
		ConnInfo:   req.GetConnInfo(),
		Enabled:    constants.EnabledValue,
		CreateTime: time.Now().Format("2006-01-02 15:04:05"),
		ModifyTime: time.Now().Format("2006-01-02 15:04:05"),
	}

	// 保存到数据库
	if err := x.dbDAO.AddDevice(device); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	rsp.DeviceId = uint32(newDeviceID)
	log.InfoContextf(ctx, "CreateStorageDevice response: %+v", rsp)
	return rsp, nil
}

// UpdateStorageDevice 更新存储设备（只能修改设备名）
func (x *MetaServicerImpl) UpdateStorageDevice(ctx context.Context, req *pb.UpdateStorageDeviceReq) (*pb.UpdateStorageDeviceRsp, error) {
	log.InfoContextf(ctx, "UpdateStorageDevice enter:%+v", req)
	rsp := &pb.UpdateStorageDeviceRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "更新存储设备成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpdateStorageDevice failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetDeviceId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备ID必须大于0"))
		return rsp, nil
	}
	if req.GetDeviceName() == "" {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备名不能为空"))
		return rsp, nil
	}

	// 检查存储设备是否存在
	existDevice, err := x.dbDAO.GetDeviceByID(int(req.GetDeviceId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备不存在"))
		return rsp, nil
	}

	// 更新字段（只允许修改设备名）
	existDevice.DeviceName = req.GetDeviceName()
	existDevice.ModifyTime = time.Now().Format("2006-01-02 15:04:05")

	// 保存到数据库
	if err := x.dbDAO.UpdateDevice(existDevice); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "UpdateStorageDevice response: %+v", rsp)
	return rsp, nil
}

// DeleteStorageDevice 删除存储设备（检查字段路由引用）
func (x *MetaServicerImpl) DeleteStorageDevice(ctx context.Context, req *pb.DeleteStorageDeviceReq) (*pb.DeleteStorageDeviceRsp, error) {
	log.InfoContextf(ctx, "DeleteStorageDevice enter:%+v", req)
	rsp := &pb.DeleteStorageDeviceRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "删除存储设备成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "DeleteStorageDevice failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetDeviceId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备ID必须大于0"))
		return rsp, nil
	}

	deviceID := int(req.GetDeviceId())

	// 检查存储设备是否存在
	_, err := x.dbDAO.GetDeviceByID(deviceID)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备不存在"))
		return rsp, nil
	}

	// 检查是否被字段路由引用
	isReferenced, err := x.dbDAO.IsDeviceReferencedByFieldRoute(deviceID)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}
	if isReferenced {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备已被字段路由引用，无法删除"))
		return rsp, nil
	}

	// 执行禁用
	if err := x.dbDAO.DeleteDevice(deviceID); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "DeleteStorageDevice response: %+v", rsp)
	return rsp, nil
}

// ListStorageDevices 列出所有存储设备
func (x *MetaServicerImpl) ListStorageDevices(ctx context.Context, req *pb.ListStorageDevicesReq) (*pb.ListStorageDevicesRsp, error) {
	log.InfoContextf(ctx, "ListStorageDevices enter:%+v", req)
	rsp := &pb.ListStorageDevicesRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "获取存储设备列表成功",
		},
		Devices: []*pb.StorageDeviceInfo{},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "ListStorageDevices failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 从数据库获取存储设备列表
	devices, err := x.dbDAO.GetDeviceList()
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}

	// 转换为proto格式
	for _, device := range devices {
		deviceInfo := &pb.StorageDeviceInfo{
			DeviceId:   uint32(device.DeviceID),
			DeviceName: device.DeviceName,
			DeviceType: uint32(device.DeviceType),
			ConnInfo:   device.ConnInfo,
			Ctime:      device.CreateTime,
			Mtime:      device.ModifyTime,
			Enabled:    device.Enabled,
		}
		rsp.Devices = append(rsp.Devices, deviceInfo)
	}

	log.InfoContextf(ctx, "ListStorageDevices response: count=%d", len(rsp.Devices))
	return rsp, nil
}
