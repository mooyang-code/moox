package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	"github.com/mooyang-code/moox/server/internal/gateway"
	_ "github.com/mooyang-code/moox/server/internal/middleware"
	"github.com/mooyang-code/moox/server/internal/service"
	apisvr "github.com/mooyang-code/moox/server/internal/service/apirouter"
	authsvr "github.com/mooyang-code/moox/server/internal/service/auth"
	authcfg "github.com/mooyang-code/moox/server/internal/service/auth/config"
	collectorsvr "github.com/mooyang-code/moox/server/internal/service/collector"
	nodeservice "github.com/mooyang-code/moox/server/internal/service/nodeservice"
	sshConfig "github.com/mooyang-code/moox/server/internal/service/ssh/app/config"
	sshService "github.com/mooyang-code/moox/server/internal/service/ssh/app/service"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/gin-gonic/gin"
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
	// 从配置文件加载所有配置
	authCfg, err := authcfg.LoadConfig()
	if err != nil {
		log.Fatalf("LoadConfig err[%v]", err)
	}

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

	// 先注册API路由，这样采集器处理器才能找到路由
	log.Info("正在注册API路由...")
	apisvr.RegisterStandardHTTPHandlers(s)

	// 初始化采集器服务
	log.Info("正在初始化采集器服务...")
	collectorImp, err := collectorsvr.InitCollectorServiceImpl("")
	if err != nil {
		log.Fatalf("初始化采集器服务失败: %v", err)
	}
	collectorImp.RegisterCollectorHandlers()
	// 注册HTTP路由（文件上传等）
	collectorImp.RegisterHTTPRoutes(apisvr.GetHTTPMux())
	// 启动异步服务（包括队列消费者）
	ctx := context.Background()
	collectorImp.Start(ctx)
	log.Info("采集器服务初始化完成")

	// 注册云节点服务
	log.Info("正在注册云节点服务...")
	heartbeatManager := collectorImp.GetHeartbeatManager()
	if heartbeatManager != nil {
		cloudNodeService := nodeservice.NewCloudNodeService(heartbeatManager)
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
	collectorImp.RegisterCollectorGateway("http://localhost:20101") // API服务在20101端口
	log.Info("采集器网关注册完成")

	// 注册定时器
	timer.RegisterScheduler("dnsproxySchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsproxy.timer"), apisvr.DnsproxySchedule)

	// 启动trpc服务器
	log.Info("启动trpc服务器...")
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}
