package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	asynctaskmodel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// NodeDeploymentWorker 处理异步云函数部署的工作器
type NodeDeploymentWorker struct {
	queueManager        *queue.QueueManager
	cloudAccountService CloudAccountService
	asyncTaskService    AsyncTaskService
	nodeDAO             dao.SCFNodeDAO
	db                  *gorm.DB
	stopCh              chan struct{}
}

// NewNodeDeploymentWorker 创建新的节点部署工作器
func NewNodeDeploymentWorker(db *gorm.DB, queueManager *queue.QueueManager, cloudAccountService CloudAccountService, asyncTaskService AsyncTaskService) *NodeDeploymentWorker {
	return &NodeDeploymentWorker{
		queueManager:        queueManager,
		cloudAccountService: cloudAccountService,
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

	// 2. 获取节点信息
	node, err := w.nodeDAO.GetSCFNode(ctx, msg.NodeID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 获取节点信息失败: %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("获取节点信息失败: %v", err))
		return
	}
	if node == nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 节点不存在: %s", workerID, msg.NodeID)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("节点不存在: %s", msg.NodeID))
		return
	}

	// 3. 检查节点类型
	if node.NodeType != model.NodeTypeSCF {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 节点类型错误: %s", workerID, node.NodeType)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("节点类型错误: %s (需要SCF)", node.NodeType))
		return
	}

	// 4. 解码并保存部署文件
	tempDir, err := ioutil.TempDir("", "deploy_")
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 创建临时目录失败: %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("创建临时目录失败: %v", err))
		return
	}
	defer os.RemoveAll(tempDir)

	zipFilePath := filepath.Join(tempDir, msg.FileName)
	zipData, err := base64.StdEncoding.DecodeString(msg.ZipFileBase64)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 解码base64失败: %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("解码部署文件失败: %v", err))
		return
	}

	err = ioutil.WriteFile(zipFilePath, zipData, 0644)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 保存文件失败: %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("保存部署文件失败: %v", err))
		return
	}

	// 5. 准备云服务
	cloudProvider, err := w.prepareCloudService(ctx, node.CloudAccountID, node.Region)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 6. 将 base64 解码后的文件进行 base64 编码（CloudProvider 需要 base64 格式的内容）
	zipContentBase64 := base64.StdEncoding.EncodeToString(zipData)
	
	// 7. 更新云函数代码
	log.InfoContextf(ctx, "[NodeDeploymentWorker-%d] 开始更新云函数代码: FunctionName=%s, Namespace=%s", 
		workerID, node.NodeID, node.Namespace)
	
	updateReq := &provider.UpdateFunctionRequest{
		FunctionName: node.NodeID,
		Namespace:    node.Namespace,
		ZipFile:      zipContentBase64,
	}
	
	err = cloudProvider.UpdateFunction(ctx, updateReq)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeploymentWorker-%d] 更新云函数代码失败: %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("更新云函数代码失败: %v", err))
		return
	}

	// 8. 更新节点的部署时间和版本信息
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

	// 9. 处理成功
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
	if msg.ZipFileBase64 == "" {
		return fmt.Errorf("消息验证失败: ZipFileBase64为空")
	}
	if msg.FileName == "" {
		return fmt.Errorf("消息验证失败: FileName为空")
	}
	return nil
}

// prepareCloudService 准备云服务
func (w *NodeDeploymentWorker) prepareCloudService(ctx context.Context, cloudAccountID, region string) (provider.Client, error) {
	cloudAccount, err := w.cloudAccountService.GetAccountWithoutMask(ctx, cloudAccountID)
	if err != nil {
		return nil, fmt.Errorf("获取云账户信息失败: %w", err)
	}

	// 创建云厂商配置
	config := &provider.CloudConfig{
		Provider:  provider.ProviderType(cloudAccount.Provider),
		SecretID:  cloudAccount.SecretID,
		SecretKey: cloudAccount.SecretKey,
		ExtraConfig: map[string]interface{}{
			"region": region,
		},
	}

	// 获取云厂商实例
	cloudProvider, err := provider.NewCloudProvider(config)
	if err != nil {
		return nil, fmt.Errorf("获取云厂商实例失败: %w", err)
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