package bootstrap

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/console"
	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	adminsecurity "github.com/mooyang-code/moox/modules/admin/internal/security"
	"github.com/mooyang-code/moox/modules/admin/internal/service/archiveproxy"
	authsvr "github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"github.com/mooyang-code/moox/modules/admin/internal/service/gatewaycontrol"
	secretrpc "github.com/mooyang-code/moox/modules/admin/internal/service/secret/rpc"
	setuprpc "github.com/mooyang-code/moox/modules/admin/internal/service/setup/rpc"
	sshrpc "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/rpc"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
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
	// 1. 注册认证服务。浏览器会话只放在进程内存里，认证服务与控制台共用这一份。
	log.Info("正在初始化认证服务...")
	sessions, err := session.NewStore(cfg.Auth.Session.TTL)
	if err != nil {
		return err
	}
	authImp, err := authsvr.NewService(cfg.Auth, services.DBManager, sessions)
	if err != nil {
		return err
	}
	if err := register(&adminpb.AuthServer_ServiceDesc, authImp); err != nil {
		return err
	}
	master, err := adminsecurity.GetEncryptionKey()
	if err != nil {
		return err
	}
	control, err := gatewaycontrol.NewService(services.DBManager.GetDB(), cfg.AdminNodeID, master)
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
	// 注册 SSH 裸 HTTP 处理器。和 RPC 一样按会话 Cookie 鉴权，另外要求 session_id 是当前用户建的 SSH 会话。
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
	if err := register(&adminpb.ActivityLogServer_ServiceDesc, services.ActivityLog); err != nil {
		return err
	}

	gateway, err := newConsoleGateway(trpc.BackgroundContext(), services.DBManager.GetDB(), cfg.AdminNodeID, master)
	if err != nil {
		return err
	}
	if err := console.InitConsoleServices(s, local, gateway, sessions, services.SpaceMgr); err != nil {
		_ = gateway.Close()
		return err
	}
	services.consoleGateway = gateway
	// 归档的分区副本下载与最新数据预览：裸 HTTP 处理器，按会话 Cookie 鉴权后，
	// 再按拓扑里唯一启用的 archive-worker 部署转发到它的数据端口（本机直连，只出站主机经 console 网关客户端中转）。
	if err := registerArchiveProxy(services, cfg.AdminNodeID, gateway); err != nil {
		services.closeGateways()
		return err
	}

	log.Info("TRPC 服务注册完成")
	return nil
}

func registerArchiveProxy(services *Services, controlHostID string, gateway archiveproxy.RelayDialer) error {
	topology, err := sysdeploy.NewTopologyDAO(services.DBManager.GetDB(), controlHostID)
	if err != nil {
		return err
	}
	proxy, err := archiveproxy.New(archiveproxy.Options{Topology: topology, Relay: gateway, ControlHostID: controlHostID})
	if err != nil {
		return err
	}
	console.RegisterRawHandler("archive", "PartitionDownload", console.RawHandler(proxy.PartitionDownload))
	console.RegisterRawHandler("archive", "RecentRows", console.RawHandler(proxy.RecentRows))
	return nil
}
