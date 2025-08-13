package api

import (
	"context"
)

// APIResponse 接口统一的返回信息（兼容wuji返回格式）
type APIResponse struct {
	Code int         `json:"code"`
	Data interface{} `json:"data"`
}

// SchemaHandler 数据表读写接口
type SchemaHandler interface {
	SchemaID() string
	GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error)
	PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error)
}