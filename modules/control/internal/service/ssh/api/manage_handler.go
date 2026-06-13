package api

import (
	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/modules/control/internal/common"
	apperrors "github.com/mooyang-code/moox/modules/control/internal/errors"
	ssh "github.com/mooyang-code/moox/modules/control/internal/service/ssh"
)

// ManageHandler 会话管理 handler
type ManageHandler struct {
	svc ssh.Service
}

// NewManageHandler 创建 handler
func NewManageHandler(svc ssh.Service) *ManageHandler {
	return &ManageHandler{svc: svc}
}

// GetOnlineSessions 获取在线会话列表
func (h *ManageHandler) GetOnlineSessions(c *gin.Context) {
	sessions := h.svc.GetOnlineSessions(c.Request.Context())
	common.SuccessResponse(c, "ok", sessions)
}

// ForceDisconnect 强制断开会话
func (h *ManageHandler) ForceDisconnect(c *gin.Context) {
	var req ForceDisconnectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	if err := h.svc.ForceDisconnect(c.Request.Context(), req.SessionID); err != nil {
		common.HandleAppError(c, apperrors.Internal("强制断开失败", err))
		return
	}

	common.SuccessResponse(c, "断开成功", nil)
}
