package collector

import (
	"context"

	apisvr "github.com/mooyang-code/moox/server/internal/service/apirouter"
	"github.com/mooyang-code/moox/server/internal/service/collector/api"
)

// RegisterHandler 注册处理器到主API系统
func RegisterHandler(handler api.SchemaHandler) {
	// 创建适配器，将我们的SchemaHandler适配到主API系统的接口
	adapter := &handlerAdapter{handler: handler}
	apisvr.GetAPIHandleInstance().Register(adapter)
}

// handlerAdapter 适配器，将collector的SchemaHandler适配到api的SchemaHandler
type handlerAdapter struct {
	handler api.SchemaHandler
}

// InterfaceID 实现api.SchemaHandler接口
func (a *handlerAdapter) InterfaceID() string {
	return a.handler.SchemaID()
}

// GetHandle 实现api.SchemaHandler接口
func (a *handlerAdapter) GetHandle(ctx context.Context, params map[string]string) (*apisvr.APIRsp, error) {
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
func (a *handlerAdapter) PostHandle(ctx context.Context, params map[string]string) (*apisvr.APIRsp, error) {
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

// convertToSlice 将任意类型转换为[]any
func convertToSlice(data interface{}) []any {
	// 如果已经是[]any类型，直接返回
	if slice, ok := data.([]any); ok {
		return slice
	}
	
	// 如果是[]interface{}类型，也直接返回
	if slice, ok := data.([]interface{}); ok {
		result := make([]any, len(slice))
		for i, v := range slice {
			result[i] = v
		}
		return result
	}
	
	// 如果是字符串（比如日志内容），包装成单元素数组
	if str, ok := data.(string); ok {
		return []any{str}
	}
	
	// 如果是nil，返回空数组
	if data == nil {
		return []any{}
	}
	
	// 其他情况，将数据包装成单元素数组
	return []any{data}
}
