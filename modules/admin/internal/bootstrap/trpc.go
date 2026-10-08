package bootstrap

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/console"
	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	authsvr "github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	secretrpc "github.com/mooyang-code/moox/modules/admin/internal/service/secret/rpc"
	setuprpc "github.com/mooyang-code/moox/modules/admin/internal/service/setup/rpc"
	sshrpc "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/rpc"
	sysdeployrpc "github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy/rpc"
	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"

	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// RegisterTRPCServices 注册管理后台的全部 tRPC 服务，并把它们登记为控制台的进程内服务：
// 控制台调用管理后台自己的服务时不经过主机网关。
func RegisterTRPCServices(s *server.Server, cfg *Config, services *Services) error {
	local := console.NewLocalServices(configuredServerFilters()...)
	register := func(name string, desc *server.ServiceDesc, impl any, registerTRPC func()) error {
		registerTRPC()
		if err := local.Register(desc, impl); err != nil {
			return fmt.Errorf("登记进程内服务 %s: %w", name, err)
		}
		return nil
	}

	log.Info("正在初始化认证服务...")
	authImp, err := authsvr.NewService(cfg.Auth, services.DBManager)
	if err != nil {
		return err
	}
	if err := register("auth", &adminpb.AuthServer_ServiceDesc, authImp, func() {
		adminpb.RegisterAuthService(s.Service("trpc.moox.infra.Auth"), authImp)
	}); err != nil {
		return err
	}
	if err := adminhealth.Register(s.Service("trpc.moox.admin.Health"), time.Now()); err != nil {
		return err
	}
	if err := register("space", &adminpb.SpaceMgrServer_ServiceDesc, services.SpaceMgr, func() {
		adminpb.RegisterSpaceMgrService(s.Service("trpc.moox.admin.SpaceMgr"), services.SpaceMgr)
	}); err != nil {
		return err
	}

	// SSH 管理；终端和文件传输走控制台的原始处理器，每次连接或传输先经签名的 RPC 获取一次性 ticket。
	sshSvc := sshrpc.NewService(services.SSHService)
	if err := register("ssh", &adminpb.SshServer_ServiceDesc, sshSvc, func() {
		adminpb.RegisterSshService(s.Service("trpc.moox.ops.Ssh"), sshSvc)
	}); err != nil {
		return err
	}
	console.SetRawSessionOwnerVerifier(services.SSHService.SessionBelongsToUser)
	console.RegisterRawHandler("ssh", "WsConnect", console.RawHandler(sshrpc.WebSocketConnectHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpDownload", console.RawHandler(sshrpc.SftpDownloadHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpUpload", console.RawHandler(sshrpc.SftpUploadHandler(services.SSHService)))

	secretSvc := secretrpc.NewService(services.SecretService)
	if err := register("secret", &adminpb.SecretMgrServer_ServiceDesc, secretSvc, func() {
		adminpb.RegisterSecretMgrService(s.Service("trpc.moox.ops.SecretMgr"), secretSvc)
	}); err != nil {
		return err
	}
	sysDeploySvc := sysdeployrpc.NewService(services.SysDeploy, services.Placements, services.GatewayControl)
	if err := register("sysdeploy", &adminpb.SysDeployServer_ServiceDesc, sysDeploySvc, func() {
		adminpb.RegisterSysDeployService(s.Service("trpc.moox.ops.SysDeploy"), sysDeploySvc)
	}); err != nil {
		return err
	}
	// 网关控制只给主机网关，不登记为控制台的进程内服务（组件目录也不放行 console）。
	adminpb.RegisterGatewayControlService(s.Service("trpc.moox.admin.GatewayControl"), services.GatewayControl)
	setupSvc := setuprpc.NewService(services.Setup)
	if err := register("setup", &adminpb.SetupServer_ServiceDesc, setupSvc, func() {
		adminpb.RegisterSetupService(s.Service("trpc.moox.admin.Setup"), setupSvc)
	}); err != nil {
		return err
	}
	if err := register("publishlease", &adminpb.CollectorPublishLeaseServer_ServiceDesc, services.CollectorPublishLease, func() {
		adminpb.RegisterCollectorPublishLeaseService(s.Service("trpc.moox.admin.CollectorPublishLease"), services.CollectorPublishLease)
	}); err != nil {
		return err
	}

	options := console.Options{Catalog: servicecatalog.Default(), Local: local, Authorizer: services.SpaceMgr}
	remote, err := newGatewayClient(cfg.App.GatewayClient, servicecatalog.ConsoleCaller, cfg.App.GatewayClient.ConsoleKeyFile)
	if err != nil {
		return fmt.Errorf("创建控制台的 gatewayclient: %w", err)
	}
	if remote != nil {
		options.Remote = remote
	} else {
		log.Warn("没有配置 gateway_client：控制台只能调用管理后台自己的服务")
	}
	if err := console.Register(s, options); err != nil {
		return err
	}
	log.Info("TRPC 服务注册完成")
	return nil
}

// newGatewayClient 按 gateway_client 配置为一个调用方身份创建内部方式的客户端；没有配置时返回 nil。
func newGatewayClient(cfg config.GatewayClientConfig, caller, keyFile string) (*gatewayclient.Client, error) {
	if strings.TrimSpace(keyFile) == "" {
		return nil, nil
	}
	return gatewayclient.New(gatewayclient.Options{Config: gatewayclient.Config{
		Mode: gatewayclient.ModeLocal, Caller: caller, KeyFile: keyFile, CAFile: cfg.CAFile,
		CacheDir: filepath.Join(cfg.CacheDir, caller), LocalAddress: cfg.LocalAddress,
	}})
}

// configuredServerFilters 返回 trpc_go.yaml 中配置的服务端过滤器，进程内调用经过同一组过滤器（校验、脱敏等）。
func configuredServerFilters() []filter.ServerFilter {
	global := trpc.GlobalConfig()
	if global == nil {
		return nil
	}
	var filters []filter.ServerFilter
	for _, name := range global.Server.Filter {
		if f := filter.GetServer(name); f != nil {
			filters = append(filters, f)
		} else {
			log.Warnf("进程内调用找不到服务端过滤器 %s", name)
		}
	}
	return filters
}
