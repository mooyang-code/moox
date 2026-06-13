package api

import (
	"context"

	"github.com/mooyang-code/moox/modules/control/internal/common"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/types"
)

// APIResponse 使用统一的响应格式
type APIResponse = common.UnifiedAPIResponse

// SchemaHandler 数据表读写接口
type SchemaHandler interface {
	SchemaID() string
	GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error)
	PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error)
}

// PackageAPIResponse 使用统一的响应格式（避免与 types.go 冲突）
type PackageAPIResponse = common.UnifiedAPIResponse

// PackageOptionVO 代码包选项视图对象
type PackageOptionVO struct {
	PackageID   string `json:"package_id"` // 代码包ID(11位随机字符串)
	Label       string `json:"label"`      // 显示标签
	PackageName string `json:"package_name"`
	Version     string `json:"version"`
	Runtime     string `json:"runtime"`
	PackageType string `json:"package_type"`
}

var SuccessResponse = common.SuccessResponse
var HandleAppError = common.HandleAppError
var PaginatedListResponse = common.PaginatedListResponse

// ==================== Heartbeat API Types ====================

// HeartbeatAPIResponse 心跳API响应（使用统一响应格式）
type HeartbeatAPIResponse = common.UnifiedAPIResponse

// ReportHeartbeatResponse 心跳上报响应（用于API文档）
type ReportHeartbeatResponse = types.ReportHeartbeatResponse

// HeartbeatReportRequest HTTP心跳上报请求
type HeartbeatReportRequest struct {
	NodeID              string                 `json:"node_id" binding:"required"`
	NodeType            string                 `json:"node_type" binding:"required"`
	RunningVersion      string                 `json:"running_version"`
	SourceService       string                 `json:"source_service"`
	Status              int                    `json:"status"`
	Message             string                 `json:"message,omitempty"`
	Metadata            map[string]interface{} `json:"metadata,omitempty"`
	SupportedCollectors []string               `json:"supported_collectors,omitempty"` // 支持的采集器类型
	TasksMD5            string                 `json:"tasks_md5"`                      // 当前任务列表MD5值
}
