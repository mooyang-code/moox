package api

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// ObjectRoute 数据对象路由表结构
type ObjectRoute struct {
	model.ObjectRoute
	dbDAO dao.DataInterfacer
}

var NewObjectRoute = func() SchemaHandler {
	var imp ObjectRoute
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		log.Fatalf("NewObjectRoute NewDataInterfacer:%+v", err)
	}
	return &imp
}

// RegisterObjectRouteHandler 注册数据对象路由处理器到API入口
func RegisterObjectRouteHandler() {
	// 注册数据对象路由表处理器
	GetAPIHandleInstance().Register(NewObjectRoute())
}

// SchemaID 实现接口TBItem
func (ObjectRoute) SchemaID() string {
	return model.ObjectRouteTableName
}

// GetHandle 数据对象路由表的读接口入口
func (s ObjectRoute) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-GetHandle:%s, params:%+v", s.SchemaID(), params)

	// 获取路由列表
	routes, err := s.dbDAO.GetObjectRouteList()
	if err != nil {
		log.ErrorContextf(ctx, "GetObjectRouteList失败: %v", err)
		return &APIRsp{
			Code: 500,
			Data: []interface{}{},
		}, err
	}

	// 将路由列表转换为接口切片
	dataList := make([]interface{}, 0, len(routes))
	for _, route := range routes {
		dataList = append(dataList, route)
	}

	// 返回成功响应
	return &APIRsp{
		Code: 200,
		Data: dataList,
	}, nil
}

// PostHandle 数据对象路由表的写接口入口
func (s ObjectRoute) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-PostHandle, params:%+v", params)
	return &APIRsp{Code: 200, Data: []interface{}{}}, nil
}

// FieldRoute 字段路由表结构
type FieldRoute struct {
	model.FieldRoute
	dbDAO dao.DataInterfacer
}

var NewFieldRoute = func() SchemaHandler {
	var imp FieldRoute
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		log.Fatalf("NewFieldRoute NewDataInterfacer:%+v", err)
	}
	return &imp
}

// RegisterFieldRouteHandler 注册字段路由处理器到API入口
func RegisterFieldRouteHandler() {
	// 注册字段路由表处理器
	GetAPIHandleInstance().Register(NewFieldRoute())
}

// SchemaID 实现接口TBItem
func (FieldRoute) SchemaID() string {
	return model.FieldRouteTableName
}

// GetHandle 字段路由表的读接口入口
func (s FieldRoute) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-GetHandle, params:%+v", params)

	// 获取路由列表
	routes, err := s.dbDAO.GetFieldRouteList()
	if err != nil {
		log.ErrorContextf(ctx, "GetFieldRouteList失败: %v", err)
		return &APIRsp{
			Code: 500,
			Data: []interface{}{},
		}, err
	}

	// 将路由列表转换为接口切片
	dataList := make([]interface{}, 0, len(routes))
	for _, route := range routes {
		dataList = append(dataList, route)
	}

	// 返回成功响应
	return &APIRsp{
		Code: 200,
		Data: dataList,
	}, nil
}

// PostHandle 字段路由表的写接口入口
func (s FieldRoute) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-PostHandle, params:%+v", params)
	return &APIRsp{Code: 200, Data: []interface{}{}}, nil
}
