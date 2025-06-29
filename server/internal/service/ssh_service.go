package service

import (
	"github.com/gin-gonic/gin"
	sshService "github.com/mooyang-code/moox/server/internal/service/ssh/app/service"
)

// SSHService WebSSH服务包装器，用于集成到moox服务中
type SSHService struct {
	// 可以添加一些配置或状态管理
}

// NewSSHService 创建SSH服务实例
func NewSSHService() *SSHService {
	return &SSHService{}
}

// RegisterRoutes 注册SSH相关路由到现有的gin引擎
func (s *SSHService) RegisterRoutes(router *gin.Engine) {
	// 注册SSH连接配置管理路由
	s.registerSSHConfigRoutes(router)

	// 注册SSH连接管理路由
	s.registerSSHConnectionRoutes(router)

	// 注册SFTP文件管理路由
	s.registerSFTPRoutes(router)

	// 注册容器SSH路由
	s.registerContainerSSHRoutes(router)

	// 注册SSH状态管理路由
	s.registerSSHStatusRoutes(router)
}



// registerSSHConfigRoutes 注册SSH配置管理路由
func (s *SSHService) registerSSHConfigRoutes(router *gin.Engine) {
	// API路由组（移除认证中间件）
	api := router.Group("/api")

	// SSH连接配置
	api.GET("/conn_conf", sshService.ConfFindAll)
	api.GET("/conn_conf/:id", sshService.ConfFindByID)
	api.POST("/conn_conf", sshService.ConfCreate)
	api.PUT("/conn_conf", sshService.ConfUpdateById)
	api.DELETE("/conn_conf/:id", sshService.ConfDeleteById)
}

// registerSSHConnectionRoutes 注册SSH连接管理路由
func (s *SSHService) registerSSHConnectionRoutes(router *gin.Engine) {
	api := router.Group("/api")

	// SSH连接管理
	api.GET("/ssh/conn", sshService.NewSshConn)
	api.PATCH("/ssh/conn", sshService.ResizeWindow)
	api.POST("/ssh/exec", sshService.ExecCommand)
	api.POST("/ssh/disconnect", sshService.Disconnect)
	api.POST("/ssh/create_session", sshService.CreateSessionId)
}

// registerSFTPRoutes 注册SFTP文件管理路由
func (s *SSHService) registerSFTPRoutes(router *gin.Engine) {
	api := router.Group("/api")

	// SFTP文件管理
	api.POST("/sftp/create_dir", sshService.SftpCreateDir)
	api.POST("/sftp/list", sshService.SftpList)
	api.GET("/sftp/download", sshService.SftpDownLoad)
	api.PUT("/sftp/upload", sshService.SftpUpload)
	api.DELETE("/sftp/delete", sshService.SftpDelete)
}

// registerContainerSSHRoutes 注册容器SSH路由
func (s *SSHService) registerContainerSSHRoutes(router *gin.Engine) {
	api := router.Group("/api")

	// 容器SSH管理
	api.POST("/container/ssh/create_session", sshService.CreateContainerSSHSession)
	api.GET("/container/ssh/conn", sshService.ContainerSSHConn)
}

// registerSSHStatusRoutes 注册SSH状态管理路由
func (s *SSHService) registerSSHStatusRoutes(router *gin.Engine) {
	api := router.Group("/api")

	// SSH连接状态管理
	api.GET("/conn_manage/online_client", sshService.GetOnlineClient)
	api.PUT("/conn_manage/refresh_conn_time", sshService.RefreshConnTime)
}

// InitSSHService 初始化SSH服务
func (s *SSHService) InitSSHService() error {
	// 初始化SSH服务相关组件
	// 这里可以调用原webssh的初始化逻辑
	return nil
}
