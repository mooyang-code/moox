package api

import (
	"github.com/gin-gonic/gin"

	cloudnode "github.com/mooyang-code/moox/server/internal/service/cloudnode"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
	"github.com/mooyang-code/moox/server/internal/errors"
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
// @Description 节点上报心跳信息
// @Tags 心跳管理
// @Accept json
// @Produce json
// @Param request body HeartbeatReportRequest true "心跳上报请求"
// @Success 200 {object} APIResponse
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
		NodeID:        req.NodeID,
		NodeType:      req.NodeType,
		SourceService: req.SourceService,
		Metadata:      req.Metadata,
	}

	if err := h.service.ReportHeartbeat(c.Request.Context(), serviceReq); err != nil {
		HandleAppError(c, errors.Internal("report heartbeat failed", err))
		return
	}

	SuccessResponse(c, "heartbeat reported successfully", nil)
}

// BatchReportHeartbeat 批量上报心跳
// @Summary 批量上报心跳
// @Description 批量上报多个节点的心跳信息
// @Tags 心跳管理
// @Accept json
// @Produce json
// @Param request body BatchHeartbeatReportRequest true "批量心跳上报请求"
// @Success 200 {object} APIResponse
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/batch-report [post]
func (h *HeartbeatHandler) BatchReportHeartbeat(c *gin.Context) {
	var req BatchHeartbeatReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleAppError(c, errors.InvalidParam("request", err.Error()))
		return
	}

	// 转换为服务层请求
	serviceReq := &types.BatchReportHeartbeatRequest{
		Heartbeats: make([]types.ReportHeartbeatRequest, len(req.Heartbeats)),
	}
	for i, hb := range req.Heartbeats {
		serviceReq.Heartbeats[i] = types.ReportHeartbeatRequest{
			NodeID:        hb.NodeID,
			NodeType:      hb.NodeType,
			SourceService: hb.SourceService,
			Metadata:      hb.Metadata,
		}
	}

	if err := h.service.BatchReportHeartbeat(c.Request.Context(), serviceReq); err != nil {
		HandleAppError(c, errors.Internal("batch report heartbeat failed", err))
		return
	}

	SuccessResponse(c, "batch heartbeat reported successfully", nil)
}