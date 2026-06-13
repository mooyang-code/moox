package api

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// StorageEntity 存储实体表结构
type StorageEntity struct {
	model.StorageEntity
	dbDAO dao.DataInterfacer
}

var NewStorageEntity = func() SchemaHandler {
	var imp StorageEntity
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		log.Fatalf("NewStorageEntity NewDataInterfacer:%+v", err)
	}
	return &imp
}

// RegisterEntityHandler 注册存储实体处理器到API入口
func RegisterEntityHandler() {
	// 注册实体表处理器
	GetAPIHandleInstance().Register(NewStorageEntity())
}

// SchemaID 实现接口TBItem
func (StorageEntity) SchemaID() string {
	return model.StorageEntityTableName
}

// GetHandle 存储实体表的读接口入口
func (s StorageEntity) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-GetHandle:%s, params:%+v", s.SchemaID(), params)

	// 获取实体列表
	entities, err := s.dbDAO.GetEntityList()
	if err != nil {
		log.ErrorContextf(ctx, "GetEntityList失败: %v", err)
		return &APIRsp{
			Code: 500,
			Data: []interface{}{},
		}, err
	}

	// 将实体列表转换为接口切片
	dataList := make([]interface{}, 0, len(entities))
	for _, entity := range entities {
		dataList = append(dataList, entity)
	}

	// 返回成功响应
	return &APIRsp{
		Code: 200,
		Data: dataList,
	}, nil
}

// PostHandle 存储实体表的写接口入口
func (s StorageEntity) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-PostHandle, params:%+v", params)
	return &APIRsp{Code: 200, Data: []interface{}{}}, nil
}
