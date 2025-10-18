package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	"github.com/mooyang-code/moox/server/internal/config"
	"github.com/mooyang-code/moox/server/internal/container"
	"github.com/mooyang-code/moox/server/internal/gateway"
	"github.com/mooyang-code/moox/server/internal/logger"
	_ "github.com/mooyang-code/moox/server/internal/middleware"
	"github.com/mooyang-code/moox/server/internal/service"
	authsvr "github.com/mooyang-code/moox/server/internal/service/auth"
	authcfg "github.com/mooyang-code/moox/server/internal/service/auth/config"
	collectorcore "github.com/mooyang-code/moox/server/internal/service/collector/core"
	collectorgateway "github.com/mooyang-code/moox/server/internal/service/collector/gateway"
	dnsproxy "github.com/mooyang-code/moox/server/internal/service/dnsproxy"
	"github.com/mooyang-code/moox/server/internal/service/fileserver"
	nodeserviceapi "github.com/mooyang-code/moox/server/internal/service/nodeservice/api"
	sshConfig "github.com/mooyang-code/moox/server/internal/service/ssh/app/config"
	sshService "github.com/mooyang-code/moox/server/internal/service/ssh/app/service"
	pb "github.com/mooyang-code/moox/server/proto/gen"

	"github.com/gin-gonic/gin"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// startWebSSHService 启动WebSSH服务
func startWebSSHService() {
	gin.SetMode(gin.ReleaseMode)
	var engine = gin.Default()

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
	log.Infof("Starting MooX WebSSH Service，address: %+v", address)

	// 如果证书和私钥文件存在,就使用https协议,否则使用http协议
	if certErr == nil && keyErr == nil {
		log.Info("Starting HTTPS server，address: %+v", address)
		err := engine.RunTLS(address, sshConfig.DefaultConfig.CertFile, sshConfig.DefaultConfig.KeyFile)
		if err != nil {
			log.Errorf("Failed to start HTTPS server, error:%+v", err.Error())
		}
	} else {
		log.Infof("Starting HTTP server，address: %+v", address)
		err := engine.Run(address)
		if err != nil {
			log.Errorf("Failed to start HTTP server, error:%+v", err.Error())
		}
	}
}

func main() {
	ctx := context.Background()

	// 1. 加载应用配置
	log.Info("正在加载应用配置...")
	appCfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.Fatalf("加载应用配置失败: %v", err)
	}
	logger.Infof(ctx, "应用配置加载成功，环境: %s，端口: %d", appCfg.Server.Environment, appCfg.Server.Port)

	// 2. 注册到容器
	container.Register(container.ServiceConfig, appCfg)
	container.Register(container.ServiceLogger, logger.NewTrpcLogger())

	// 3. 从配置文件加载认证配置
	authCfg, err := authcfg.LoadConfig()
	if err != nil {
		log.Fatalf("LoadConfig err[%v]", err)
	}

	// 4. 初始化采集器服务（在启动WebSSH服务之前）
	log.Info("正在初始化采集器服务...")
	collectorImp, err := collectorcore.InitCollectorServiceImpl("")
	if err != nil {
		log.Fatalf("初始化采集器服务失败: %v", err)
	}
	container.Register("collectorService", collectorImp)

	// 启动文件下载服务（在独立的goroutine中运行）
	fileserver.StartFileDownloadService()

	// 启动WebSSH服务（在独立的goroutine中运行）
	go func() {
		log.Info("正在启动WebSSH服务...")
		startWebSSHService()
	}()

	// 创建trpc服务器
	s := trpc.NewServer()

	// 初始化认证服务
	authImp, e := authsvr.NewAuthService(authCfg)
	if e != nil {
		log.Fatal(e)
	}
	pb.RegisterAuthAPIService(s, authImp)

	// 注册采集器处理器
	logger.Info(ctx, "正在注册采集器处理器...")
	collectorImp.RegisterCollectorHandlers()
	// 启动异步服务（包括队列消费者、心跳管理器）
	collectorImp.Start(ctx)
	logger.Info(ctx, "采集器服务初始化完成")

	// 注册云节点服务（包含接收心跳的服务）
	log.Info("正在注册云节点服务...")
	heartbeatManager := collectorImp.GetHeartbeatManager()
	if heartbeatManager != nil {
		cloudNodeService := nodeserviceapi.NewCloudNodeService(heartbeatManager)
		pb.RegisterCloudNodeAPIService(s, cloudNodeService)
		log.Info("云节点服务注册完成")
	} else {
		log.Warn("心跳管理器未初始化，跳过云节点服务注册")
	}

	// 初始化网关服务（包括服务处理器和HTTP路由）
	log.Info("正在初始化网关服务...")
	gateway.InitGatewayServices(s)
	log.Info("网关服务初始化完成")

	// 注册采集器网关（必须在网关服务初始化之后）
	log.Info("正在注册采集器网关...")
	gatewayHandler := collectorgateway.NewGatewayHandler(collectorImp)
	collectorgateway.RegisterGatewayHandler(gatewayHandler)
	log.Info("采集器网关注册完成")

	// 注册定时器
	timer.RegisterScheduler("dnsproxySchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsproxy.timer"), dnsproxy.DnsproxySchedule)

	// 启动trpc服务器
	log.Info("启动trpc服务器...")
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}
