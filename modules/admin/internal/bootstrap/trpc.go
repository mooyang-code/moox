package bootstrap

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/console"
	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	"github.com/mooyang-code/moox/modules/admin/internal/service/archiveproxy"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/mooyang-code/moox/modules/admin/internal/service/gatewaycontrol"
	secretrpc "github.com/mooyang-code/moox/modules/admin/internal/service/secret/rpc"
	setuprpc "github.com/mooyang-code/moox/modules/admin/internal/service/setup/rpc"
	sshrpc "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/rpc"
	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// RegisterTRPCServices 注册所有 TRPC 服务与控制台本地分派器：
// - 纯控制台服务（Auth、Ssh）仅注册到进程内 LocalDispatcher，不开多余回环 TCP 端口；
// - 纯机器服务（Setup、GatewayControl）仅注册到回环 tRPC 端口，不暴露给控制台；
// - 同时面向控制台与机器的服务（SecretMgr、SysDeploy、ActivityLog）双边注册，复用同一实例。
func RegisterTRPCServices(s *server.Server, cfg *Config, services *Services) error {
	local, err := console.NewLocalDispatcher()
	if err != nil {
		return err
	}
	registerLocalOnly := func(desc *server.ServiceDesc, implementation any) error {
		filters, err := localServiceFilters(desc.ServiceName)
		if err != nil {
			return err
		}
		return local.Register(desc, implementation, filters...)
	}
	registerRPCOnly := func(desc *server.ServiceDesc, implementation any) error {
		rpc := s.Service(desc.ServiceName)
		if rpc == nil {
			return fmt.Errorf("Admin RPC service %q is not configured", desc.ServiceName)
		}
		return rpc.Register(desc, implementation)
	}
	registerRPCAndLocal := func(desc *server.ServiceDesc, implementation any) error {
		if err := registerLocalOnly(desc, implementation); err != nil {
			return err
		}
		return registerRPCOnly(desc, implementation)
	}

	// 1. 纯控制台服务：认证服务（浏览器会话只放在进程内存里，认证服务与控制台共用这一份）。
	log.Info("正在初始化认证服务...")
	sessions, err := auth.NewSessionStore(cfg.Auth.Session.TTL)
	if err != nil {
		return err
	}
	authImp, err := auth.NewService(cfg.Auth, services.DBManager, sessions, services.ActivityLog.RecordLogin)
	if err != nil {
		return err
	}
	if err := registerLocalOnly(&adminpb.AuthServer_ServiceDesc, authImp); err != nil {
		return err
	}

	// 2. 网关控制面（独立回环端口 + 专用 SerializationTypeNoop / 验签隔离）与健康端口。
	control, err := gatewaycontrol.NewService(services.DBManager.GetDB(), cfg.AdminNodeID)
	if err != nil {
		return err
	}
	if err := gatewaycontrol.Register(s.Service("trpc.moox.admin.GatewayControl"), control); err != nil {
		return err
	}
	if err := adminhealth.Register(s.Service("trpc.moox.admin.Health"), time.Now()); err != nil {
		return err
	}

	// 3. 纯控制台服务：SSH 管理服务（RPC 走进程内 LocalDispatcher，流式端点走 rawhandler 并在 SSH 处理器内校验会话归属）。
	sshSvc := sshrpc.NewService(services.SSHService)
	if err := registerLocalOnly(&adminpb.SshServer_ServiceDesc, sshSvc); err != nil {
		return err
	}
	console.RegisterRawHandler("ssh", "WsConnect", console.RawHandler(sshrpc.WebSocketConnectHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpDownload", console.RawHandler(sshrpc.SftpDownloadHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpUpload", console.RawHandler(sshrpc.SftpUploadHandler(services.SSHService)))

	// 4. 控制台 + 机器回环服务：SecretMgr、SysDeploy、ActivityLog。
	secretSvc := secretrpc.NewService(services.SecretDAO)
	if err := registerRPCAndLocal(&adminpb.SecretMgrServer_ServiceDesc, secretSvc); err != nil {
		return err
	}
	if err := registerRPCAndLocal(&adminpb.SysDeployServer_ServiceDesc, services.SysDeploy); err != nil {
		return err
	}
	if err := registerRPCAndLocal(&adminpb.ActivityLogServer_ServiceDesc, services.ActivityLog); err != nil {
		return err
	}

	// 5. 纯机器回环服务：Setup（仅给 moox-cli setup init 调用，不注册到控制台 LocalDispatcher）。
	if err := registerRPCOnly(&adminpb.SetupServer_ServiceDesc, setuprpc.NewService(services.Setup)); err != nil {
		return err
	}

	// 6. 控制台转发客户端与归档只出站中转（复用长生命周期 TopologyDAO）。
	topology := services.SysDeploy.TopologyDAO()
	gateway, err := newConsoleGateway(services.DBManager.GetDB(), topology, cfg.AdminNodeID, hostKeyFile())
	if err != nil {
		return err
	}
	handler, err := console.NewHandler(local, gateway, sessions)
	if err != nil {
		_ = gateway.Close()
		return err
	}
	services.consoleGateway, services.consoleHandler = gateway, handler
	if err := registerArchiveProxy(topology, cfg.AdminNodeID, gateway); err != nil {
		services.closeGateways()
		return err
	}

	log.Info("TRPC 服务注册完成")
	return nil
}

func registerArchiveProxy(topology archiveproxy.Topology, controlHostID string, gateway archiveproxy.RelayDialer) error {
	proxy, err := archiveproxy.New(archiveproxy.Options{Topology: topology, Relay: gateway, ControlHostID: controlHostID})
	if err != nil {
		return err
	}
	console.RegisterRawHandler("archive", "PartitionDownload", console.RawHandler(proxy.PartitionDownload))
	console.RegisterRawHandler("archive", "RecentRows", console.RawHandler(proxy.RecentRows))
	return nil
}
