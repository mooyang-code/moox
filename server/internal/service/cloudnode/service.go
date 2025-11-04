package cloudnode

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
	"github.com/mooyang-code/moox/server/internal/service/database"

	"trpc.group/trpc-go/trpc-go/log"
)

// ========== 核心接口定义 ==========

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
	GetNodeList(ctx context.Context, req *NodeListRequest) (*NodeListResponse, error)

	// GetCloudNode 根据节点ID获取云节点详情
	GetCloudNode(ctx context.Context, nodeID string) (*CloudNodeDTO, error)

	// GetNodesByType 根据节点类型获取云节点列表
	GetNodesByType(ctx context.Context, nodeType string) ([]*CloudNodeDTO, error)

	// GetOnlineNodes 获取所有在线云节点列表
	GetOnlineNodes(ctx context.Context) ([]*CloudNodeDTO, error)

	// ========== 节点生命周期管理 ==========

	// CreateNode 创建云节点（调用云厂商API）
	CreateNode(ctx context.Context, node *CloudNodeDTO, codeConfig *FunctionCodeConfig) (*CloudNodeDTO, error)

	// UpdateNode 更新云节点
	UpdateNode(ctx context.Context, node *CloudNodeDTO) error

	// DeleteNode 删除云节点（调用云厂商API删除云函数）
	DeleteNode(ctx context.Context, nodeID string) error

	// DeleteNodeFromDB 从数据库删除节点记录
	DeleteNodeFromDB(ctx context.Context, nodeID string) error

	// ========== 节点数据库操作 ==========
	// 注意：SaveNodeToDB 已移至私有方法，仅供内部使用；UpdateNodeStatus 已移除，状态管理由HeartbeatNode表负责

	// ========== 节点状态管理 ==========

	// UpdateNodePackageID 更新节点代码包ID
	UpdateNodePackageID(ctx context.Context, nodeID string, packageID string) error

	// ========== 节点部署 ==========

	// DeployNode 部署/更新云节点
	DeployNode(ctx context.Context, nodeID string, codeConfig *FunctionCodeConfig) error

	// ========== 云函数调用 ==========

	// InvokeFunction 调用云函数
	InvokeFunction(ctx context.Context, nodeID string, eventData interface{}) (*InvokeFunctionResponse, error)
}

// AccountService 云账户管理服务接口
type AccountService interface {
	// ========== 云账户管理 ==========

	// CreateAccount 创建云账户
	CreateAccount(ctx context.Context, account *CloudAccountDTO) error

	// UpdateAccount 更新云账户
	UpdateAccount(ctx context.Context, account *CloudAccountDTO) error

	// DeleteAccount 删除云账户
	DeleteAccount(ctx context.Context, accountID string) error

	// ========== 云账户查询 ==========

	// GetAccount 获取云账户详情
	GetAccount(ctx context.Context, accountID string) (*CloudAccountDTO, error)

	// ListAccounts 获取所有云账户列表
	ListAccounts(ctx context.Context) ([]*CloudAccountDTO, error)

	// ListAccountsByProvider 根据云厂商获取账户列表
	ListAccountsByProvider(ctx context.Context, provider string) ([]*CloudAccountDTO, error)

	// GetAccountWithoutMask 获取云账户（不脱敏，供provider使用）
	GetAccountWithoutMask(ctx context.Context, accountID string) (*provider.CloudAccount, error)

	// GetCOSAccountInfo 获取COS账户信息（返回简化结构，供外部模块使用）
	GetCOSAccountInfo(ctx context.Context, accountID string) (*COSAccountInfo, error)

	// ========== 内部服务访问 ==========

	// GetProviderByAccount 获取云厂商客户端（供外部使用）
	GetProviderByAccount(cloudAccountID string) provider.Client
}

// PackageService 代码包管理服务接口
type PackageService interface {
	// GetPackageList 获取代码包列表
	GetPackageList(ctx context.Context, req *PackageListRequest) (*PackageListResponse, error)

	// GetPackageDetail 获取代码包详情
	GetPackageDetail(ctx context.Context, packageID string) (*PackageDetail, error)

	// DeletePackage 删除代码包
	DeletePackage(ctx context.Context, packageID string) error

	// UploadPackage 上传代码包
	UploadPackage(ctx context.Context, req *UploadPackageRequest) (*UploadPackageResponse, error)

	// GetPackageDownloadURL 获取代码包下载URL
	GetPackageDownloadURL(ctx context.Context, packageID string) (*PackageDownloadURL, error)
}

// HeartbeatService 心跳服务接口，管理节点心跳和健康监控
type HeartbeatService interface {
	// ========== 心跳上报 ==========

	// ReportHeartbeat 上报心跳
	ReportHeartbeat(ctx context.Context, req *types.ReportHeartbeatRequest) error

	// BatchReportHeartbeat 批量上报心跳
	BatchReportHeartbeat(ctx context.Context, req *types.BatchReportHeartbeatRequest) error

	// ========== 节点管理 ==========

	// RegisterHeartbeatNode 注册心跳节点
	RegisterHeartbeatNode(ctx context.Context, req *types.RegisterNodeRequest) (*types.HeartbeatNode, error)

	// UnregisterHeartbeatNode 注销心跳节点
	UnregisterHeartbeatNode(ctx context.Context, nodeID, nodeType string) error

	// GetHeartbeatNode 获取节点心跳信息
	GetHeartbeatNode(ctx context.Context, nodeID, nodeType string) (*types.HeartbeatNode, error)

	// GetNodeStatus 获取节点状态
	GetNodeStatus(ctx context.Context, nodeID string) (*types.NodeStatus, error)

	// ListHeartbeatNodes 列出心跳节点
	ListHeartbeatNodes(ctx context.Context, filter *types.NodeFilter) ([]*types.HeartbeatNode, int64, error)

	// UpdateHeartbeatNodeConfig 更新心跳节点配置
	UpdateHeartbeatNodeConfig(ctx context.Context, req *types.UpdateNodeConfigRequest) error

	// ========== 探测管理 ==========

	// ProbeHeartbeatNode 手动探测心跳节点
	ProbeHeartbeatNode(ctx context.Context, nodeID, nodeType, action string) (*types.ProbeResult, error)
}

// CloudNodeDTO 云节点数据传输对象（统一用于输入输出）
type CloudNodeDTO struct {
	ID                  int               `json:"id"`
	NodeID              string            `json:"node_id"`
	CloudAccountID      string            `json:"cloud_account_id"`
	PackageID           string            `json:"package_id"`
	PackageVersion      string            `json:"package_version,omitempty"`
	Namespace           string            `json:"namespace"`
	NodeType            string            `json:"node_type"`
	Region              string            `json:"region"`
	IPAddress           string            `json:"ip_address"`
	SupportedCollectors string            `json:"supported_collectors"`
	Metadata            string            `json:"metadata"`
	TimeoutThreshold    int               `json:"timeout_threshold"`  // 超时阈值（秒），0表示使用全局默认值
	HeartbeatInterval   int               `json:"heartbeat_interval"` // 心跳间隔（秒），0表示使用全局默认值
	ProbeEnabled        bool              `json:"probe_enabled"`      // 是否启用探测
	ProbeURL            string            `json:"probe_url"`          // 探测URL
	Status              *types.NodeStatus `json:"status,omitempty"`   // 节点状态
	Invalid             int               `json:"invalid"`
	CreateTime          time.Time         `json:"create_time"`
	ModifyTime          time.Time         `json:"modify_time"`
}

// CloudAccountDTO 云账户数据传输对象（统一用于输入输出）
type CloudAccountDTO struct {
	ID          int       `json:"id"`
	AccountID   string    `json:"account_id"`
	AccountName string    `json:"account_name"`
	Provider    string    `json:"provider"`
	SecretID    string    `json:"secret_id"`  // 已脱敏
	SecretKey   string    `json:"secret_key"` // 已脱敏
	AppID       string    `json:"app_id"`
	COSRegion   string    `json:"cos_region"`
	COSBucket   string    `json:"cos_bucket"`
	ExtraConfig string    `json:"extra_config"`
	Invalid     int       `json:"invalid"`
	CreateTime  time.Time `json:"create_time"`
	ModifyTime  time.Time `json:"modify_time"`
}

// UploadPackageRequest 上传代码包请求
type UploadPackageRequest struct {
	PackageName    string `json:"package_name" binding:"required"`
	Version        string `json:"version" binding:"required"`
	Description    string `json:"description"`
	Runtime        string `json:"runtime" binding:"required"`
	PackageType    string `json:"package_type" binding:"required"`
	FileContent    string `json:"file_content" binding:"required"` // base64编码的zip文件内容
	CloudAccountID string `json:"cloud_account_id"`                // 云账户ID，可选，用于COS配置
}

// PackageListRequest 代码包列表请求
type PackageListRequest struct {
	Page        int    `form:"page,default=1"`
	PageSize    int    `form:"page_size,default=20"`
	PackageName string `form:"package_name"`
	Runtime     string `form:"runtime"`
	PackageType string `form:"package_type"`
	Status      *int   `form:"status"`
}

// PackageListResponse 代码包列表响应
type PackageListResponse struct {
	Total int64              `json:"total"`
	Items []*PackageListItem `json:"items"`
}

// PackageListItem 代码包列表项
type PackageListItem struct {
	PackageID        string     `json:"package_id"`
	PackageName      string     `json:"package_name"`
	Version          string     `json:"version"`
	Description      string     `json:"description"`
	Runtime          string     `json:"runtime"`
	PackageType      string     `json:"package_type"`
	PackageTypeLabel string     `json:"package_type_label"`
	FileSize         int64      `json:"file_size"`
	FileMD5          string     `json:"file_md5"`
	CloudAccountID   string     `json:"cloud_account_id"`
	COSRegion        string     `json:"cos_region"`
	Status           int        `json:"status"`
	StatusLabel      string     `json:"status_label"`
	LastDeployTime   *time.Time `json:"last_deploy_time"`
	CreateTime       time.Time  `json:"created_time"`
}

// PackageDetail 代码包详情视图对象
type PackageDetail struct {
	ID               int64  `json:"id"` // 数据库ID（仅用于内部）
	PackageID        string `json:"package_id"`
	PackageName      string `json:"package_name"`
	Version          string `json:"version"`
	Description      string `json:"description"`
	Runtime          string `json:"runtime"`
	PackageType      string `json:"package_type"`
	PackageTypeLabel string `json:"package_type_label"`

	// 文件信息
	OriginalFilename string `json:"original_filename"`
	FileSize         int64  `json:"file_size"`
	FileMD5          string `json:"file_md5"`

	// COS存储信息
	CloudAccountID string `json:"cloud_account_id"`
	COSRegion      string `json:"cos_region"`
	COSBucket      string `json:"cos_bucket"`
	COSPath        string `json:"cos_path"`
	COSURL         string `json:"cos_url"`

	// 状态管理
	Status         int    `json:"status"`
	StatusLabel    string `json:"status_label"`
	UploadProgress int    `json:"upload_progress"`
	ErrorMessage   string `json:"error_message"`

	// 使用统计
	LastDeployTime *time.Time `json:"last_deploy_time"`

	// 审计字段
	Invalid    int       `json:"invalid"`
	CreateTime time.Time `json:"created_time"`
	ModifyTime time.Time `json:"updated_time"`
}

// PackageDownloadURL 代码包下载URL
type PackageDownloadURL struct {
	PackageID   string `json:"package_id"`
	PackageName string `json:"package_name"`
	Version     string `json:"version"`
	Filename    string `json:"filename"`
	DownloadURL string `json:"download_url"`
	FileSize    int64  `json:"file_size"`
	FileMD5     string `json:"file_md5"`
}

// UploadPackageResponse 上传代码包响应（异步模式）
type UploadPackageResponse struct {
	JobID       string `json:"job_id"`
	PackageName string `json:"package_name"`
	Version     string `json:"version"`
	Status      int    `json:"status"`
	Message     string `json:"message"`
}

// NodeListRequest 节点列表查询请求
type NodeListRequest struct {
	Page     int    `form:"page,default=1"`
	PageSize int    `form:"page_size,default=20"`
	NodeType string `form:"node_type"`
	Status   string `form:"status"`
	Keyword  string `form:"keyword"`
}

// NodeListResponse 节点列表响应
type NodeListResponse struct {
	Total int64           `json:"total"`
	Items []*CloudNodeDTO `json:"items"`
	Page  int             `json:"page"`
	Size  int             `json:"size"`
}

// RegisterExecutors 注册 cloudnode 模块的所有异步任务执行器
func RegisterExecutors(dbManager *database.Manager, cloudNodeService Service, heartbeatService HeartbeatService) error {
	log.Info("[CloudNode] 正在注册异步任务执行器...")

	// 注册节点管理 executors
	// 需要类型断言为具体实现以访问私有方法
	serviceImpl := cloudNodeService.(*ServiceImpl)
	createNodeExecutor := NewCreateNodeExecutor(serviceImpl, heartbeatService)
	deleteNodeExecutor := NewDeleteNodeExecutor(serviceImpl, heartbeatService)
	deployNodeExecutor := NewDeployNodeExecutor(serviceImpl, heartbeatService)
	err := asynctask.RegisterExecutor(createNodeExecutor)
	if err != nil {
		return err
	}
	err = asynctask.RegisterExecutor(deleteNodeExecutor)
	if err != nil {
		return err
	}
	err = asynctask.RegisterExecutor(deployNodeExecutor)
	if err != nil {
		return err
	}

	// 注册代码包管理 executor
	packageDAO := dao.NewFunctionPackageDAO(dbManager.GetDB())
	accountDAO := dao.NewCloudAccountDAO(dbManager.GetDB())
	uploadFileExecutor := NewUploadPackageExecutor(packageDAO, accountDAO)
	err = asynctask.RegisterExecutor(uploadFileExecutor)
	if err != nil {
		return err
	}

	log.Info("[CloudNode] 异步任务执行器注册完成：CREATE_NODE, DELETE_NODE, DEPLOY_NODE, UPLOAD_FILE_TO_COS")
	return nil
}

// ========== 探测器相关实现 ==========

// 全局变量：探测器注册表
var (
	probersMu  sync.RWMutex
	probersMap = make(map[string]Prober) // key为prober.Name()
)

// Prober 探测器接口，用于不同类型的节点探测
type Prober interface {
	// Name 探测器名称
	Name() string
	// Probe 执行探测
	Probe(ctx context.Context, req *ProbeRequest) (*ProbeResponse, error)
}

// ProbeRequest 探测请求
type ProbeRequest struct {
	NodeID   string
	NodeType string
	ProbeURL string
	Timeout  int
	Action   string // 探测动作：health, init 等
	Metadata map[string]interface{}
}

// ProbeResponse 探测响应
type ProbeResponse struct {
	NodeID          string `json:"node_id"`
	State           string `json:"state"`
	Timestamp       string `json:"timestamp"`
	OSName          string `json:"os"`
	FunctionVersion string `json:"function_version"`
	RequestID       string `json:"request_id"`
}

// FunctionInvoker 函数调用接口（用于探测云函数）
type FunctionInvoker interface {
	InvokeFunction(ctx context.Context, nodeID string, eventData interface{}) (interface{}, error)
}

// ========== 全局探测器注册表函数 ==========

// RegisterProber 注册探测器
func RegisterProber(prober Prober) error {
	if prober == nil {
		return fmt.Errorf("prober cannot be nil")
	}

	name := prober.Name()
	if name == "" {
		return fmt.Errorf("prober name cannot be empty")
	}

	probersMu.Lock()
	defer probersMu.Unlock()

	probersMap[name] = prober
	return nil
}

// RegisterDefaultProbers 注册默认探测器
func RegisterDefaultProbers(nodeDAO dao.CloudNodeDAO, accountFactory *provider.AccountFactory) error {
	// 注册HTTP探测器
	err := RegisterProber(NewHTTPHeartbeatProber())
	if err != nil {
		return err
	}

	// 注册SCF探测器（需要依赖注入）
	if nodeDAO != nil && accountFactory != nil {
		err = RegisterProber(NewSCFHeartbeatProber(nodeDAO, accountFactory))
		if err != nil {
			return err
		}
	}
	return nil
}

// GetProber 获取探测器
func GetProber(name string) (Prober, bool) {
	probersMu.RLock()
	defer probersMu.RUnlock()

	prober, exists := probersMap[name]
	return prober, exists
}

// HasProber 检查是否注册了指定探测器
func HasProber(name string) bool {
	probersMu.RLock()
	defer probersMu.RUnlock()

	_, exists := probersMap[name]
	return exists
}

// ListProbers 列出所有探测器
func ListProbers() map[string]Prober {
	probersMu.RLock()
	defer probersMu.RUnlock()

	result := make(map[string]Prober)
	for name, prober := range probersMap {
		result[name] = prober
	}
	return result
}
