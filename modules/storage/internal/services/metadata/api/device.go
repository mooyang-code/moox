package api

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// StorageDevice 存储设备表结构
type StorageDevice struct {
	model.StorageDevice
	dbDAO dao.DataInterfacer
}

var NewStorageDevice = func() SchemaHandler {
	var imp StorageDevice
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		log.Fatalf("NewStorageDevice NewDataInterfacer:%+v", err)
	}
	return &imp
}

// RegisterDeviceHandler 注册存储设备处理器到API入口
func RegisterDeviceHandler() {
	// 注册设备表处理器
	GetAPIHandleInstance().Register(NewStorageDevice())
}

// SchemaID 实现接口TBItem
func (StorageDevice) SchemaID() string {
	return model.StorageDeviceTableName
}

// GetHandle 存储设备表的读接口入口
func (s StorageDevice) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-GetHandle:%s, params:%+v", s.SchemaID(), params)

	// 获取设备列表
	devices, err := s.dbDAO.GetDeviceList()
	if err != nil {
		log.ErrorContextf(ctx, "GetDeviceList失败: %v", err)
		return &APIRsp{
			Code: 500,
			Data: []interface{}{},
		}, err
	}

	// 将设备列表转换为接口切片
	dataList := make([]interface{}, 0, len(devices))
	for _, device := range devices {
		dataList = append(dataList, device)
	}

	// 返回成功响应
	return &APIRsp{
		Code: 200,
		Data: dataList,
	}, nil
}

// PostHandle 存储设备表的写接口入口
func (s StorageDevice) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-PostHandle, params:%+v", params)
	return &APIRsp{Code: 200, Data: []interface{}{}}, nil
}
