package api

import (
	"github.com/mooyang-code/moox/modules/control/internal/errors"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/types"

	"github.com/gin-gonic/gin"
)

// HeartbeatHandler 心跳接口处理器
type HeartbeatHandler struct {
	service cloudnode.HeartbeatService
}

// NewHeartbeatHandler 创建心跳接口处理器
func NewHeartbeatHandler(service cloudnode.HeartbeatService) *HeartbeatHandler {
	return &HeartbeatHandler{
		service: service,
	}
}

// ReportHeartbeat 上报心跳
// @Summary 上报心跳
// @Description 节点上报心跳信息，并返回服务端包版本信息
// @Tags 心跳管理
// @Accept json
// @Produce json
// @Param request body HeartbeatReportRequest true "心跳上报请求"
// @Success 200 {object} APIResponse{result=ReportHeartbeatResponse} "心跳上报成功，返回服务端信息"
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/report [post]
func (h *HeartbeatHandler) ReportHeartbeat(c *gin.Context) {
	var req HeartbeatReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleAppError(c, errors.InvalidParam("request", err.Error()))
		return
	}

	// 转换为服务层请求
	serviceReq := &types.ReportHeartbeatRequest{
		NodeID:              req.NodeID,
		NodeType:            req.NodeType,
		RunningVersion:      req.RunningVersion,
		SourceService:       req.SourceService,
		Metadata:            req.Metadata,
		SupportedCollectors: req.SupportedCollectors,
		TasksMD5:            req.TasksMD5,
	}

	response, err := h.service.ReportHeartbeat(c.Request.Context(), serviceReq)
	if err != nil {
		HandleAppError(c, errors.Internal("report heartbeat failed", err))
		return
	}
	SuccessResponse(c, "heartbeat reported successfully", response)
}

