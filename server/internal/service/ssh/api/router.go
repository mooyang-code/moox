package api

import (
	"github.com/gin-gonic/gin"
	ssh "github.com/mooyang-code/moox/server/internal/service/ssh"
)

// RegisterSSHRoutes 注册 SSH 相关路由
func RegisterSSHRoutes(router *gin.RouterGroup, svc ssh.Service) {
	hostHandler := NewHostHandler(svc)
	sessionHandler := NewSessionHandler(svc)
	sftpHandler := NewSFTPHandler(svc)
	manageHandler := NewManageHandler(svc)

	// 主机配置
	host := router.Group("/ssh_host")
	{
		host.POST("/list", hostHandler.ListHosts)
		host.POST("/create", hostHandler.CreateHost)
		host.PUT("/update", hostHandler.UpdateHost)
		host.DELETE("/delete", hostHandler.DeleteHost)
		host.GET("/detail", hostHandler.GetHost)
	}

	// SSH 会话
	sshGroup := router.Group("/ssh")
	{
		sshGroup.POST("/create_session", sessionHandler.CreateSession)
		sshGroup.POST("/disconnect", sessionHandler.DisconnectSession)
		sshGroup.POST("/resize", sessionHandler.ResizeWindow)
		sshGroup.POST("/exec", sessionHandler.ExecCommand)
	}

	// SFTP
	sftpGroup := router.Group("/sftp")
	{
		sftpGroup.POST("/list", sftpHandler.SftpList)
		sftpGroup.POST("/mkdir", sftpHandler.SftpMkdir)
		sftpGroup.POST("/delete", sftpHandler.SftpDelete)
	}

	// 会话管理
	manage := router.Group("/ssh_manage")
	{
		manage.POST("/online_sessions", manageHandler.GetOnlineSessions)
		manage.POST("/force_disconnect", manageHandler.ForceDisconnect)
	}
}

// RegisterSSHDirectRoutes 注册需要直接访问的路由（WebSocket、文件上传下载）
// 这些路由不通过 Gateway 代理
func RegisterSSHDirectRoutes(router *gin.Engine, svc ssh.Service) {
	sessionHandler := NewSessionHandler(svc)
	sftpHandler := NewSFTPHandler(svc)

	api := router.Group("/api")
	{
		// WebSocket 终端连接
		api.GET("/ssh/conn", sessionHandler.WebSocketConn)
		// 文件下载（直接流式传输）
		api.GET("/sftp/download", sftpHandler.SftpDownload)
		// 文件上传（multipart form）
		api.POST("/sftp/upload", sftpHandler.SftpUpload)
	}
}
