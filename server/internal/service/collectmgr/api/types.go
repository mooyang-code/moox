package api

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/common"
)

// APIResponse 使用统一的响应格式
type APIResponse = common.UnifiedAPIResponse

// CreateTaskRuleResponse 创建任务规则响应
type CreateTaskRuleResponse struct {
	RuleID string `json:"rule_id"`
}

// SchemaHandler 数据表读写接口
type SchemaHandler interface {
	SchemaID() string
	GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error)
	PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error)
}

// 使用统一的响应函数
var SuccessResponse = common.SuccessResponse
var HandleAppError = common.HandleAppError
var PaginatedListResponse = common.PaginatedListResponse
