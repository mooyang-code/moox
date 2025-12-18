package api

import (
	"github.com/mooyang-code/moox/server/internal/common"
)

// APIResponse 使用统一的响应格式
type APIResponse = common.UnifiedAPIResponse

// 使用统一的响应函数
var SuccessResponse = common.SuccessResponse
var HandleAppError = common.HandleAppError
var PaginatedListResponse = common.PaginatedListResponse
