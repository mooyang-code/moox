package bootstrap

import (
	"context"
	"net/http"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/activitylog"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	setupservice "github.com/mooyang-code/moox/modules/admin/internal/service/setup"
	ssh "github.com/mooyang-code/moox/modules/admin/internal/service/ssh"
	sshdao "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/gatewayclient"

	"trpc.group/trpc-go/trpc-go/log"
)

// Services 应用服务集合。
type Services struct {
	DBManager *database.Manager

	SSHService ssh.Service
	SecretDAO  *secretdao.SecretDAO
	// Setup is exposed only through the loopback setup listener.
	Setup       *setupservice.Service
	SysDeploy   sysdeploy.Service
	ActivityLog *activitylog.Service

	consoleGateway *gatewayclient.Client
	// consoleHandler 处理浏览器的 /api/admin/*，挂在浏览器入口（9527）上。
	consoleHandler http.Handler
}

func (s *Services) closeGateways() {
	if s.consoleGateway != nil {
		_ = s.consoleGateway.Close()
	}
}

// StartBackgroundServices 启动 admin 本地基础服务。
func StartBackgroundServices(ctx context.Context, cfg *Config) (*Services, error) {
	log.Info("正在启动后台服务...")

	dbManager, err := initializeDatabase(&cfg.App.Database)
	if err != nil {
		return nil, err
	}

	services, err := createCoreServices(ctx, dbManager, cfg)
	if err != nil {
		return nil, err
	}

	if err := startBackgroundWorkers(ctx, services); err != nil {
		return nil, err
	}

	log.Info("后台服务启动完成")
	return services, nil
}

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

func createCoreServices(ctx context.Context, dbManager *database.Manager, cfg *Config) (*Services, error) {
	log.Info("[Bootstrap] 正在创建核心服务...")
	_ = ctx

	encryptionKey := strings.TrimSpace(cfg.EncryptionKey)
	if encryptionKey == "" {
		return nil, secretdao.ErrMissingEncryptionKey
	}

	log.Info("[Bootstrap] 正在创建服务部署信息服务...")
	sysDeployService, err := sysdeploy.NewService(dbManager, cfg.AdminNodeID, encryptionKey)
	if err != nil {
		return nil, err
	}
	db := dbManager.GetDB()

	log.Info("[Bootstrap] 正在创建 SSH 服务...")
	sshService := ssh.NewService(sshdao.NewHostDAO(db, encryptionKey))
	sysDeployService.SetSessionCloser(sshService)

	log.Info("[Bootstrap] 正在创建秘钥管理服务...")
	secretDAO := secretdao.NewSecretDAO(db, encryptionKey)
	setupService := setupservice.NewService(db, encryptionKey)

	log.Info("[Bootstrap] 核心服务创建完成")
	return &Services{
		DBManager:   dbManager,
		SSHService:  sshService,
		SecretDAO:   secretDAO,
		Setup:       setupService,
		SysDeploy:   sysDeployService,
		ActivityLog: activitylog.NewService(db),
	}, nil
}

func startBackgroundWorkers(ctx context.Context, services *Services) error {
	log.Info("[Bootstrap] 正在启动后台服务...")
	_ = ctx
	_ = services
	log.Info("[Bootstrap] 所有后台服务已启动")
	return nil
}
