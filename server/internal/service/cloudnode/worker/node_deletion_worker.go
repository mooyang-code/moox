package worker

import (
	"context"
	"fmt"
	"strings"
	"time"

	asynctaskmodel "github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// NodeDeletionWorker 处理异步云函数删除的工作器
type NodeDeletionWorker struct {
	queueManager        *queue.Manager
	cloudAccountService CloudAccountService
	asyncTaskService    AsyncTaskService
	nodeDAO             dao.SCFNodeDAO
	db                  *gorm.DB
	stopCh              chan struct{}
}

// NewNodeDeletionWorker 创建新的节点删除工作器
func NewNodeDeletionWorker(db *gorm.DB, queueManager *queue.Manager, cloudAccountService CloudAccountService, asyncTaskService AsyncTaskService) *NodeDeletionWorker {
	return &NodeDeletionWorker{
		queueManager:        queueManager,
		cloudAccountService: cloudAccountService,
		asyncTaskService:    asyncTaskService,
		nodeDAO:             dao.NewSCFNodeDAO(db),
		db:                  db,
		stopCh:              make(chan struct{}),
	}
}

// Start 启动工作器
func (w *NodeDeletionWorker) Start(ctx context.Context) {
	log.InfoContext(ctx, "[NodeDeletionWorker] 正在启动工作器...")

	// 启动多个goroutine并发处理
	for i := 0; i < 2; i++ {
		go w.processMessages(ctx, i)
	}
}

// Stop 停止工作器
func (w *NodeDeletionWorker) Stop() {
	log.Info("[NodeDeletionWorker] 正在停止工作器...")
	close(w.stopCh)
}

// processMessages 从队列中处理消息
func (w *NodeDeletionWorker) processMessages(ctx context.Context, workerID int) {
	log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 工作器已启动", workerID)

	nodeDeletionQueue := w.queueManager.GetNodeDeletionQueue()

	for {
		select {
		case <-w.stopCh:
			log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 工作器已停止", workerID)
			return
		case msg := <-nodeDeletionQueue.Channel():
			w.handleMessage(ctx, msg, workerID)
		case <-time.After(5 * time.Second):
			// 定期检查是否有任务
			// 注意：NodeDeletionQueue 没有 Dequeue 方法，只能从 channel 读取
		}
	}
}

// handleMessage 处理单个消息
func (w *NodeDeletionWorker) handleMessage(ctx context.Context, msg queue.NodeDeletionMessage, workerID int) {
	log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 正在处理消息: NodeID=%s, Region=%s", workerID, msg.NodeID, msg.Region)

	// 1. 验证输入
	if err := w.validateMessage(&msg); err != nil {
		log.ErrorContextf(ctx, "[NodeDeletionWorker-%d] %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 2. 获取节点信息（确认节点存在）
	node, err := w.nodeDAO.GetSCFNode(ctx, msg.NodeID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeletionWorker-%d] 获取节点信息失败: %v", workerID, err)
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, fmt.Sprintf("获取节点信息失败: %v", err))
		return
	}
	if node == nil {
		log.ErrorContextf(ctx, "[NodeDeletionWorker-%d] 节点不存在: %s", workerID, msg.NodeID)
		// 节点不存在也算删除成功
		w.handleTaskSuccess(ctx, msg.TaskID, msg.ItemID)
		return
	}

	// 3. 准备云服务
	cloudProvider, err := w.prepareCloudProvider(ctx, msg.CloudAccountID, msg.Region, workerID)
	if err != nil {
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 4. 删除云函数
	err = w.deleteCloudFunction(ctx, cloudProvider, &msg, workerID)
	if err != nil {
		// 如果云函数不存在，也认为删除成功
		if strings.Contains(err.Error(), "ResourceNotFound") || strings.Contains(err.Error(), "not found") {
			log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 云函数 %s 不存在，继续删除数据库记录", workerID, msg.FunctionName)
		} else {
			w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
			return
		}
	}

	// 5. 删除数据库记录
	if err := w.deleteNodeFromDB(ctx, msg.NodeID, workerID); err != nil {
		w.handleTaskError(ctx, msg.TaskID, msg.ItemID, err.Error())
		return
	}

	// 6. 更新任务状态（如果有）
	if msg.TaskID != "" && msg.ItemID != "" {
		w.handleTaskSuccess(ctx, msg.TaskID, msg.ItemID)
	}
}

// validateMessage 验证消息的必要字段
func (w *NodeDeletionWorker) validateMessage(msg *queue.NodeDeletionMessage) error {
	if msg.NodeID == "" {
		return fmt.Errorf("节点ID不能为空")
	}
	if msg.CloudAccountID == "" {
		return fmt.Errorf("云账户ID不能为空")
	}
	if msg.Region == "" {
		return fmt.Errorf("区域不能为空")
	}
	if msg.FunctionName == "" {
		return fmt.Errorf("函数名称不能为空")
	}
	if msg.Namespace == "" {
		return fmt.Errorf("命名空间不能为空")
	}
	return nil
}

// prepareCloudProvider 准备云服务提供商实例
func (w *NodeDeletionWorker) prepareCloudProvider(ctx context.Context, cloudAccountID, region string, workerID int) (provider.Client, error) {
	// 获取云账户（内部使用不脱敏）
	account, err := w.cloudAccountService.GetAccountWithoutMask(ctx, cloudAccountID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeletionWorker-%d] 获取云账户失败: %v", workerID, err)
		return nil, fmt.Errorf("获取云账户失败: %w", err)
	}

	log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 使用云账户: ID=%s, Provider=%s",
		workerID, account.AccountID, account.Provider)

	// 创建云平台配置
	config := &provider.Config{
		Provider:  provider.CloudPlatform(account.Provider),
		SecretID:  account.SecretID,
		SecretKey: account.SecretKey,
		ExtraConfig: map[string]interface{}{
			"region": region,
		},
	}

	// 获取云平台实例
	cloudProvider, err := provider.New(config)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeletionWorker-%d] 获取云平台实例失败: %v", workerID, err)
		return nil, fmt.Errorf("获取云平台实例失败: %w", err)
	}

	return cloudProvider, nil
}

// deleteCloudFunction 删除云函数
func (w *NodeDeletionWorker) deleteCloudFunction(ctx context.Context, cloudProvider provider.Client, msg *queue.NodeDeletionMessage, workerID int) error {
	log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 正在删除云函数: %s (namespace: %s)", workerID, msg.FunctionName, msg.Namespace)

	err := cloudProvider.DeleteFunction(ctx, msg.FunctionName, msg.Namespace)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeletionWorker-%d] 删除云函数失败: %v", workerID, err)
		return fmt.Errorf("删除云函数失败: %w", err)
	}

	log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 成功删除云函数: %s", workerID, msg.FunctionName)
	return nil
}

// deleteNodeFromDB 从数据库删除节点记录
func (w *NodeDeletionWorker) deleteNodeFromDB(ctx context.Context, nodeID string, workerID int) error {
	err := w.nodeDAO.DeleteSCFNode(ctx, nodeID)
	if err != nil {
		log.ErrorContextf(ctx, "[NodeDeletionWorker-%d] 删除节点记录失败: %v", workerID, err)
		return fmt.Errorf("删除节点记录失败: %w", err)
	}

	log.InfoContextf(ctx, "[NodeDeletionWorker-%d] 成功删除节点 %s 的数据库记录", workerID, nodeID)
	return nil
}

// handleTaskError 处理任务错误
func (w *NodeDeletionWorker) handleTaskError(ctx context.Context, taskID, itemID, errorMsg string) {
	log.ErrorContextf(ctx, "handleTaskError: taskID=%s, itemID=%s, errorMsg=%s", taskID, itemID, errorMsg)
	// 如果有任务ID，更新任务状态
	if taskID != "" && itemID != "" {
		err := w.asyncTaskService.UpdateTaskDetailStatus(ctx, taskID, itemID, asynctaskmodel.TaskDetailStatusFailed, errorMsg)
		if err != nil {
			log.ErrorContextf(ctx, "[NodeDeletionWorker] 更新任务详情状态失败: %v", err)
		}
	}
}

// handleTaskSuccess 处理任务成功
func (w *NodeDeletionWorker) handleTaskSuccess(ctx context.Context, taskID, itemID string) {
	log.InfoContextf(ctx, "handleTaskSuccess: taskID=%s, itemID=%s", taskID, itemID)
	// 如果有任务ID，更新任务状态
	if taskID != "" && itemID != "" {
		err := w.asyncTaskService.UpdateTaskDetailStatus(ctx, taskID, itemID, asynctaskmodel.TaskDetailStatusSuccess, "")
		if err != nil {
			log.ErrorContextf(ctx, "[NodeDeletionWorker] 更新任务详情状态失败: %v", err)
		} else {
			log.InfoContextf(ctx, "[NodeDeletionWorker] 成功更新任务详情状态为成功: taskID=%s, itemID=%s", taskID, itemID)
		}
	}
}
