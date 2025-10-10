package queue

import (
	"time"
	
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"gorm.io/gorm"
)

// QueueManager 管理所有队列
type QueueManager struct {
	nodeCreationQueue   *NodeCreationQueue
	nodeDeletionQueue   *NodeDeletionQueue
	functionUpdateQueue *FunctionUpdateQueue
	nodeDeploymentQueue *NodeDeploymentQueue
	db                  *gorm.DB
	cfConfig            *config.CloudFunctionConfig
}

// NewQueueManager 创建新的队列管理器
func NewQueueManager(db *gorm.DB) *QueueManager {
	// 加载云函数配置
	cfConfig, err := config.LoadCloudFunctionConfig()
	if err != nil {
		// 如果加载失败，使用默认配置
		cfConfig = config.GetCloudFunctionConfig()
	}

	return &QueueManager{
		nodeCreationQueue:   NewNodeCreationQueue(100),   // 缓冲区大小为100
		nodeDeletionQueue:   NewNodeDeletionQueue(100),   // 缓冲区大小为100
		functionUpdateQueue: NewFunctionUpdateQueue(100), // 缓冲区大小为100
		nodeDeploymentQueue: NewNodeDeploymentQueue(100), // 缓冲区大小为100
		db:                  db,
		cfConfig:            cfConfig,
	}
}

// GetNodeCreationQueue 返回节点创建队列
func (m *QueueManager) GetNodeCreationQueue() *NodeCreationQueue {
	return m.nodeCreationQueue
}

// GetNodeDeletionQueue 返回节点删除队列
func (m *QueueManager) GetNodeDeletionQueue() *NodeDeletionQueue {
	return m.nodeDeletionQueue
}

// GetFunctionUpdateQueue 返回函数更新队列
func (m *QueueManager) GetFunctionUpdateQueue() *FunctionUpdateQueue {
	return m.functionUpdateQueue
}

// EnqueueNodeCreation 将节点创建消息加入队列（统一带任务ID，可为空）
func (m *QueueManager) EnqueueNodeCreation(node *model.SCFNode, taskID, itemID string) error {
	msg := NodeCreationMessage{
		NodeID:         node.NodeID,
		CloudAccountID: node.CloudAccountID,
		Namespace:      node.Namespace,
		Region:         node.Region,
		NodeType:       node.NodeType,
		FunctionName:   node.NodeID, // 使用NodeID作为函数名
		ZipFilePath:    m.cfConfig.GetZipFilePath(),
		TaskID:         taskID,
		ItemID:         itemID,
		NodeData:       node, // 包含完整的节点数据
		CreatedAt:      time.Now(),
	}

	return m.nodeCreationQueue.Enqueue(msg)
}

// EnqueueNodeDeletion 将节点删除消息加入队列
func (m *QueueManager) EnqueueNodeDeletion(node *model.SCFNode, taskID, itemID string) error {
	msg := NodeDeletionMessage{
		NodeID:         node.NodeID,
		CloudAccountID: node.CloudAccountID,
		Namespace:      node.Namespace,
		Region:         node.Region,
		FunctionName:   node.NodeID, // 使用NodeID作为函数名
		TaskID:         taskID,
		ItemID:         itemID,
		CreatedAt:      time.Now(),
	}

	return m.nodeDeletionQueue.Enqueue(msg)
}

// EnqueueFunctionUpdate 将函数更新消息加入队列
func (m *QueueManager) EnqueueFunctionUpdate(node *model.SCFNode, zipFilePath string) error {
	msg := FunctionUpdateMessage{
		NodeID:         node.NodeID,
		CloudAccountID: node.CloudAccountID,
		Namespace:      node.Namespace,
		Region:         node.Region,
		FunctionName:   node.NodeID, // 使用NodeID作为函数名
		ZipFilePath:    zipFilePath,
		CreatedAt:      time.Now(),
	}

	return m.functionUpdateQueue.Enqueue(msg)
}

// GetNodeDeploymentQueue 返回节点部署队列
func (m *QueueManager) GetNodeDeploymentQueue() *NodeDeploymentQueue {
	return m.nodeDeploymentQueue
}

// GetCloudFunctionConfig 返回云函数配置
func (m *QueueManager) GetCloudFunctionConfig() *config.CloudFunctionConfig {
	return m.cfConfig
}

// EnqueueNodeDeployment 将节点部署消息加入队列
func (m *QueueManager) EnqueueNodeDeployment(msg *NodeDeploymentMessage) error {
	return m.nodeDeploymentQueue.Enqueue(*msg)
}

// Close 关闭所有队列
func (m *QueueManager) Close() {
	m.nodeCreationQueue.Close()
	m.nodeDeletionQueue.Close()
	m.functionUpdateQueue.Close()
	m.nodeDeploymentQueue.Close()
}