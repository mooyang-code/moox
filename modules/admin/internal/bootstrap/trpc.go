package bootstrap

import (
	"github.com/mooyang-code/moox/modules/admin/internal/console"
	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	authsvr "github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	secretrpc "github.com/mooyang-code/moox/modules/admin/internal/service/secret/rpc"
	setuprpc "github.com/mooyang-code/moox/modules/admin/internal/service/setup/rpc"
	sshrpc "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/rpc"
	sysdeployrpc "github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy/rpc"
	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"time"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// RegisterTRPCServices 注册所有TRPC服务。
// 本进程业务服务均开有协议 http（trpc_go.yaml protocol:http），由统一网关 forwardHTTP 透传，
// 不再注册 dispatcher / ServiceHandler。
func RegisterTRPCServices(s *server.Server, cfg *Config, services *Services) error {
	// 1. 注册认证服务
	log.Info("正在初始化认证服务...")
	authImp, err := authsvr.NewService(cfg.Auth, services.DBManager)
	if err != nil {
		return err
	}
	adminpb.RegisterAuthService(s.Service("trpc.moox.infra.Auth"), authImp)

	// 2. 初始化网关服务
	log.Info("正在初始化网关服务...")
	if err := console.InitGatewayServices(s, services.SysDeploy, cfg.AdminNodeID, services.SpaceMgr); err != nil {
		return err
	}
	if err := adminhealth.Register(s.Service("trpc.moox.admin.Health"), time.Now()); err != nil {
		return err
	}

	// 3. 注册各模块 RPC 服务（本进程有协议 http，经统一网关透传 /api/admin/{service}/{method}）
	// 3.0 Space 管理服务
	adminpb.RegisterSpaceMgrService(s.Service("trpc.moox.admin.SpaceMgr"), services.SpaceMgr)

	// 3.1 云节点/采集管理已拆为独立服务；admin 仅通过 gateway 转发
	// /api/admin/cloudnode/* -> moox-cloudnode
	// /api/admin/collectmgr/* -> moox-collector

	// 3.5 SSH 管理服务（直连端点走 rawhandler）
	sshSvc := sshrpc.NewService(services.SSHService)
	adminpb.RegisterSshService(s.Service("trpc.moox.ops.Ssh"), sshSvc)
	console.SetRawSessionOwnerVerifier(services.SSHService.SessionBelongsToUser)
	// 注册 SSH 裸 HTTP 处理器。每次连接/传输必须先通过已签名的管理 RPC
	// 获取与操作类型绑定的一次性 ticket；session_id 本身不承担鉴权。
	console.RegisterRawHandler("ssh", "WsConnect", console.RawHandler(sshrpc.WebSocketConnectHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpDownload", console.RawHandler(sshrpc.SftpDownloadHandler(services.SSHService)))
	console.RegisterRawHandler("ssh", "SftpUpload", console.RawHandler(sshrpc.SftpUploadHandler(services.SSHService)))

	// 3.7 秘钥管理服务
	secretSvc := secretrpc.NewService(services.SecretService)
	adminpb.RegisterSecretMgrService(s.Service("trpc.moox.ops.SecretMgr"), secretSvc)

	// 3.8 服务部署信息
	sysDeploySvc := sysdeployrpc.NewService(services.SysDeploy, services.Placements, services.GatewayControl)
	adminpb.RegisterSysDeployService(s.Service("trpc.moox.ops.SysDeploy"), sysDeploySvc)

	// 3.9 网关控制：只监听本机，主机网关经 control 的主机网关访问
	adminpb.RegisterGatewayControlService(s.Service("trpc.moox.admin.GatewayControl"), services.GatewayControl)

	// Setup is intentionally registered only on its dedicated loopback listener.
	adminpb.RegisterSetupService(s.Service("trpc.moox.admin.Setup"), setuprpc.NewService(services.Setup))
	adminpb.RegisterCollectorPublishLeaseService(s.Service("trpc.moox.admin.CollectorPublishLease"), services.CollectorPublishLease)

	log.Info("TRPC 服务注册完成")
	return nil
}
