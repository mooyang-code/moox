package bootstrap

import (
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	authsvr "github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	dnsproxyrpc "github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy/rpc"
	secretrpc "github.com/mooyang-code/moox/modules/admin/internal/service/secret/rpc"
	sshrpc "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/rpc"
	sysdeployrpc "github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy/rpc"
	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

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
	if err := gateway.InitGatewayServices(s); err != nil {
		return err
	}

	// 3. 注册各模块 RPC 服务（本进程有协议 http，经统一网关透传 /api/admin/{service}/{method}）
	// 3.0 Space 管理服务
	adminpb.RegisterSpaceMgrService(s.Service("trpc.moox.admin.SpaceMgr"), services.SpaceMgr)

	// 3.1 云节点/采集管理已拆为独立服务；admin 仅通过 gateway 转发
	// /api/admin/cloudnode/* -> moox-cloudnode
	// /api/admin/collectmgr/* -> moox-collector

	// 3.4 DNS 代理服务
	dnsSvc := dnsproxyrpc.NewService()
	adminpb.RegisterDnsService(s.Service("trpc.moox.infra.Dns"), dnsSvc)

	// 3.5 SSH 管理服务（直连端点走 rawhandler）
	sshSvc := sshrpc.NewService(services.SSHService)
	adminpb.RegisterSshService(s.Service("trpc.moox.ops.Ssh"), sshSvc)
	// 注册 SSH 直连端点裸 HTTP 处理器（WebSocket 终端 + SFTP 流式上传/下载，经统一网关 rawhandler 分派）
	// 鉴权由 session_id 完成（session 创建时已校验登录态），网关 authorize 对这些路径放行（no_auth_methods）
	gateway.RegisterRawHandler("ssh", "WsConnect", gateway.RawHandler(sshrpc.WebSocketConnectHandler(services.SSHService)))
	gateway.RegisterRawHandler("ssh", "SftpDownload", gateway.RawHandler(sshrpc.SftpDownloadHandler(services.SSHService)))
	gateway.RegisterRawHandler("ssh", "SftpUpload", gateway.RawHandler(sshrpc.SftpUploadHandler(services.SSHService)))

	// 3.7 秘钥管理服务
	secretSvc := secretrpc.NewService(services.SecretService)
	adminpb.RegisterSecretMgrService(s.Service("trpc.moox.ops.SecretMgr"), secretSvc)

	// 3.8 服务部署信息
	sysDeploySvc := sysdeployrpc.NewService(services.SysDeploy)
	adminpb.RegisterSysDeployService(s.Service("trpc.moox.ops.SysDeploy"), sysDeploySvc)

	log.Info("TRPC 服务注册完成")
	return nil
}
