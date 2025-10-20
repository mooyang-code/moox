package app

import (
	"fmt"
	"net/http"
	"os"

	"github.com/mooyang-code/moox/server/internal/service"
	sshConfig "github.com/mooyang-code/moox/server/internal/service/ssh/app/config"
	sshService "github.com/mooyang-code/moox/server/internal/service/ssh/app/service"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// StartWebSSHService 启动WebSSH服务（在独立的goroutine中运行）
func StartWebSSHService() {
	go func() {
		log.Info("正在启动WebSSH服务...")

		gin.SetMode(gin.ReleaseMode)
		engine := gin.Default()

		// 设置默认路由
		engine.GET("/", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"message": "MooX WebSSH Service",
				"version": "1.0.0",
				"status":  "running",
			})
		})

		// API路由组（移除所有认证中间件）
		api := engine.Group("/api")

		// 系统相关接口
		api.POST("/login", sshService.UserLogin)
		api.POST("/sys/db_conn_check", sshService.DbConnCheck)
		api.GET("/sys/is_init", sshService.GetIsInit)
		api.POST("/sys/init", sshService.SysInit)
		api.GET("/sys/config", sshService.GetRunConf)
		api.POST("/sys/config", sshService.SetRunConf)

		// 创建SSH服务实例并注册路由（移除认证）
		sshSvc := service.NewSSHService()
		sshSvc.RegisterRoutes(engine)

		// 命令收藏
		api.GET("/cmd_note", sshService.CmdNoteFindAll)
		api.GET("/cmd_note/:id", sshService.CmdNoteFindByID)
		api.POST("/cmd_note", sshService.CmdNoteCreate)
		api.PUT("/cmd_note", sshService.CmdNoteUpdateById)
		api.DELETE("/cmd_note/:id", sshService.CmdNoteDeleteById)

		// 策略配置
		api.GET("/policy_conf", sshService.PolicyConfFindAll)
		api.GET("/policy_conf/:id", sshService.PolicyConfFindByID)
		api.POST("/policy_conf", sshService.PolicyConfCreate)
		api.PUT("/policy_conf", sshService.PolicyConfUpdateById)
		api.DELETE("/policy_conf/:id", sshService.PolicyConfDeleteById)

		// 访问控制
		api.GET("/net_filter", sshService.NetFilterFindAll)
		api.GET("/net_filter/:id", sshService.NetFilterFindByID)
		api.POST("/net_filter", sshService.NetFilterCreate)
		api.PUT("/net_filter", sshService.NetFilterUpdateById)
		api.DELETE("/net_filter/:id", sshService.NetFilterDeleteById)

		// 用户管理
		api.GET("/user", sshService.UserFindAll)
		api.GET("/user/:id", sshService.UserFindByID)
		api.POST("/user", sshService.UserCreate)
		api.PUT("/user", sshService.UserUpdateById)
		api.DELETE("/user/:id", sshService.UserDeleteById)
		api.PATCH("/user/check_name_exists", sshService.CheckUserNameExists)
		api.PATCH("/user/pwd", sshService.ModifyPasswd)

		// 审计日志
		api.POST("/login_audit", sshService.LoginAuditSearch)

		// 容器管理
		api.GET("/container/list", sshService.GetContainerList)
		api.GET("/container/:id", sshService.GetContainerDetail)
		api.POST("/container/:id/start", sshService.StartContainer)
		api.POST("/container/:id/stop", sshService.StopContainer)
		api.POST("/container/:id/restart", sshService.RestartContainer)

		// 启动WebSSH服务
		address := fmt.Sprintf("%s:%s", sshConfig.DefaultConfig.Address, sshConfig.DefaultConfig.Port)
		_, certErr := os.Open(sshConfig.DefaultConfig.CertFile)
		_, keyErr := os.Open(sshConfig.DefaultConfig.KeyFile)
		log.Infof("Starting MooX WebSSH Service, address: %s", address)

		// 如果证书和私钥文件存在,就使用https协议,否则使用http协议
		if certErr == nil && keyErr == nil {
			log.Infof("Starting HTTPS server, address: %s", address)
			if err := engine.RunTLS(address, sshConfig.DefaultConfig.CertFile, sshConfig.DefaultConfig.KeyFile); err != nil {
				log.Errorf("Failed to start HTTPS server: %v", err)
			}
		} else {
			log.Infof("Starting HTTP server, address: %s", address)
			if err := engine.Run(address); err != nil {
				log.Errorf("Failed to start HTTP server: %v", err)
			}
		}
	}()
}
