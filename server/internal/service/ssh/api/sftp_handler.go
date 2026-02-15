package api

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/common"
	apperrors "github.com/mooyang-code/moox/server/internal/errors"
	ssh "github.com/mooyang-code/moox/server/internal/service/ssh"

	"trpc.group/trpc-go/trpc-go/log"
)

// SFTPHandler SFTP 文件管理 handler
type SFTPHandler struct {
	svc ssh.Service
}

// NewSFTPHandler 创建 handler
func NewSFTPHandler(svc ssh.Service) *SFTPHandler {
	return &SFTPHandler{svc: svc}
}

// SftpList SFTP 目录列表
func (h *SFTPHandler) SftpList(c *gin.Context) {
	var req SftpListRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	data, err := h.svc.SftpList(c.Request.Context(), req.SessionID, req.Path)
	if err != nil {
		common.HandleAppError(c, apperrors.Internal("读取目录失败", err))
		return
	}

	common.SuccessResponse(c, "ok", data)
}

// SftpMkdir SFTP 创建目录
func (h *SFTPHandler) SftpMkdir(c *gin.Context) {
	var req SftpMkdirRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	if err := h.svc.SftpMkdir(c.Request.Context(), req.SessionID, req.Path); err != nil {
		common.HandleAppError(c, apperrors.Internal("创建目录失败", err))
		return
	}

	common.SuccessResponse(c, "创建目录成功", nil)
}

// SftpDelete SFTP 删除文件或目录
func (h *SFTPHandler) SftpDelete(c *gin.Context) {
	var req SftpDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("body", err.Error()))
		return
	}

	if err := h.svc.SftpDelete(c.Request.Context(), req.SessionID, req.Path); err != nil {
		common.HandleAppError(c, apperrors.Internal("删除失败", err))
		return
	}

	common.SuccessResponse(c, "删除成功", nil)
}

// SftpDownload SFTP 下载文件（直连端点，不走 Gateway）
func (h *SFTPHandler) SftpDownload(c *gin.Context) {
	sessionID := c.Query("session_id")
	filePath, err := url.QueryUnescape(c.Query("path"))
	if err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("path", "路径格式不正确"))
		return
	}

	file, size, name, err := h.svc.SftpDownload(c.Request.Context(), sessionID, filePath)
	if err != nil {
		common.HandleAppError(c, apperrors.Internal("下载文件失败", err))
		return
	}
	defer file.Close()

	c.Writer.WriteHeader(http.StatusOK)
	c.Header("Content-Disposition", "attachment; filename="+name)
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprintf("%d", size))

	if _, err := file.WriteTo(c.Writer); err != nil {
		log.Errorf("[SSH SFTP] 下载文件写入失败: %v", err)
	}
	c.Writer.Flush()
}

// SftpUpload SFTP 上传文件（直连端点，不走 Gateway）
func (h *SFTPHandler) SftpUpload(c *gin.Context) {
	dstPath := c.PostForm("path")
	sessionID := c.PostForm("session_id")
	if dstPath == "" || sessionID == "" {
		common.HandleAppError(c, apperrors.InvalidParam("path/session_id", "参数不能为空"))
		return
	}

	form, err := c.MultipartForm()
	if err != nil {
		common.HandleAppError(c, apperrors.InvalidParam("files", "获取上传文件失败"))
		return
	}
	files := form.File["file"]
	if len(files) == 0 {
		common.HandleAppError(c, apperrors.InvalidParam("files", "没有上传文件"))
		return
	}

	uploaded, err := h.svc.SftpUpload(c.Request.Context(), sessionID, dstPath, files)
	if err != nil {
		common.HandleAppError(c, apperrors.Internal("上传失败", err))
		return
	}

	msg := fmt.Sprintf("%d 个文件上传成功", len(uploaded))
	common.SuccessResponse(c, msg, uploaded)
}
