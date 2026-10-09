package bootstrap

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/console"
	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	adminsecurity "github.com/mooyang-code/moox/modules/admin/internal/security"
	authsvr "github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	authdao "github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/gatewaycontrol"
	secretrpc "github.com/mooyang-code/moox/modules/admin/internal/service/secret/rpc"
	setuprpc "github.com/mooyang-code/moox/modules/admin/internal/service/setup/rpc"
	sshrpc "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/rpc"
	sysdeployrpc "github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy/rpc"
	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// RegisterTRPCServices 注册所有TRPC服务。
// 控制台复用相同实例及生成的 RPC handler，独立校验 console ACL。
func RegisterTRPCServices(s *server.Server, cfg *Config, services *Services) error {
	local, err := console.NewLocalDispatcher()
	if err != nil {
		return err
	}
	register := func(desc *server.ServiceDesc, implementation any) error {
		filters, err := localServiceFilters(desc.ServiceName)
		if err != nil {
			return err
		}
		if err := local.Register(desc, implementation, filters...); err != nil {
			return err
		}
		rpc := s.Service(desc.ServiceName)
		if rpc == nil {
			return fmt.Errorf("Admin RPC service %q is not configured", desc.ServiceName)
		}
		return rpc.Register(desc, implementation)
	}
	// 1. 注册认证服务
	log.Info("正在初始化认证服务...")
	authImp, err := authsvr.NewService(cfg.Auth, services.DBManager)
	if err != nil {
		return err
	}
	if err := register(&adminpb.AuthServer_ServiceDesc, authImp); err != nil {
		return err
	}
	// Auth initializes the shared durable Badger cache. GatewayControl uses its
	// own nonce namespace, independent of each host gateway's local nonce DB.
	cache, err := authdao.NewCacheDBFromBadger(services.DBManager.GetCache())
	if err != nil {
		return err
	}
	master, err := adminsecurity.GetEncryptionKey()
	if err != nil {
		return err
	}
	control, err := gatewaycontrol.NewService(services.DBManager.GetDB(), cfg.AdminNodeID, master, authdao.NewUserDAO(services.DBManager.GetDB(), cache))
	if err != nil {
		return err
	}
	if err := gatewaycontrol.Register(s.Service("trpc.moox.admin.GatewayControl"), control); err != nil {
		return err
	}

	if err := adminhealth.Register(s.Service("trpc.moox.admin.Health"), time.Now()); err != nil {
		return err
	}

	if err := register(&adminpb.SpaceMgrServer_ServiceDesc, services.SpaceMgr); err != nil {
		return err
	}

	// 3.5 SSH 管理服务（直连端点走 rawhandler）
	sshSvc := sshrpc.NewService(services.SSHService)
	if err := register(&adminpb.SshServer_ServiceDesc, sshSvc); err != nil {
		return err
	}
	console.SetRawSessionOwnerVerifier(services.SSHService.SessionBelongsToUser)
	// 注册 SSH 裸 HTTP 处理器。每次连接/传输必须先通过已签名的管理 RPC
	// 获取与操作类型绑定的一次性 ticket；session_id 本身不承担鉴权。
	console.RegisterRawHandler("ssh", "WsConnect", console.RawHandler(sshrpc.WebSocketConnectHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpDownload", console.RawHandler(sshrpc.SftpDownloadHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpUpload", console.RawHandler(sshrpc.SftpUploadHandler(services.SSHService)))

	// 3.7 秘钥管理服务
	secretSvc := secretrpc.NewService(services.SecretService)
	if err := register(&adminpb.SecretMgrServer_ServiceDesc, secretSvc); err != nil {
		return err
	}

	// 3.8 服务部署信息
	sysDeploySvc := sysdeployrpc.NewService(services.SysDeploy)
	if err := register(&adminpb.SysDeployServer_ServiceDesc, sysDeploySvc); err != nil {
		return err
	}

	// Machine setup uses its dedicated loopback listener; the browser surface
	// uses the same implementation and the catalog's console ACL.
	if err := register(&adminpb.SetupServer_ServiceDesc, setuprpc.NewService(services.Setup)); err != nil {
		return err
	}
	if err := register(&adminpb.CollectorPublishLeaseServer_ServiceDesc, services.CollectorPublishLease); err != nil {
		return err
	}

	gateway, err := newAdminGateway(trpc.BackgroundContext(), services.DBManager.GetDB(), cfg.AdminNodeID, master, "console")
	if err != nil {
		return err
	}
	if err := console.InitConsoleServices(s, local, gateway, services.SpaceMgr); err != nil {
		_ = gateway.Close()
		return err
	}
	services.consoleGateway = gateway
	services.machineGateway, err = newAdminGateway(trpc.BackgroundContext(), services.DBManager.GetDB(), cfg.AdminNodeID, master, "admin")
	if err != nil {
		services.closeGateways()
		return err
	}

	log.Info("TRPC 服务注册完成")
	return nil
}
