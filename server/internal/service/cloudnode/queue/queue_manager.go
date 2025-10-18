package queue

import (
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"gorm.io/gorm"
)

// Manager 管理所有队列
type Manager struct {
	nodeCreationQueue   *NodeCreationQueue
	nodeDeletionQueue   *NodeDeletionQueue
	functionUpdateQueue *FunctionUpdateQueue
	nodeDeploymentQueue *NodeDeploymentQueue
	db                  *gorm.DB
	cfConfig            *config.CloudFunctionConfig
}

// NewManager 创建新的队列管理器
func NewManager(db *gorm.DB) *Manager {
	// 加载云函数配置
	cfConfig, err := config.LoadCloudFunctionConfig()
	if err != nil {
		// 如果加载失败，使用默认配置
		cfConfig = config.GetCloudFunctionConfig()
	}

	return &Manager{
		nodeCreationQueue:   NewNodeCreationQueue(100),   // 缓冲区大小为100
		nodeDeletionQueue:   NewNodeDeletionQueue(100),   // 缓冲区大小为100
		functionUpdateQueue: NewFunctionUpdateQueue(100), // 缓冲区大小为100
		nodeDeploymentQueue: NewNodeDeploymentQueue(100), // 缓冲区大小为100
		db:                  db,
		cfConfig:            cfConfig,
	}
}

// GetNodeCreationQueue 返回节点创建队列
func (m *Manager) GetNodeCreationQueue() *NodeCreationQueue {
	return m.nodeCreationQueue
}

// GetNodeDeletionQueue 返回节点删除队列
func (m *Manager) GetNodeDeletionQueue() *NodeDeletionQueue {
	return m.nodeDeletionQueue
}

// GetFunctionUpdateQueue 返回函数更新队列
func (m *Manager) GetFunctionUpdateQueue() *FunctionUpdateQueue {
	return m.functionUpdateQueue
}

// EnqueueNodeCreation 将节点创建消息加入队列（统一带任务ID，可为空）
func (m *Manager) EnqueueNodeCreation(node *model.SCFNode, taskID, itemID string) error {
	var packageID int64
	if node.PackageID != nil {
		packageID = int64(*node.PackageID)
	}

	msg := NodeCreationMessage{
		NodeID:         node.NodeID,
		CloudAccountID: node.CloudAccountID,
		PackageID:      packageID,
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
func (m *Manager) EnqueueNodeDeletion(node *model.SCFNode, taskID, itemID string) error {
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
func (m *Manager) EnqueueFunctionUpdate(node *model.SCFNode, zipFilePath string) error {
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
func (m *Manager) GetNodeDeploymentQueue() *NodeDeploymentQueue {
	return m.nodeDeploymentQueue
}

// GetCloudFunctionConfig 返回云函数配置
func (m *Manager) GetCloudFunctionConfig() *config.CloudFunctionConfig {
	return m.cfConfig
}

// EnqueueNodeDeployment 将节点部署消息加入队列
func (m *Manager) EnqueueNodeDeployment(msg *NodeDeploymentMessage) error {
	return m.nodeDeploymentQueue.Enqueue(*msg)
}

// Close 关闭所有队列
func (m *Manager) Close() {
	m.nodeCreationQueue.Close()
	m.nodeDeletionQueue.Close()
	m.functionUpdateQueue.Close()
	m.nodeDeploymentQueue.Close()
}
