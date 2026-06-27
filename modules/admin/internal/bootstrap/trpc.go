package bootstrap

import (
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	asynctaskrpc "github.com/mooyang-code/moox/modules/admin/internal/service/asynctask/rpc"
	authsvr "github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	cloudnoderpc "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/rpc"
	collectmgrrpc "github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/rpc"
	adminsvc "github.com/mooyang-code/moox/modules/admin/internal/service/admin"
	dnsproxyrpc "github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy/rpc"
	fileserver "github.com/mooyang-code/moox/modules/admin/internal/service/fileserver"
	monitorrpc "github.com/mooyang-code/moox/modules/admin/internal/service/monitor/rpc"
	sshrpc "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/rpc"
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

	// 注册控制面编排服务（工作空间与元数据），仅暴露编排语义，不暴露 storage adapter。
	adminImp := adminsvc.NewService()
	adminpb.RegisterAdminService(s, adminImp)

	// 2. 初始化网关服务
	log.Info("正在初始化网关服务...")
	gateway.InitGatewayServices(s)

	// 3. 注册各模块 RPC 服务（本进程有协议 http，经统一网关透传 /api/admin/{service}/{method}）
	// 3.0 Space 管理服务
	adminpb.RegisterSpaceMgrService(s.Service("trpc.moox.admin.SpaceMgr"), services.SpaceMgr)

	// 3.1 异步任务服务
	asyncTaskSvc := asynctaskrpc.NewService(services.AsyncTask)
	adminpb.RegisterAsyncTaskService(s.Service("trpc.moox.infra.AsyncTask"), asyncTaskSvc)

	// 3.2 云节点服务
	cloudNodeSvc := cloudnoderpc.NewService(services.CloudNodeMgr, services.AsyncTask)
	adminpb.RegisterCloudNodeMgrService(s.Service("trpc.moox.collect.CloudNodeMgr"), cloudNodeSvc)

	// 3.3 采集管理服务
	collectmgrSvc := collectmgrrpc.NewService(
		services.TaskRuleService,
		services.TaskInstanceService,
		services.DataTypeConfigService,
		services.TaskPlannerService,
	)
	adminpb.RegisterCollectMgrService(s.Service("trpc.moox.collect.CollectMgr"), collectmgrSvc)

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

	// 3.6 监控服务
	monitorSvc := monitorrpc.NewService(services.Monitor)
	adminpb.RegisterMonitorService(s.Service("trpc.moox.ops.Monitor"), monitorSvc)

	// 3.7 注册文件下载裸 HTTP 处理器（云函数包下载，经统一网关 rawhandler 分派）
	// 路由：/api/admin/fileserver/download?file={path}&token={file_download_jwt}
	// 鉴权由 fileserver 内部校验 file_download token，网关 authorize 对该路径放行（no_auth_methods）
	gateway.RegisterRawHandler("fileserver", "download", gateway.RawHandler(fileserver.DownloadHandler(fileserver.DefaultConfig)))

	log.Info("TRPC 服务注册完成")
	return nil
}
