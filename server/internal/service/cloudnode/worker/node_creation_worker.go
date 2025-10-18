package worker

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	asynctaskmodel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	packagemgrmodel "github.com/mooyang-code/moox/server/internal/service/packagemgr/model"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Platform 云平台类型别名，避免 provider.CloudPlatform 的啰嗦写法
type Platform = provider.CloudPlatform

// NodeCreationWorker 处理异步云函数创建的工作器
type NodeCreationWorker struct {
	queueManager        *queue.Manager
	cloudAccountService CloudAccountService
	packageService      PackageService
	asyncTaskService    AsyncTaskService
	nodeDAO             dao.SCFNodeDAO
	db                  *gorm.DB
	stopCh              chan struct{}
}

// NewNodeCreationWorker 创建新的节点创建工作器
func NewNodeCreationWorker(db *gorm.DB, queueManager *queue.Manager, cloudAccountService CloudAccountService, packageService PackageService, asyncTaskService AsyncTaskService) *NodeCreationWorker {
	return &NodeCreationWorker{
		queueManager:        queueManager,
		cloudAccountService: cloudAccountService,
		packageService:      packageService,
		asyncTaskService:    asyncTaskService,
		nodeDAO:             dao.NewSCFNodeDAO(db),
		db:                  db,
		stopCh:              make(chan struct{}),
	}
}

// Start 启动工作器
func (w *NodeCreationWorker) Start(ctx context.Context) {
	log.InfoContext(ctx, "[NodeCreationWorker] 正在启动工作器...")

	// 启动多个goroutine并发处理
	for i := 0; i < 3; i++ {
		go w.processMessages(ctx, i)
	}
}

// Stop 停止工作器
func (w *NodeCreationWorker) Stop() {
	log.Info("[NodeCreationWorker] 正在停止工作器...")
	close(w.stopCh)
}

// processMessages 从队列中处理消息
func (w *NodeCreationWorker) processMessages(ctx context.Context, workerID int) {
	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 工作器已启动", workerID)

	nodeCreationQueue := w.queueManager.GetNodeCreationQueue()

	for {
		select {
		case <-w.stopCh:
			log.InfoContextf(ctx, "[NodeCreationWorker-%d] 工作器已停止", workerID)
			return
		case msg := <-nodeCreationQueue.Channel():
			w.handleMessage(ctx, msg, workerID)
		case <-time.After(5 * time.Second):
			// 检查内存队列中的消息
			msg, err := nodeCreationQueue.Dequeue(ctx)
			if err == nil {
				w.handleMessage(ctx, msg, workerID)
			}
		}
	}
}

// handleMessage 处理单个消息
func (w *NodeCreationWorker) handleMessage(ctx context.Context, msg queue.NodeCreationMessage, workerID int) {
	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 正在处理消息: NodeID=%s, Region=%s", workerID, msg.NodeID, msg.Region)

	// 1. 验证输入
	if err := w.validateMessage(&msg); err != nil {
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, "validateMessage-"+err.Error())
		return
	}

	// 2. 准备云服务
	cloudProvider, err := w.prepareCloudProvider(ctx, msg.CloudAccountID, msg.Region, workerID)
	if err != nil {
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, "prepareCloudProvider-"+err.Error())
		return
	}

	// 3. 创建云资源（命名空间和函数）
	functionInfo, err := w.createCloudResources(ctx, cloudProvider, &msg, workerID)
	if err != nil {
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, "createCloudResources-"+err.Error())
		return
	}

	// 4. 保存节点到数据库
	if err := w.saveNodeToDB(ctx, &msg, functionInfo, workerID); err != nil {
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, "saveNodeToDB-"+err.Error())
		return
	}

	// 5. 更新任务状态（如果有）
	if msg.TaskID != "" && msg.ItemID != "" {
		w.handleTaskSuccess(ctx, msg.TaskID, msg.ItemID)
	}
}

// validateMessage 验证消息的必要字段
func (w *NodeCreationWorker) validateMessage(msg *queue.NodeCreationMessage) error {
	if msg.NodeData == nil {
		return fmt.Errorf("消息中没有节点数据")
	}
	if msg.CloudAccountID == "" {
		return fmt.Errorf("云账户ID不能为空")
	}
	if msg.Region == "" {
		return fmt.Errorf("区域不能为空")
	}
	if msg.Namespace == "" {
		return fmt.Errorf("命名空间不能为空")
	}
	if msg.ZipFilePath == "" {
		return fmt.Errorf("ZIP文件路径不能为空")
	}
	return nil
}

// prepareCloudProvider 准备云服务提供商实例
func (w *NodeCreationWorker) prepareCloudProvider(ctx context.Context, cloudAccountID, region string, workerID int) (provider.Client, error) {
	// 获取云账户（内部使用不脱敏）
	account, err := w.cloudAccountService.GetAccountWithoutMask(ctx, cloudAccountID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 获取云账户失败: %v", workerID, err)
		return nil, fmt.Errorf("获取云账户失败: %w", err)
	}

	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 使用云账户: ID=%s, Provider=%s",
		workerID, account.AccountID, account.Provider)

	// 创建云平台配置
	config := &provider.Config{
		Provider:  Platform(account.Provider),
		SecretID:  account.SecretID,
		SecretKey: account.SecretKey,
		ExtraConfig: map[string]interface{}{
			"region": region,
		},
	}

	// 获取云平台实例
	cloudProvider, err := provider.New(config)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 获取云平台实例失败: %v", workerID, err)
		return nil, fmt.Errorf("获取云平台实例失败: %w", err)
	}

	return cloudProvider, nil
}

// createCloudResources 创建云资源（命名空间和函数）
func (w *NodeCreationWorker) createCloudResources(ctx context.Context, cloudProvider provider.Client,
	msg *queue.NodeCreationMessage, workerID int) (*provider.FunctionInfo, error) {
	// 1. 创建或确认命名空间
	if err := w.ensureNamespace(ctx, cloudProvider, msg.Namespace, msg.NodeID, workerID); err != nil {
		return nil, err
	}

	// 2. 读取并准备函数代码（如果本地ZIP文件存在）
	zipBase64 := w.prepareLocalZipFile(ctx, msg.ZipFilePath, workerID)

	// 3. 创建或获取云函数
	functionInfo, err := w.ensureFunction(ctx, cloudProvider, msg, zipBase64, workerID)
	if err != nil {
		return nil, err
	}
	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 成功完成云函数设置: %+v", workerID, functionInfo)
	return functionInfo, nil
}

// ensureNamespace 确保命名空间存在
func (w *NodeCreationWorker) ensureNamespace(ctx context.Context, cloudProvider provider.Client, namespace, nodeID string, workerID int) error {
	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 正在创建命名空间: %s", workerID, namespace)
	err := cloudProvider.CreateNamespace(ctx, namespace, fmt.Sprintf("moox节点 %s 的命名空间", nodeID))
	if err != nil {
		// 检查命名空间是否已存在
		if strings.Contains(err.Error(), "ResourceInUse.Namespace") || strings.Contains(err.Error(), "already exists") {
			log.InfoContextf(ctx, "[NodeCreationWorker-%d] 命名空间 %s 已存在，继续执行", workerID, namespace)
			return nil
		}
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 创建命名空间失败: %v", workerID, err)
		return fmt.Errorf("创建命名空间失败: %w", err)
	}
	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 成功创建命名空间: %s", workerID, namespace)
	return nil
}

// prepareZipFile 读取并编码ZIP文件
func (w *NodeCreationWorker) prepareZipFile(ctx context.Context, zipFilePath string, workerID int) (string, error) {
	zipData, err := os.ReadFile(zipFilePath)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 读取zip文件失败: %v", workerID, err)
		return "", fmt.Errorf("读取zip文件失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(zipData), nil
}

// prepareLocalZipFile 准备本地ZIP文件
func (w *NodeCreationWorker) prepareLocalZipFile(ctx context.Context, zipFilePath string, workerID int) string {
	// 如果没有配置ZIP文件路径，直接返回空字符串
	if zipFilePath == "" {
		log.InfoContextf(ctx, "[NodeCreationWorker-%d] 未配置ZIP文件路径，将使用COS默认代码", workerID)
		return ""
	}

	// 如果文件不存在，直接返回空字符串
	if _, err := os.Stat(zipFilePath); err != nil {
		log.InfoContextf(ctx, "[NodeCreationWorker-%d] 本地ZIP文件不存在: %s，将使用COS默认代码", workerID, zipFilePath)
		return ""
	}

	// 文件存在，尝试读取
	zipBase64, err := w.prepareZipFile(ctx, zipFilePath, workerID)
	if err != nil {
		log.WarnContextf(ctx, "[NodeCreationWorker-%d] 读取本地ZIP文件失败，将使用COS默认代码: %v", workerID, err)
		return ""
	}
	return zipBase64
}

// ensureFunction 确保云函数存在
func (w *NodeCreationWorker) ensureFunction(ctx context.Context, cloudProvider provider.Client,
	msg *queue.NodeCreationMessage, zipBase64 string, workerID int) (*provider.FunctionInfo, error) {
	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 正在创建云函数: %s", workerID, msg.FunctionName)

	// 根据PackageID获取COS信息或使用本地ZIP文件
	cosInfo, err := w.prepareFunctionCode(ctx, msg.PackageID, zipBase64, workerID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 准备函数代码失败: %v", workerID, err)
		return nil, fmt.Errorf("准备函数代码失败: %w", err)
	}

	req := &provider.CreateFunctionRequest{
		FunctionName: msg.FunctionName,
		Namespace:    msg.Namespace,
		Runtime:      cosInfo.Runtime,
		Description:  fmt.Sprintf("由moox为节点 %s 创建", msg.NodeID),
		ZipFile:      cosInfo.ZipFile,
		COSBucket:    cosInfo.COSBucket,
		COSPath:      cosInfo.COSPath,
		COSRegion:    cosInfo.COSRegion,
		MemorySize:   1024, // 运行内存MB
		Timeout:      240,  // 创建超时秒
		Environment: map[string]string{
			"NODE_ID": msg.NodeID,
			"REGION":  msg.Region,
		},
	}

	functionInfo, err := cloudProvider.CreateFunction(ctx, req)
	if err != nil {
		// 检查函数是否已存在
		if strings.Contains(err.Error(), "ResourceInUse.Function") || strings.Contains(err.Error(), "already exists") {
			log.InfoContextf(ctx, "[NodeCreationWorker-%d] 函数 %s 已存在，获取现有函数信息", workerID, msg.FunctionName)
			return w.getExistingFunction(ctx, cloudProvider, msg.FunctionName, msg.Namespace, workerID)
		}
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 创建云函数失败: %v;", workerID, err)
		return nil, fmt.Errorf("创建云函数失败: %w", err)
	}

	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 成功创建云函数: %s", workerID, msg.FunctionName)
	w.waitForFunctionReady(ctx, cloudProvider, msg.FunctionName, msg.Namespace, workerID)
	return functionInfo, nil
}

// getExistingFunction 获取已存在的函数信息
func (w *NodeCreationWorker) getExistingFunction(ctx context.Context, cloudProvider provider.Client, functionName, namespace string, workerID int) (*provider.FunctionInfo, error) {
	functionInfo, err := cloudProvider.GetFunction(ctx, functionName, namespace)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 获取已存在的函数失败: %v", workerID, err)
		return nil, fmt.Errorf("获取已存在的函数失败: %w", err)
	}
	return functionInfo, nil
}

// saveNodeToDB 保存节点到数据库
func (w *NodeCreationWorker) saveNodeToDB(ctx context.Context, msg *queue.NodeCreationMessage, functionInfo *provider.FunctionInfo, workerID int) error {
	// 更新节点元数据，记录函数信息
	metadata := fmt.Sprintf(`{"function_name":"%s","namespace":"%s","status":"%s","created_at":"%s"}`,
		functionInfo.FunctionName,
		functionInfo.Namespace,
		functionInfo.Status,
		time.Now().Format(time.RFC3339))
	msg.NodeData.Metadata = metadata

	// 更新节点状态为在线
	msg.NodeData.Status = cloudnodemodel.NodeStatusOnline

	// 创建节点记录
	err := w.nodeDAO.CreateSCFNode(ctx, msg.NodeData)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 创建节点记录失败: %v", workerID, err)
		return fmt.Errorf("创建节点记录失败: %w", err)
	}

	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 成功创建节点 %s", workerID, msg.NodeID)
	return nil
}

// handleTaskError 处理任务错误
func (w *NodeCreationWorker) handleTaskError(ctx context.Context, taskID, itemID, errorMsg string) {
	log.ErrorContextf(ctx, "handleTaskError: taskID=%s, itemID=%s, errorMsg=%s ", taskID, itemID, errorMsg)
	// 如果有任务ID，更新任务状态
	if taskID != "" && itemID != "" {
		err := w.asyncTaskService.UpdateTaskDetailStatus(ctx, taskID, itemID, asynctaskmodel.TaskDetailStatusFailed, errorMsg)
		if err != nil {
			log.ErrorContextf(ctx, "[NodeCreationWorker] 更新任务详情状态失败: %v", err)
		}
	}
}

// handleTaskSuccess 处理任务成功
func (w *NodeCreationWorker) handleTaskSuccess(ctx context.Context, taskID, itemID string) {
	// 如果有任务ID，更新任务状态
	if taskID != "" && itemID != "" {
		err := w.asyncTaskService.UpdateTaskDetailStatus(ctx, taskID, itemID, asynctaskmodel.TaskDetailStatusSuccess, "")
		if err != nil {
			log.ErrorContextf(ctx, "[NodeCreationWorker] 更新任务详情状态失败: %v", err)
		}
	}
}

// waitForFunctionReady 等待函数变为活跃状态
func (w *NodeCreationWorker) waitForFunctionReady(ctx context.Context, cloudProvider provider.Client, functionName, namespace string, workerID int) {
	log.InfoContextf(ctx, "[NodeCreationWorker-%d] 等待函数 %s 就绪...", workerID, functionName)

	maxWaitTime := 5 * time.Minute
	startTime := time.Now()

	for {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			log.InfoContextf(ctx, "[NodeCreationWorker-%d] 等待函数时上下文已取消", workerID)
			return
		default:
		}

		// 检查是否超过最大等待时间
		if time.Since(startTime) > maxWaitTime {
			log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 等待函数就绪超时", workerID)
			return
		}

		// 获取函数状态
		functionInfo, err := cloudProvider.GetFunction(ctx, functionName, namespace)
		if err != nil {
			log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 获取函数状态失败: %v，继续等待...", workerID, err)
			time.Sleep(2 * time.Second)
			continue
		}

		if functionInfo != nil && functionInfo.Status == "Active" {
			log.InfoContextf(ctx, "[NodeCreationWorker-%d] 函数 %s 现已激活！", workerID, functionName)
			return
		}

		if functionInfo != nil && functionInfo.Status == "Failed" {
			log.ErrorContextf(ctx, "[NodeCreationWorker-%d] 函数创建失败，状态: %s", workerID, functionInfo.Status)
			return
		}

		log.InfoContextf(ctx, "[NodeCreationWorker-%d] 函数状态: %s，等待中...", workerID, functionInfo.Status)
		time.Sleep(2 * time.Second)
	}
}

// PackageService 包管理服务接口
type PackageService interface {
	GetPackageByID(ctx context.Context, packageID int64) (*packagemgrmodel.FunctionPackage, error)
}

// PackageServiceAdapter 包管理服务适配器 - 将FunctionPackageService适配为PackageService接口
type PackageServiceAdapter struct {
	functionPackageService interface {
		GetPackageDetailModel(ctx context.Context, id int64) (*packagemgrmodel.FunctionPackage, error)
	}
}

// NewPackageServiceAdapter 创建包管理服务适配器
func NewPackageServiceAdapter(functionPackageService interface {
	GetPackageDetailModel(ctx context.Context, id int64) (*packagemgrmodel.FunctionPackage, error)
}) PackageService {
	return &PackageServiceAdapter{
		functionPackageService: functionPackageService,
	}
}

// GetPackageByID 根据ID获取代码包信息
func (a *PackageServiceAdapter) GetPackageByID(ctx context.Context, packageID int64) (*packagemgrmodel.FunctionPackage, error) {
	return a.functionPackageService.GetPackageDetailModel(ctx, packageID)
}

// FunctionCodeInfo 函数代码信息
type FunctionCodeInfo struct {
	ZipFile   string // base64编码的ZIP文件（用于本地上传）
	COSBucket string // COS桶名（用于COS部署）
	COSPath   string // COS路径（用于COS部署）
	COSRegion string // COS区域（用于COS部署）
	Runtime   string // 运行时环境
}

// prepareFunctionCode 根据PackageID准备函数代码
func (w *NodeCreationWorker) prepareFunctionCode(ctx context.Context, packageID int64, fallbackZipBase64 string, workerID int) (*FunctionCodeInfo, error) {
	if packageID <= 0 {
		return nil, fmt.Errorf("代码包ID是必需的，不能为空或小于等于0")
	}

	// 获取代码包信息
	pkg, err := w.packageService.GetPackageByID(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("获取代码包信息失败: %w", err)
	}

	if pkg.Status != packagemgrmodel.PackageStatusAvailable {
		return nil, fmt.Errorf("代码包状态不可用: %d", pkg.Status)
	}

	// 判断是否为COS存储（COSBucket不是"local"且有完整的COS信息）
	if pkg.COSBucket != "local" && pkg.CloudAccountID != "" && pkg.COSBucket != "" && pkg.COSPath != "" && pkg.COSRegion != "" {
		log.InfoContextf(ctx, "[NodeCreationWorker-%d] 使用COS部署: bucket=%s, path=%s, region=%s, runtime=%s",
			workerID, pkg.COSBucket, pkg.COSPath, pkg.COSRegion, pkg.Runtime)
		return &FunctionCodeInfo{
			COSBucket: pkg.COSBucket,
			COSPath:   pkg.COSPath,
			COSRegion: pkg.COSRegion,
			Runtime:   pkg.Runtime,
		}, nil
	}

	// 使用本地存储（COSBucket标记为"local"）
	if pkg.COSBucket == "local" {
		localPath := constants.GetPackageFilePath(pkg.ID, pkg.OriginalFilename)

		// 检查本地文件是否存在
		if _, err := os.Stat(localPath); err == nil {
			log.InfoContextf(ctx, "[NodeCreationWorker-%d] 使用本地存储文件: %s, runtime=%s", workerID, localPath, pkg.Runtime)
			zipBase64, err := w.prepareZipFile(ctx, localPath, workerID)
			if err != nil {
				return nil, fmt.Errorf("读取本地代码包文件失败: %w", err)
			}
			return &FunctionCodeInfo{
				ZipFile: zipBase64,
				Runtime: pkg.Runtime,
			}, nil
		}

		// 本地文件不存在，使用fallback ZIP文件
		log.WarnContextf(ctx, "[NodeCreationWorker-%d] 本地文件不存在: %s，使用fallback ZIP文件", workerID, localPath)
		return &FunctionCodeInfo{
			ZipFile: fallbackZipBase64,
			Runtime: pkg.Runtime,
		}, nil
	}

	// 如果既不是COS存储也不是本地存储，返回错误
	return nil, fmt.Errorf("代码包存储配置无效：COSBucket=%s, CloudAccountID=%s", pkg.COSBucket, pkg.CloudAccountID)
}
