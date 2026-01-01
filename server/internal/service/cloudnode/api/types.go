package api

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
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
	SourceService       string                 `json:"source_service"`
	Status              int                    `json:"status"`
	Message             string                 `json:"message,omitempty"`
	Metadata            map[string]interface{} `json:"metadata,omitempty"`
	SupportedCollectors []string               `json:"supported_collectors,omitempty"` // 支持的采集器类型
}

// NodeRegisterRequest HTTP节点注册请求
type NodeRegisterRequest struct {
	NodeID        string                 `json:"node_id" binding:"required"`
	NodeType      string                 `json:"node_type" binding:"required"`
	SourceService string                 `json:"source_service"`
	ProbeConfig   map[string]interface{} `json:"probe_config,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// NodeConfigUpdateRequest HTTP节点配置更新请求
type NodeConfigUpdateRequest struct {
	ProbeEnabled *bool                  `json:"probe_enabled,omitempty"`
	ProbeConfig  map[string]interface{} `json:"probe_config,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// NodeListRequest 节点列表请求
type NodeListRequest struct {
	NodeIDs       []string `form:"node_ids" json:"node_ids"`
	NodeTypes     []string `form:"node_types" json:"node_types"`
	SourceService string   `form:"source_service" json:"source_service"`
	Status        *int     `form:"status" json:"status"`
	ProbeEnabled  *bool    `form:"probe_enabled" json:"probe_enabled"`
	Keyword       string   `form:"keyword" json:"keyword"`
	Page          int      `form:"page" json:"page"`
	PageSize      int      `form:"page_size" json:"page_size"`
	SortBy        string   `form:"sort_by" json:"sort_by"`
	SortOrder     string   `form:"sort_order" json:"sort_order"`
}

// ProbeLogListRequest 探测日志列表请求
type ProbeLogListRequest struct {
	NodeID    string `form:"node_id" json:"node_id"`
	NodeType  string `form:"node_type" json:"node_type"`
	ProbeID   string `form:"probe_id" json:"probe_id"`
	Result    *bool  `form:"result" json:"result"`
	StartTime string `form:"start_time" json:"start_time"`
	EndTime   string `form:"end_time" json:"end_time"`
	Page      int    `form:"page" json:"page"`
	PageSize  int    `form:"page_size" json:"page_size"`
}

