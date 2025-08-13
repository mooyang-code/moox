package collector

import (
	"context"

	apisvr "github.com/mooyang-code/moox/server/internal/service/apirouter"
	cloudnodeapi "github.com/mooyang-code/moox/server/internal/service/cloudnode/api"
)

// RegisterCloudNodeHandler 注册CloudNode处理器到主API系统
func RegisterCloudNodeHandler(handler cloudnodeapi.SchemaHandler) {
	// 创建适配器，将cloudnode的SchemaHandler适配到主API系统的接口
	adapter := &cloudNodeHandlerAdapter{handler: handler}
	apisvr.GetAPIHandleInstance().Register(adapter)
}

// cloudNodeHandlerAdapter 适配器，将cloudnode的SchemaHandler适配到api的SchemaHandler
type cloudNodeHandlerAdapter struct {
	handler cloudnodeapi.SchemaHandler
}

// InterfaceID 实现api.SchemaHandler接口
func (a *cloudNodeHandlerAdapter) InterfaceID() string {
	return a.handler.SchemaID()
}

// GetHandle 实现api.SchemaHandler接口
func (a *cloudNodeHandlerAdapter) GetHandle(ctx context.Context, params map[string]string) (*apisvr.APIRsp, error) {
	rsp, err := a.handler.GetHandle(ctx, params)
	if err != nil {
		return nil, err
	}

	// 转换响应格式
	return &apisvr.APIRsp{
		Code: rsp.Code,
		Data: convertToSlice(rsp.Data),
	}, nil
}

// PostHandle 实现api.SchemaHandler接口
func (a *cloudNodeHandlerAdapter) PostHandle(ctx context.Context, params map[string]string) (*apisvr.APIRsp, error) {
	rsp, err := a.handler.PostHandle(ctx, params)
	if err != nil {
		return nil, err
	}

	// 转换响应格式
	return &apisvr.APIRsp{
		Code: rsp.Code,
		Data: convertToSlice(rsp.Data),
	}, nil
}