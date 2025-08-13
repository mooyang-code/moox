package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// SCFNodeService SCF云节点服务接口
type SCFNodeService interface {
	GetNodeList(ctx context.Context) ([]*model.SCFNode, error)
	GetNode(ctx context.Context, nodeID string) (*model.SCFNode, error)
	GetNodesByType(ctx context.Context, nodeType string) ([]*model.SCFNode, error)
	GetOnlineNodes(ctx context.Context) ([]*model.SCFNode, error)
	CreateNode(ctx context.Context, node *model.SCFNode, taskID, itemID string) (*model.SCFNode, error)
	UpdateNode(ctx context.Context, node *model.SCFNode) error
	RemoveNode(ctx context.Context, nodeID string, taskID, itemID string) error
	Heartbeat(ctx context.Context, nodeID string, currentLoad string) error
	UpdateNodeLoad(ctx context.Context, nodeID string, currentLoad string) error
	UpdateNodeFunction(ctx context.Context, nodeID string, zipFilePath string) error
	DeployToNode(ctx context.Context, nodeID string, zipFileBase64 string, fileName string, taskID string) error
}

type scfNodeServiceImpl struct {
	nodeDAO      dao.SCFNodeDAO
	queueManager *queue.QueueManager
}

// NewSCFNodeService 创建新的SCF节点服务实例
func NewSCFNodeService(db *gorm.DB) SCFNodeService {
	return &scfNodeServiceImpl{
		nodeDAO:      dao.NewSCFNodeDAO(db),
		queueManager: nil, // Will be set later via SetQueueManager
	}
}

// NewSCFNodeServiceWithQueue 创建新的SCF节点服务实例（带队列管理器）
func NewSCFNodeServiceWithQueue(db *gorm.DB, queueManager *queue.QueueManager) SCFNodeService {
	return &scfNodeServiceImpl{
		nodeDAO:      dao.NewSCFNodeDAO(db),
		queueManager: queueManager,
	}
}

func (s *scfNodeServiceImpl) GetNodeList(ctx context.Context) ([]*model.SCFNode, error) {
	nodes, err := s.nodeDAO.GetSCFNodeList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get node list: %w", err)
	}

	// 检查心跳超时的节点并更新状态
	for _, node := range nodes {
		if node.Status == model.NodeStatusOnline && node.LastHeartbeat != nil {
			// 如果超过5分钟没有心跳，认为节点离线
			if time.Since(*node.LastHeartbeat) > 5*time.Minute {
				node.Status = model.NodeStatusOffline
			}
		}
	}
	return nodes, nil
}

func (s *scfNodeServiceImpl) GetNode(ctx context.Context, nodeID string) (*model.SCFNode, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	if node == nil {
		return nil, fmt.Errorf("node not found")
	}

	// 检查心跳状态
	if node.Status == model.NodeStatusOnline && node.LastHeartbeat != nil {
		if time.Since(*node.LastHeartbeat) > 5*time.Minute {
			node.Status = model.NodeStatusOffline
		}
	}
	return node, nil
}

// CreateNode 创建节点（自动分配NodeID和Namespace，带任务ID，可为空）
func (s *scfNodeServiceImpl) CreateNode(ctx context.Context, node *model.SCFNode, taskID, itemID string) (*model.SCFNode, error) {
	// 验证必填字段
	if node.CloudAccountID == "" {
		return nil, fmt.Errorf("cloud account id is required")
	}
	if node.Region == "" {
		return nil, fmt.Errorf("region is required")
	}

	// 生成NodeID
	nodeID, err := s.generateNodeID(ctx, node.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to generate node id: %w", err)
	}
	node.NodeID = nodeID

	// 分配Namespace
	namespace, err := s.allocateNamespace(ctx, node.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate namespace: %w", err)
	}
	node.Namespace = namespace

	// 设置默认值 - 初始状态为维护中，等待云函数创建成功后再更新为在线
	if node.Status == 0 {
		node.Status = model.NodeStatusMaintenance
	}

	if node.NodeType == "" {
		node.NodeType = model.NodeTypeSCF
	}

	now := time.Now()
	node.LastHeartbeat = &now
	node.Invalid = model.InvalidNo

	// 验证各种JSON字段
	if err := s.validateJSONField(node.SupportedCollectors, "supported_collectors"); err != nil {
		return nil, err
	}
	if node.SupportedCollectors == "" {
		node.SupportedCollectors = "[]"
	}

	if err := s.validateJSONField(node.Capacity, "capacity"); err != nil {
		return nil, err
	}
	if node.Capacity == "" {
		node.Capacity = "{}"
	}

	if err := s.validateJSONField(node.CurrentLoad, "current_load"); err != nil {
		return nil, err
	}
	if node.CurrentLoad == "" {
		node.CurrentLoad = "{}"
	}

	if err := s.validateJSONField(node.Metadata, "metadata"); err != nil {
		return nil, err
	}
	if node.Metadata == "" {
		node.Metadata = "{}"
	}

	// 如果是SCF节点并且有队列管理器，则将创建任务加入队列
	if node.NodeType == model.NodeTypeSCF && s.queueManager != nil {
		err = s.queueManager.EnqueueNodeCreation(node, taskID, itemID)
		if err != nil {
			return nil, fmt.Errorf("failed to enqueue node creation: %w", err)
		}
		log.InfoContextf(ctx, "Successfully enqueued node creation for node %s,taskID:%s", node.NodeID, taskID)
	} else {
		return nil, fmt.Errorf("queue manager not available or node type is not SCF")
	}
	return node, nil
}

func (s *scfNodeServiceImpl) UpdateNode(ctx context.Context, node *model.SCFNode) error {
	if node.NodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	// 检查节点是否存在
	existing, err := s.nodeDAO.GetSCFNode(ctx, node.NodeID)
	if err != nil {
		return fmt.Errorf("failed to check existing node: %w", err)
	}

	if existing == nil {
		return fmt.Errorf("node not found")
	}

	// 验证各种JSON字段
	if err := s.validateJSONField(node.SupportedCollectors, "supported_collectors"); err != nil {
		return err
	}

	if err := s.validateJSONField(node.Capacity, "capacity"); err != nil {
		return err
	}

	if err := s.validateJSONField(node.CurrentLoad, "current_load"); err != nil {
		return err
	}

	if err := s.validateJSONField(node.Metadata, "metadata"); err != nil {
		return err
	}

	if err := s.nodeDAO.UpdateSCFNode(ctx, node); err != nil {
		return fmt.Errorf("failed to update node: %w", err)
	}
	return nil
}

func (s *scfNodeServiceImpl) RemoveNode(ctx context.Context, nodeID string, taskID, itemID string) error {
	log.InfoContextf(ctx, "RemoveNode called: nodeID=%s, taskID=%s, itemID=%s", nodeID, taskID, itemID)
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	// 获取节点信息
	node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found")
	}

	// 如果是SCF节点并且有队列管理器，则将删除任务加入队列
	log.InfoContextf(ctx, "Checking deletion conditions: NodeType=%s (SCF=%s), queueManager is nil: %v",
		node.NodeType, model.NodeTypeSCF, s.queueManager == nil)
	if node.NodeType == model.NodeTypeSCF && s.queueManager != nil {
		log.InfoContextf(ctx, "Enqueuing node deletion for node %s", node.NodeID)
		err := s.queueManager.EnqueueNodeDeletion(node, taskID, itemID)
		if err != nil {
			return fmt.Errorf("failed to enqueue node deletion: %w", err)
		}
		log.InfoContextf(ctx, "Successfully enqueued node deletion for node %s, taskID: %s, itemID: %s", node.NodeID, taskID, itemID)
	} else {
		// 如果不是SCF节点或没有队列管理器，直接删除数据库记录
		if err := s.nodeDAO.DeleteSCFNode(ctx, nodeID); err != nil {
			return fmt.Errorf("failed to remove node: %w", err)
		}
	}
	return nil
}

func (s *scfNodeServiceImpl) Heartbeat(ctx context.Context, nodeID string, currentLoad string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	// 验证currentLoad是否为有效的JSON
	if currentLoad != "" {
		if err := s.validateJSONField(currentLoad, "current_load"); err != nil {
			return err
		}
	}

	if err := s.nodeDAO.UpdateNodeHeartbeat(ctx, nodeID, currentLoad); err != nil {
		return fmt.Errorf("failed to update heartbeat: %w", err)
	}
	return nil
}

// GetNodesByType 根据节点类型获取节点列表
func (s *scfNodeServiceImpl) GetNodesByType(ctx context.Context, nodeType string) ([]*model.SCFNode, error) {
	if nodeType == "" {
		return nil, fmt.Errorf("node type is required")
	}

	nodes, err := s.nodeDAO.GetSCFNodesByType(ctx, nodeType)
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes by type: %w", err)
	}

	// 检查心跳超时的节点并更新状态
	for _, node := range nodes {
		if node.Status == model.NodeStatusOnline && node.LastHeartbeat != nil {
			if time.Since(*node.LastHeartbeat) > 5*time.Minute {
				node.Status = model.NodeStatusOffline
			}
		}
	}
	return nodes, nil
}

// GetOnlineNodes 获取所有在线节点
func (s *scfNodeServiceImpl) GetOnlineNodes(ctx context.Context) ([]*model.SCFNode, error) {
	nodes, err := s.nodeDAO.GetOnlineNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get online nodes: %w", err)
	}

	// 再次检查心跳超时
	var onlineNodes []*model.SCFNode
	for _, node := range nodes {
		if node.LastHeartbeat != nil && time.Since(*node.LastHeartbeat) <= 5*time.Minute {
			onlineNodes = append(onlineNodes, node)
		}
	}
	return onlineNodes, nil
}

// UpdateNodeLoad 更新节点负载信息
func (s *scfNodeServiceImpl) UpdateNodeLoad(ctx context.Context, nodeID string, currentLoad string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}

	if currentLoad == "" {
		return fmt.Errorf("current load is required")
	}

	// 验证currentLoad是否为有效的JSON
	if err := s.validateJSONField(currentLoad, "current_load"); err != nil {
		return err
	}

	if err := s.nodeDAO.UpdateNodeLoad(ctx, nodeID, currentLoad); err != nil {
		return fmt.Errorf("failed to update node load: %w", err)
	}
	return nil
}

// generateNodeID 生成节点ID
func (s *scfNodeServiceImpl) generateNodeID(ctx context.Context, region string) (string, error) {
	// 检查该region是否已有Master节点
	nodes, err := s.nodeDAO.GetSCFNodesByRegion(ctx, region)
	if err != nil {
		return "", fmt.Errorf("failed to check existing nodes: %w", err)
	}

	// 检查是否已有Master节点
	hasMaster := false
	for _, node := range nodes {
		if strings.HasPrefix(node.NodeID, "DataCollector-Master-") {
			hasMaster = true
			break
		}
	}

	// 生成Unix时间戳
	timestamp := time.Now().Unix()

	// 生成更随机的字符串
	randomStr := s.generateRandomString(4)

	// 根据是否已有Master节点决定节点类型
	var nodeID string
	if !hasMaster {
		nodeID = fmt.Sprintf("DataCollector-Master-%d-%s", timestamp, randomStr)
	} else {
		nodeID = fmt.Sprintf("DataCollector-General-%d-%s", timestamp, randomStr)
	}

	return nodeID, nil
}

// allocateNamespace 分配命名空间
func (s *scfNodeServiceImpl) allocateNamespace(ctx context.Context, region string) (string, error) {
	// 获取该region下的所有命名空间使用情况
	namespaceStats, err := s.nodeDAO.GetNamespaceStats(ctx, region)
	if err != nil {
		return "", fmt.Errorf("failed to get namespace stats: %w", err)
	}

	// 查找可用的命名空间
	// 规则：每个命名空间最多50个函数，每个region最多5个命名空间
	for i := 1; i <= 5; i++ {
		namespace := fmt.Sprintf("%s-%02d", region, i)
		count, exists := namespaceStats[namespace]

		// 如果命名空间不存在或函数数少于50个，则可以使用
		if !exists || count < 50 {
			return namespace, nil
		}
	}

	// 如果所有命名空间都满了，尝试找到函数数最少的命名空间
	minCount := 50
	selectedNamespace := ""
	for i := 1; i <= 5; i++ {
		namespace := fmt.Sprintf("%s-%02d", region, i)
		count, exists := namespaceStats[namespace]
		if !exists {
			// 如果命名空间不存在，直接使用
			return namespace, nil
		}
		if count < minCount {
			minCount = count
			selectedNamespace = namespace
		}
	}

	if selectedNamespace != "" {
		return selectedNamespace, nil
	}

	return "", fmt.Errorf("no available namespace in region %s", region)
}

// generateRandomString 生成随机字符串
func (s *scfNodeServiceImpl) generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// 使用crypto/rand生成更安全的随机数
	result := make([]byte, length)
	for i := range result {
		// 使用时间纳秒作为种子，确保每次都不同
		rand.Seed(time.Now().UnixNano() + int64(i))
		result[i] = charset[rand.Intn(len(charset))]
	}
	return string(result)
}

// UpdateNodeFunction 更新节点的云函数
func (s *scfNodeServiceImpl) UpdateNodeFunction(ctx context.Context, nodeID string, zipFilePath string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	if zipFilePath == "" {
		return fmt.Errorf("zip file path is required")
	}

	// 获取节点信息
	node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found")
	}

	// 只有SCF节点才能更新函数
	if node.NodeType != model.NodeTypeSCF {
		return fmt.Errorf("only SCF nodes can update function")
	}

	// 如果有队列管理器，则将更新任务加入队列
	if s.queueManager != nil {
		err := s.queueManager.EnqueueFunctionUpdate(node, zipFilePath)
		if err != nil {
			return fmt.Errorf("failed to enqueue function update: %w", err)
		}
		log.InfoContextf(ctx, "Successfully enqueued function update for node %s", node.NodeID)
	} else {
		return fmt.Errorf("queue manager not available")
	}

	return nil
}

// validateJSONField 验证JSON字段格式
func (s *scfNodeServiceImpl) validateJSONField(field string, fieldName string) error {
	if field == "" {
		return nil
	}

	var temp interface{}
	if err := json.Unmarshal([]byte(field), &temp); err != nil {
		return fmt.Errorf("invalid %s format: %w", fieldName, err)
	}
	return nil
}

// DeployToNode 部署云函数到节点
func (s *scfNodeServiceImpl) DeployToNode(ctx context.Context, nodeID string, zipFileBase64 string, fileName string, taskID string) error {
	if nodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	if zipFileBase64 == "" {
		return fmt.Errorf("zip file base64 is required")
	}
	if fileName == "" {
		return fmt.Errorf("file name is required")
	}

	// 获取节点信息
	node, err := s.nodeDAO.GetSCFNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	// 只有SCF节点才能部署云函数
	if node.NodeType != model.NodeTypeSCF {
		return fmt.Errorf("only SCF nodes can deploy functions")
	}

	// 检查节点状态
	if node.Status != model.NodeStatusOnline {
		log.WarnContextf(ctx, "Node %s is not online (status: %d), deployment may fail", nodeID, node.Status)
	}

	// 如果有队列管理器，则将部署任务加入队列
	if s.queueManager != nil {
		// 创建部署消息
		deploymentMessage := &queue.NodeDeploymentMessage{
			NodeID:         nodeID,
			CloudAccountID: node.CloudAccountID,
			Region:         node.Region,
			Namespace:      node.Namespace,
			ZipFileBase64:  zipFileBase64,
			FileName:       fileName,
			TaskID:         taskID,
			ItemID:         nodeID,
			EnqueueTime:    time.Now(),
		}
		
		err := s.queueManager.EnqueueNodeDeployment(deploymentMessage)
		if err != nil {
			return fmt.Errorf("failed to enqueue node deployment: %w", err)
		}
		log.InfoContextf(ctx, "Successfully enqueued deployment for node %s, taskID: %s, file: %s", 
			node.NodeID, taskID, fileName)
	} else {
		return fmt.Errorf("queue manager not available")
	}

	return nil
}
