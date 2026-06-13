package api

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// Field 字段表结构
type Field struct {
	model.Field
	dbDAO dao.DataInterfacer
}

var NewField = func() SchemaHandler {
	var imp Field
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		log.Fatalf("NewField NewDataInterfacer:%+v", err)
	}
	return &imp
}

// RegisterFieldHandler 注册字段处理器到API入口
func RegisterFieldHandler() {
	// 注册字段表处理器
	GetAPIHandleInstance().Register(NewField())
}

// SchemaID 实现接口TBItem
func (Field) SchemaID() string {
	return model.FieldTableName
}

// GetHandle 字段表的读接口入口
func (s Field) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-GetHandle:%s, params:%+v", s.SchemaID(), params)

	// 如果有参数指定interface_name，则调用GetFieldItems获取特定字段
	if interfaceName, ok := params["interface_name"]; ok && interfaceName != "" {
		fields, err := s.dbDAO.GetFieldItems(interfaceName)
		if err != nil {
			log.ErrorContextf(ctx, "GetFieldItems失败: %v", err)
			return &APIRsp{
				Code: 500,
				Data: []interface{}{},
			}, err
		}

		// 将字段列表转换为接口切片
		dataList := make([]interface{}, 0, len(fields))
		for _, field := range fields {
			dataList = append(dataList, field)
		}

		// 返回成功响应
		return &APIRsp{
			Code: 200,
			Data: dataList,
		}, nil
	}

	// 没有指定interface_name，则获取所有字段
	fields, err := s.getAllFields(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "GetFieldList失败: %v", err)
		return &APIRsp{
			Code: 500,
			Data: []interface{}{},
		}, err
	}

	// 将字段列表转换为接口切片
	dataList := make([]interface{}, 0, len(fields))
	for _, field := range fields {
		dataList = append(dataList, field)
	}

	// 返回成功响应
	return &APIRsp{
		Code: 200,
		Data: dataList,
	}, nil
}

// getAllFields 获取所有字段
func (s Field) getAllFields(ctx context.Context) ([]model.Field, error) {
	fields, err := s.dbDAO.GetValidFieldList()
	if err != nil {
		log.ErrorContextf(ctx, "GetValidFieldList失败: %v", err)
		return nil, err
	}
	return fields, nil
}

// PostHandle 字段表的写接口入口
func (s Field) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-PostHandle, params:%+v", params)
	return &APIRsp{Code: 200, Data: []interface{}{}}, nil
}
