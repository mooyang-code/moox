package api

import (
	"github.com/gin-gonic/gin"

	cloudnode "github.com/mooyang-code/moox/server/internal/service/cloudnode"
	"github.com/mooyang-code/moox/server/internal/errors"
)

// ProbeHandler 探测接口处理器
type ProbeHandler struct {
	service cloudnode.HeartbeatService
}

// NewProbeHandler 创建探测接口处理器
func NewProbeHandler(service cloudnode.HeartbeatService) *ProbeHandler {
	return &ProbeHandler{
		service: service,
	}
}

// ProbeNode 手动探测节点
// @Summary 手动探测节点
// @Description 手动触发对指定节点的探测
// @Tags 探测管理
// @Param node_id path string true "节点ID"
// @Param node_type path string true "节点类型"
// @Param action query string false "探测动作" default(health)
// @Success 200 {object} APIResponse{data=types.ProbeResult}
// @Failure 400 {object} APIResponse
// @Failure 500 {object} APIResponse
// @Router /api/v1/heartbeat/probe/{node_id}/{node_type} [post]
func (h *ProbeHandler) ProbeNode(c *gin.Context) {
	nodeID := c.Param("node_id")
	nodeType := c.Param("node_type")
	action := c.DefaultQuery("action", "health")

	if nodeID == "" || nodeType == "" {
		HandleAppError(c, errors.InvalidParam("node_id_or_node_type", "node_id and node_type are required"))
		return
	}

	result, err := h.service.ProbeHeartbeatNode(c.Request.Context(), nodeID, nodeType, action)
	if err != nil {
		HandleAppError(c, errors.Internal("failed to probe node", err))
		return
	}

	SuccessResponse(c, "probe completed", result)
}


