package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	asynctaskmodel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	packagemgrmodel "github.com/mooyang-code/moox/server/internal/service/packagemgr/model"
	
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// NodeDeploymentWorker 处理异步云函数部署的工作器
type NodeDeploymentWorker struct {
	queueManager        *queue.QueueManager
	cloudAccountService CloudAccountService
	packageService      PackageService
	asyncTaskService    AsyncTaskService
	nodeDAO             dao.SCFNodeDAO
	db                  *gorm.DB
	stopCh              chan struct{}
}

// NewNodeDeploymentWorker 创建新的节点部署工作器
func NewNodeDeploymentWorker(db *gorm.DB, queueManager *queue.QueueManager, cloudAccountService CloudAccountService, packageService PackageService, asyncTaskService AsyncTaskService) *NodeDeploymentWorker {
	return &NodeDeploymentWorker{
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
func (w *NodeDeploymentWorker) Start(ctx context.Context) {
	log.InfoContext(ctx, "[NodeDeploymentWorker] 正在启动工作器...")

	// 启动多个goroutine并发处理
	for i := 0; i < 2; i++ {
		go w.processMessages(ctx, i)
	}
}

// Stop 停止工作器
func (w *NodeDeploymentWorker) Stop() {
	log.Info("[NodeDeploymentWorker] 正在停止工作器...")
	close(w.stopCh)
}

// processMessages 从队列中处理消息
func (w *NodeDeploymentWorker) processMessages(ctx context.Context, workerID int) {
	log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 工作器已启动", workerID)

	nodeDeploymentQueue := w.queueManager.GetNodeDeploymentQueue()

	for {
		select {
		case <-w.stopCh:
			log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 工作器已停止", workerID)
			return
		case msg := <-nodeDeploymentQueue.Channel():
			w.handleMessage(ctx, msg, workerID)
		case <-time.After(5 * time.Second):
			// 定期检查队列
			deployCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
			msg, err := nodeDeploymentQueue.Dequeue(deployCtx)
			cancel()
			if err == nil {
				w.handleMessage(ctx, msg, workerID)
			}
		}
	}
}

// handleMessage 处理单个消息
func (w *NodeDeploymentWorker) handleMessage(ctx context.Context, msg queue.NodeDeploymentMessage, workerID int) {
	log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 正在处理消息: NodeID=%s, FileName=%s, TaskID=%s",
		workerID, msg.NodeID, msg.FileName, msg.TaskID)

	// 1. 验证输入
	if err := w.validateMessage(&msg); err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 2. 验证并获取节点信息
	node, err := w.validateAndGetNode(ctx, &msg, workerID)
	if err != nil {
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 3. 根据PackageID准备函数代码
	codeInfo, err := w.prepareFunctionCode(ctx, msg.PackageID, workerID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 准备函数代码失败: %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("准备函数代码失败: %v", err))
		return
	}

	// 4. 准备云服务
	cloudProvider, err := w.prepareCloudService(ctx, node.CloudAccountID, node.Region)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 5. 执行云函数部署
	if err := w.deployFunction(ctx, cloudProvider, node, codeInfo, workerID); err != nil {
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 6. 更新节点元数据
	if err := w.updateNodeMetadata(ctx, node, &msg, workerID); err != nil {
		log.WarnContextf(ctx, "[NodeDeploymentWorker-%d] 更新节点元数据失败: %v", workerID, err)
		// 这不是致命错误，继续
	}

	// 7. 处理成功
	log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 成功部署云函数: NodeID=%s, FileName=%s",
		workerID, msg.NodeID, msg.FileName)
	w.handleTaskSuccess(ctx, msg.TaskID, msg.ItemID)
}

// validateMessage 验证消息
func (w *NodeDeploymentWorker) validateMessage(msg *queue.NodeDeploymentMessage) error {
	if msg.NodeID == "" {
		return fmt.Errorf("消息验证失败: NodeID为空")
	}
	if msg.CloudAccountID == "" {
		return fmt.Errorf("消息验证失败: CloudAccountID为空")
	}
	if msg.Region == "" {
		return fmt.Errorf("消息验证失败: Region为空")
	}
	// 必须提供PackageID
	if msg.PackageID <= 0 {
		return fmt.Errorf("消息验证失败: PackageID为空")
	}
	return nil
}

// prepareCloudService 准备云服务
func (w *NodeDeploymentWorker) prepareCloudService(ctx context.Context, cloudAccountID, region string) (provider.Client, error) {
	cloudAccount, err := w.cloudAccountService.GetAccountWithoutMask(ctx, cloudAccountID)
	if err != nil {
		return nil, fmt.Errorf("获取云账户信息失败: %w", err)
	}

	// 创建云平台配置
	config := &provider.Config{
		Provider:  provider.Provider(cloudAccount.Provider),
		SecretID:  cloudAccount.SecretID,
		SecretKey: cloudAccount.SecretKey,
		ExtraConfig: map[string]interface{}{
			"region": region,
		},
	}

	// 获取云平台实例
	cloudProvider, err := provider.New(config)
	if err != nil {
		return nil, fmt.Errorf("获取云平台实例失败: %w", err)
	}

	return cloudProvider, nil
}

// handleTaskError 处理任务错误
func (w *NodeDeploymentWorker) handleTaskError(ctx context.Context, taskID, itemID, errorMsg string) {
	if taskID != "" && itemID != "" && w.asyncTaskService != nil {
		err := w.asyncTaskService.UpdateTaskDetailStatus(ctx, taskID, itemID,
			asynctaskmodel.TaskDetailStatusFailed, errorMsg)
		if err != nil {
			log.ErrorContextf(ctx, "[NodeDeploymentWorker] 更新任务详情状态失败: %v", err)
		}
	}
}

// handleTaskSuccess 处理任务成功
func (w *NodeDeploymentWorker) handleTaskSuccess(ctx context.Context, taskID, itemID string) {
	if taskID != "" && itemID != "" && w.asyncTaskService != nil {
		err := w.asyncTaskService.UpdateTaskDetailStatus(ctx, taskID, itemID,
			asynctaskmodel.TaskDetailStatusSuccess, "")
		if err != nil {
			log.ErrorContextf(ctx, "[NodeDeploymentWorker] 更新任务详情状态失败: %v", err)
		}
	}
}

// prepareFunctionCode 根据PackageID准备函数代码
func (w *NodeDeploymentWorker) prepareFunctionCode(ctx context.Context, packageID int64, workerID int) (*FunctionCodeInfo, error) {
	if packageID <= 0 {
		return nil, fmt.Errorf("代码包ID为空")
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
		log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 使用COS部署: bucket=%s, path=%s, region=%s, runtime=%s",
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
		if _, err := os.Stat(localPath); err != nil {
			return nil, fmt.Errorf("本地代码包文件不存在: %s", localPath)
		}

		log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 使用本地存储文件: %s, runtime=%s", workerID, localPath, pkg.Runtime)
		zipBase64, err := w.prepareZipFile(ctx, localPath, workerID)
		if err != nil {
			return nil, fmt.Errorf("读取本地代码包文件失败: %w", err)
		}
		return &FunctionCodeInfo{
			ZipFile: zipBase64,
			Runtime: pkg.Runtime,
		}, nil
	}

	// 如果既不是COS存储也不是本地存储，返回错误
	return nil, fmt.Errorf("代码包存储配置无效：COSBucket=%s, CloudAccountID=%s", pkg.COSBucket, pkg.CloudAccountID)
}

// prepareZipFile 读取并编码ZIP文件
func (w *NodeDeploymentWorker) prepareZipFile(ctx context.Context, zipFilePath string, workerID int) (string, error) {
	zipData, err := ioutil.ReadFile(zipFilePath)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 读取zip文件失败: %v", workerID, err)
		return "", fmt.Errorf("读取zip文件失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(zipData), nil
}

// validateAndGetNode 验证并获取节点信息
func (w *NodeDeploymentWorker) validateAndGetNode(ctx context.Context, msg *queue.NodeDeploymentMessage, workerID int) (*model.SCFNode, error) {
	// 获取节点信息
	node, err := w.nodeDAO.GetSCFNode(ctx, msg.NodeID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 获取节点信息失败: %v", workerID, err)
		return nil, fmt.Errorf("获取节点信息失败: %v", err)
	}

	if node == nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 节点不存在: %s", workerID, msg.NodeID)
		return nil, fmt.Errorf("节点不存在: %s", msg.NodeID)
	}

	// 检查节点类型
	if node.NodeType != model.NodeTypeSCF {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 节点类型错误: %s", workerID, node.NodeType)
		return nil, fmt.Errorf("节点类型错误: %s (需要SCF)", node.NodeType)
	}
	return node, nil
}

// buildUpdateRequest 构建函数更新请求
func (w *NodeDeploymentWorker) buildUpdateRequest(ctx context.Context, node *model.SCFNode, codeInfo *FunctionCodeInfo, workerID int) (*provider.UpdateFunctionRequest, error) {
	updateReq := &provider.UpdateFunctionRequest{
		FunctionName: node.NodeID,
		Namespace:    node.Namespace,
	}

	// 根据代码信息类型设置更新参数
	if codeInfo.COSBucket != "" && codeInfo.COSPath != "" && codeInfo.COSRegion != "" {
		// 使用COS更新
		updateReq.COSBucket = codeInfo.COSBucket
		updateReq.COSPath = codeInfo.COSPath
		updateReq.COSRegion = codeInfo.COSRegion
		log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 使用COS更新函数代码: bucket=%s, path=%s, region=%s",
			workerID, codeInfo.COSBucket, codeInfo.COSPath, codeInfo.COSRegion)
	} else if codeInfo.ZipFile != "" {
		// 使用ZIP文件更新
		updateReq.ZipFile = codeInfo.ZipFile
		log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 使用ZIP文件更新函数代码", workerID)
	} else {
		return nil, fmt.Errorf("代码信息不完整，无法更新函数")
	}
	return updateReq, nil
}

// deployFunction 执行云函数部署
func (w *NodeDeploymentWorker) deployFunction(ctx context.Context, cloudProvider provider.Client, node *model.SCFNode, codeInfo *FunctionCodeInfo, workerID int) error {
	log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 开始更新云函数代码: FunctionName=%s, Namespace=%s",
		workerID, node.NodeID, node.Namespace)

	// 构建更新请求
	updateReq, err := w.buildUpdateRequest(ctx, node, codeInfo, workerID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 构建更新请求失败: %v", workerID, err)
		return fmt.Errorf("构建更新请求失败: %v", err)
	}

	// 执行函数更新
	err = cloudProvider.UpdateFunction(ctx, updateReq)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 更新云函数代码失败: %v", workerID, err)
		return fmt.Errorf("更新云函数代码失败: %v", err)
	}
	return nil
}

// updateNodeMetadata 更新节点元数据
func (w *NodeDeploymentWorker) updateNodeMetadata(ctx context.Context, node *model.SCFNode, msg *queue.NodeDeploymentMessage, workerID int) error {
	now := time.Now()
	node.ModifyTime = now

	// 更新元数据中的部署信息
	metadata := make(map[string]interface{})
	if node.Metadata != "" {
		// 解析现有元数据
		_ = json.Unmarshal([]byte(node.Metadata), &metadata)
	}
	metadata["last_deploy_time"] = now.Format(time.RFC3339)
	metadata["last_deploy_file"] = msg.FileName

	metadataBytes, _ := json.Marshal(metadata)
	node.Metadata = string(metadataBytes)

	if err := w.nodeDAO.UpdateSCFNode(ctx, node); err != nil {
		log.WarnContextf(ctx, "[NodeDeploymentWorker-%d] 更新节点部署信息失败: %v", workerID, err)
		// 这不是致命错误，继续
	}
	return nil
}
