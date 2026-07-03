package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"
	"github.com/mooyang-code/moox/modules/admin/internal/service/monitor"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/space"
	ssh "github.com/mooyang-code/moox/modules/admin/internal/service/ssh"
	sshdao "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"

	"trpc.group/trpc-go/trpc-go/log"
)

// Services 应用服务集合
type Services struct {
	// 数据库管理器（共享基础模块）
	DBManager *database.Manager

	// 各模块服务
	SpaceMgr space.Service

	// SSH 服务
	SSHService ssh.Service

	// 秘钥管理服务
	SecretService secret.Service

	// 系统服务部署信息
	SysDeploy sysdeploy.Service

	// 监控服务
	Monitor monitor.Service
}

// StartBackgroundServices 启动 admin 本地基础服务。
// 云节点、云函数包和采集管理已拆到独立服务，admin 只保留网关转发和基础管理能力。
func StartBackgroundServices(ctx context.Context, cfg *Config) (*Services, error) {
	log.Info("正在启动后台服务...")

	// 1. 初始化数据库
	dbManager, err := initializeDatabase(&cfg.App.Database)
	if err != nil {
		return nil, err
	}

	// 2. 创建核心服务（只创建，不启动）
	services, err := createCoreServices(ctx, dbManager, cfg)
	if err != nil {
		return nil, err
	}

	// 3. 启动所有后台服务
	if err := startBackgroundWorkers(ctx, services); err != nil {
		return nil, err
	}

	log.Info("后台服务启动完成")
	return services, nil
}

// initializeDatabase 初始化数据库
func initializeDatabase(dbCfg *config.DatabaseConfig) (*database.Manager, error) {
	log.Info("[Bootstrap] 正在初始化数据库...")

	dbManager := database.NewManager()
	if err := dbManager.Initialize(dbCfg); err != nil {
		log.Errorf("[Bootstrap] 初始化数据库失败: %v", err)
		return nil, err
	}

	log.Info("[Bootstrap] 数据库初始化成功")
	return dbManager, nil
}

// createCoreServices 创建核心服务
func createCoreServices(ctx context.Context, dbManager *database.Manager, cfg *Config) (*Services, error) {
	log.Info("[Bootstrap] 正在创建核心服务...")

	// 创建 Space 服务
	log.Info("[Bootstrap] 正在创建 Space 服务...")
	spaceService := space.NewService(dbManager)

	// 创建系统服务部署信息服务，并写入缺失的默认部署记录。
	log.Info("[Bootstrap] 正在创建服务部署信息服务...")
	sysDeployService := sysdeploy.NewService(dbManager)
	if err := sysDeployService.SeedDefaults(ctx); err != nil {
		return nil, err
	}
	gateway.SetServiceDetailResolver(sysDeployService.ResolveGatewayServiceDetail)

	db := dbManager.GetDB()

	// 初始化DNSProxy实例（全局单例，供定时器使用）
	log.Info("[Bootstrap] 正在初始化DNSProxy实例...")
	dnsproxy.InitDNSProxyInstance()

	// 创建 SSH 服务
	log.Info("[Bootstrap] 正在创建 SSH 服务...")
	sshHostDAO := sshdao.NewSSHHostDAO(db)
	sshSessionDAO := sshdao.NewSSHSessionDAO(db)
	sshService := ssh.NewService(sshHostDAO, sshSessionDAO)

	// 创建秘钥管理服务
	log.Info("[Bootstrap] 正在创建秘钥管理服务...")
	secretDAO := secretdao.NewSecretDAO(db)
	secretService := secret.NewService(secretDAO)

	// 创建监控服务
	log.Info("[Bootstrap] 正在创建监控服务...")
	monitorService := monitor.NewService(dbManager)
	monitor.InitMonitorInstance(dbManager)

	log.Info("[Bootstrap] 核心服务创建完成")
	services := &Services{
		DBManager:     dbManager,
		SpaceMgr:      spaceService,
		SSHService:    sshService,
		SecretService: secretService,
		SysDeploy:     sysDeployService,
		Monitor:       monitorService,
	}

	return services, nil
}

// startBackgroundWorkers 启动所有后台服务
func startBackgroundWorkers(ctx context.Context, services *Services) error {
	log.Info("[Bootstrap] 正在启动后台服务...")
	_ = ctx
	_ = services
	// CloudNode/Collector 已拆为独立服务，由 admin 网关转发。
	// SSH 直连端点（WebSocket/SFTP 上传下载）通过统一网关 rawhandler（/api/admin/ssh/*）提供。

	log.Info("[Bootstrap] 所有后台服务已启动")
	return nil
}
