package api

import (
	"github.com/mooyang-code/moox/server/internal/common"
)

// APIResponse 使用统一的响应格式
type APIResponse = common.UnifiedAPIResponse

// PackageOptionVO 代码包选项视图对象
type PackageOptionVO struct {
	ID          int64  `json:"id"`
	Label       string `json:"label"`       // 显示标签
	PackageName string `json:"package_name"`
	Version     string `json:"version"`
	Runtime     string `json:"runtime"`
	PackageType string `json:"package_type"`
}

// 使用统一的响应函数
var SuccessResponse = common.SuccessResponse
var ErrorResponse = common.ErrorResponse
var PaginatedListResponse = common.PaginatedListResponse