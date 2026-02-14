package api

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/common"
	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	ssh "github.com/mooyang-code/moox/server/internal/service/ssh"
	"github.com/mooyang-code/moox/server/internal/service/ssh/model"
)

// HostHandler 主机配置 handler
type HostHandler struct {
	svc ssh.Service
}

// NewHostHandler 创建 handler
func NewHostHandler(svc ssh.Service) *HostHandler {
	return &HostHandler{svc: svc}
}

// ListHosts 主机列表
func (h *HostHandler) ListHosts(c *gin.Context) {
	var req ListHostsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}

	hosts, total, err := h.svc.ListHosts(c.Request.Context(), req.Keyword, req.Offset, req.Limit)
	if err != nil {
		common.HandleAppError(c, apperrors.Internal("查询主机列表失败", err))
		return
	}

	// 隐藏敏感字段
	for i := range hosts {
		hosts[i].Password = ""
		hosts[i].CertData = ""
		hosts[i].CertPwd = ""
	}

	common.PaginatedListResponse(c, "ok", hosts, total)
}

// CreateHost 创建主机
func (h *HostHandler) CreateHost(c *gin.Context) {
	var req CreateHostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	host := &model.SSHHost{
		Name:        req.Name,
		Address:     req.Address,
		Port:        req.Port,
		User:        req.User,
		Password:    req.Password,
		AuthType:    req.AuthType,
		NetType:     req.NetType,
		CertData:    req.CertData,
		CertPwd:     req.CertPwd,
		FontSize:    withDefault(req.FontSize, 14),
		Background:  withDefaultStr(req.Background, "#000000"),
		Foreground:  withDefaultStr(req.Foreground, "#FFFFFF"),
		CursorColor: withDefaultStr(req.CursorColor, "#FFFFFF"),
		FontFamily:  withDefaultStr(req.FontFamily, "Courier New"),
		CursorStyle: withDefaultStr(req.CursorStyle, "block"),
		Shell:       withDefaultStr(req.Shell, "bash"),
		PtyType:     withDefaultStr(req.PtyType, "xterm-256color"),
		InitCmd:     req.InitCmd,
	}

	if err := h.svc.CreateHost(c.Request.Context(), host); err != nil {
		common.HandleAppError(c, apperrors.Internal("创建主机失败", err))
		return
	}

	common.SuccessResponse(c, "创建成功", host.ID)
}

// UpdateHost 更新主机
func (h *HostHandler) UpdateHost(c *gin.Context) {
	var req UpdateHostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	host := &model.SSHHost{
		ID:          req.ID,
		Name:        req.Name,
		Address:     req.Address,
		Port:        req.Port,
		User:        req.User,
		Password:    req.Password,
		AuthType:    req.AuthType,
		NetType:     req.NetType,
		CertData:    req.CertData,
		CertPwd:     req.CertPwd,
		FontSize:    withDefault(req.FontSize, 14),
		Background:  withDefaultStr(req.Background, "#000000"),
		Foreground:  withDefaultStr(req.Foreground, "#FFFFFF"),
		CursorColor: withDefaultStr(req.CursorColor, "#FFFFFF"),
		FontFamily:  withDefaultStr(req.FontFamily, "Courier New"),
		CursorStyle: withDefaultStr(req.CursorStyle, "block"),
		Shell:       withDefaultStr(req.Shell, "bash"),
		PtyType:     withDefaultStr(req.PtyType, "xterm-256color"),
		InitCmd:     req.InitCmd,
	}

	if err := h.svc.UpdateHost(c.Request.Context(), host); err != nil {
		common.HandleAppError(c, apperrors.Internal("更新主机失败", err))
		return
	}

	common.SuccessResponse(c, "更新成功", nil)
}

// DeleteHost 删除主机
func (h *HostHandler) DeleteHost(c *gin.Context) {
	var req DeleteHostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	if err := h.svc.DeleteHost(c.Request.Context(), req.ID); err != nil {
		common.HandleAppError(c, apperrors.Internal("删除主机失败", err))
		return
	}

	common.SuccessResponse(c, "删除成功", nil)
}

// GetHost 获取主机详情
func (h *HostHandler) GetHost(c *gin.Context) {
	idStr := c.Query("id")
	if idStr == "" {
		common.HandleAppError(c, apperrors.InvalidParam("id", "id 不能为空"))
		return
	}

	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("id", "id 格式不正确"))
		return
	}

	host, err := h.svc.GetHost(c.Request.Context(), id)
	if err != nil {
		common.HandleAppError(c, apperrors.NotFound("主机"))
		return
	}

	// 隐藏敏感字段
	host.Password = ""
	host.CertData = ""
	host.CertPwd = ""

	common.SuccessResponse(c, "ok", host)
}

func withDefault(val, def int) int {
	if val == 0 {
		return def
	}
	return val
}

func withDefaultStr(val, def string) string {
	if val == "" {
		return def
	}
	return val
}
