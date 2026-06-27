package cloudnode

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/types"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// ========== 核心接口定义 ==========
//
// 设计说明：cloudnode service 层直接以 admingen PB 类型作为入参/出参，
// 不再维护中间 DTO，消除 RPC 层与 service 层之间的翻译映射。
// dao/model 层仍保留内部 model（带 sql struct tag，负责 DB 映射），
// service 实现在内部做一次性 model→PB 转换。
// status 等字段统一用 string 表达，前端直接消费 PB JSON。

// Service 云节点服务总接口，组合各个子服务
type Service interface {
	NodeService
	AccountService
	PackageService
	HeartbeatService
}

// NodeService 节点管理服务接口
type NodeService interface {
	// ========== 节点查询 ==========

	// GetNodeList 获取云节点列表（支持分页）
	GetNodeList(ctx context.Context, req *pb.GetNodeListReq) (*pb.GetNodeListRsp, error)

	// GetCloudNode 根据节点ID获取云节点详情
	GetCloudNode(ctx context.Context, nodeID string) (*pb.CloudNode, error)

	// GetNodesByType 根据节点类型获取云节点列表
	GetNodesByType(ctx context.Context, nodeType string) ([]*pb.CloudNode, error)

	// GetOnlineNodes 获取所有在线云节点列表
	GetOnlineNodes(ctx context.Context) ([]*pb.CloudNode, error)

	// ========== 节点生命周期管理 ==========

	// CreateNode 创建云节点（调用云厂商API）
	CreateNode(ctx context.Context, node *pb.CloudNode, codeConfig *FunctionCodeConfig) (*pb.CloudNode, error)

	// UpdateNode 更新云节点
	UpdateNode(ctx context.Context, node *pb.CloudNode) error

	// DeleteNode 删除云节点（调用云厂商API删除云函数）
	DeleteNode(ctx context.Context, nodeID string) error

	// DeleteNodeFromDB 从数据库删除节点记录
	DeleteNodeFromDB(ctx context.Context, nodeID string) error

	// ========== 节点状态管理 ==========

	// UpdateNodePackageID 更新节点代码包ID
	UpdateNodePackageID(ctx context.Context, nodeID string, packageID string) error

	// ========== 节点部署 ==========

	// DeployNode 部署/更新云节点
	DeployNode(ctx context.Context, nodeID string, codeConfig *FunctionCodeConfig) error

	// ========== 云函数调用 ==========

	// InvokeFunction 调用云函数
	InvokeFunction(ctx context.Context, nodeID string, eventData interface{}) (*pb.InvokeFunctionRsp, error)
}

// AccountService 云账户管理服务接口
type AccountService interface {
	// ========== 云账户管理 ==========

	// CreateAccount 创建云账户
	CreateAccount(ctx context.Context, account *pb.CloudAccount) error

	// UpdateAccount 更新云账户
	UpdateAccount(ctx context.Context, account *pb.CloudAccount) error

	// DeleteAccount 删除云账户
	DeleteAccount(ctx context.Context, accountID string) error

	// ========== 云账户查询 ==========

	// GetAccount 获取云账户详情
	GetAccount(ctx context.Context, accountID string) (*pb.CloudAccount, error)

	// ListAccounts 获取所有云账户列表
	ListAccounts(ctx context.Context) ([]*pb.CloudAccount, error)

	// ListAccountsByProvider 根据云厂商获取账户列表
	ListAccountsByProvider(ctx context.Context, provider string) ([]*pb.CloudAccount, error)

	// GetAccountWithoutMask 获取云账户（不脱敏，供provider使用）
	GetAccountWithoutMask(ctx context.Context, accountID string) (*provider.CloudAccount, error)

	// GetCOSAccountInfo 获取COS账户信息（返回简化结构，供外部模块使用）
	GetCOSAccountInfo(ctx context.Context, accountID string) (*pb.COSAccountInfo, error)

	// ========== 内部服务访问 ==========

	// GetProviderByAccount 获取云厂商客户端（供外部使用）
	GetProviderByAccount(cloudAccountID string) provider.Client
}

// PackageService 代码包管理服务接口
type PackageService interface {
	// GetPackageList 获取代码包列表
	GetPackageList(ctx context.Context, req *pb.GetPackageListReq) (*pb.GetPackageListRsp, error)

	// GetPackageDetail 获取代码包详情
	GetPackageDetail(ctx context.Context, packageID string) (*pb.PackageDetail, error)

	// DeletePackage 删除代码包
	DeletePackage(ctx context.Context, packageID string) error

	// GetPackageDownloadURL 获取代码包下载URL
	GetPackageDownloadURL(ctx context.Context, packageID string) (*pb.PackageDownloadURL, error)
}

// HeartbeatService 心跳服务接口，管理节点心跳和健康监控。
//
// 心跳协议被 collector 端（cli adminclient + cloudfunction handler）使用，
// 涉及 collectmgr taskInstanceStore / dnsproxy 等跨模块内部类型，
// 故 HeartbeatService 仍以内部 types 作为边界，RPC 层做 PB↔types 转换，
// 不牵动未迁移模块，保证 cloudnode 可独立编译验证。
type HeartbeatService interface {
	// ========== 心跳上报 ==========

	// ReportHeartbeat 上报心跳
	ReportHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) (*types.ReportHeartbeatResponse, error)

	// GetNodeStatus 获取节点状态
	GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error)

	// GetOnlineNodeIDs 获取所有在线节点ID列表
	GetOnlineNodeIDs() []string
}

// FunctionCodeConfig 云函数代码配置（内部 provider 协议结构，非对外 DTO）。
// 由 executor 构造、传给 provider，不经过 RPC/PB 序列化。
type FunctionCodeConfig struct {
	Runtime       string
	Handler       string
	Environment   map[string]string
	Version       string
	ZipFileBase64 string
	COSBucket     string
	COSPath       string
	COSRegion     string
}

// COSAccountInfo COS 账户信息（内部结构，供 executor / service_utils 构造
// provider 客户端使用，含明文凭证，不直接对外序列化）。
// 对外通过 AccountService.GetCOSAccountInfo 返回 *pb.COSAccountInfo。
type COSAccountInfo struct {
	Provider  string
	SecretID  string
	SecretKey string
	AppID     string
	COSRegion string
	COSBucket string
}

// RegisterExecutors 注册 cloudnode 模块的所有异步任务执行器
func RegisterExecutors(dbManager *database.Manager, cloudNodeService Service) error {
	log.Info("[CloudNode] 正在注册异步任务执行器...")

	// 注册节点管理 executors（需要类型断言为具体实现以访问私有方法）
	serviceImpl := cloudNodeService.(*ServiceImpl)
	createNodeExecutor := NewCreateNodeExecutor(serviceImpl)
	deleteNodeExecutor := NewDeleteNodeExecutor(serviceImpl)
	deployNodeExecutor := NewDeployNodeExecutor(serviceImpl)
	if err := asynctask.RegisterExecutor(createNodeExecutor); err != nil {
		return err
	}
	if err := asynctask.RegisterExecutor(deleteNodeExecutor); err != nil {
		return err
	}
	if err := asynctask.RegisterExecutor(deployNodeExecutor); err != nil {
		return err
	}

	// 注册代码包管理 executor
	packageDAO := dao.NewFunctionPackageDAO(dbManager.GetDB())
	accountDAO := dao.NewCloudAccountDAO(dbManager.GetDB())
	uploadFileExecutor := NewUploadPackageExecutor(packageDAO, accountDAO)
	if err := asynctask.RegisterExecutor(uploadFileExecutor); err != nil {
		return err
	}

	log.Info("[CloudNode] 异步任务执行器注册完成：CREATE_NODE, DELETE_NODE, DEPLOY_NODE, UPLOAD_FILE_TO_COS")
	return nil
}


