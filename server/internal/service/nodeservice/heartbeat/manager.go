package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager 心跳管理器
type Manager struct {
	db               *gorm.DB
	NodeStates       sync.Map // 导出以供service访问
	checkInterval    time.Duration
	timeoutDuration  time.Duration
	nodeService      logic.SCFNodeService
	heartbeatService logic.HeartbeatService
	getCloudProvider func(accountID string) provider.Client // 根据账户ID获取云客户端
	mooxServiceURL   string
}

// NewManager 创建心跳管理器
// getCloudProvider: 根据账户ID获取对应的云客户端的回调函数
func NewManager(db *gorm.DB, nodeService logic.SCFNodeService, heartbeatService logic.HeartbeatService, mooxServiceURL string, getCloudProvider func(string) provider.Client) *Manager {
	return &Manager{
		db:               db,
		checkInterval:    5 * time.Second,
		timeoutDuration:  11 * time.Second,
		nodeService:      nodeService,
		heartbeatService: heartbeatService,
		getCloudProvider: getCloudProvider,
		mooxServiceURL:   mooxServiceURL,
	}
}

// Start 启动心跳管理器
func (m *Manager) Start(ctx context.Context) {
	log.InfoContext(ctx, "[heartbeat.Manager] Starting heartbeat manager")

	// 启动节点健康监控
	go m.monitorNodeHealth(ctx)

	// 初始化所有已部署但未初始化的节点
	go m.initializeNewNodes(ctx)
}

// HandleHeartbeat 处理心跳
func (m *Manager) HandleHeartbeat(ctx context.Context, data HeartbeatData) (*HeartbeatResponse, error) {
	now := time.Now()

	// 更新内存中的节点状态
	state := NodeState{
		NodeID:        data.NodeID,
		LastHeartbeat: now,
		Status:        NodeStatusOnline,
		RunningTasks:  len(data.RunningTasks),
	}
	m.NodeStates.Store(data.NodeID, state)

	// 异步更新数据库
	go m.updateNodeHeartbeat(ctx, data)

	// 构建响应
	resp := &HeartbeatResponse{
		Success:   true,
		Timestamp: now,
	}

	return resp, nil
}

// initializeNewNodes 初始化新部署的节点
func (m *Manager) initializeNewNodes(ctx context.Context) {
	// 等待一段时间，让服务完全启动
	time.Sleep(5 * time.Second)

	// 使用 nodeService 获取所有已部署的节点
	nodes, err := m.nodeService.GetNodeList(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[heartbeat.Manager] Failed to list nodes: %v", err)
		return
	}

	// 检查离线的节点
	for _, node := range nodes {
		// 跳过已经在线的节点
		if node.Status == model.NodeStatusOnline {
			continue
		}

		// 发送心跳探测请求
		log.InfoContextf(ctx, "[heartbeat.Manager] Initializing  node: %s", node.NodeID)
		if err := m.sendProbe(ctx, node.NodeID); err != nil {
			log.ErrorContextf(ctx, "[heartbeat.Manager] Failed to initialize node %s: %v", node.NodeID, err)
		}
	}
}

// monitorNodeHealth 监控节点健康状态
func (m *Manager) monitorNodeHealth(ctx context.Context) {
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.checkNodes(ctx)
		case <-ctx.Done():
			log.InfoContext(ctx, "[heartbeat.Manager] Stopping node health monitor")
			return
		}
	}
}

// checkNodes 检查所有节点状态
func (m *Manager) checkNodes(ctx context.Context) {
	now := time.Now()

	m.NodeStates.Range(func(key, value interface{}) bool {
		nodeID := key.(string)
		state := value.(NodeState)

		elapsed := now.Sub(state.LastHeartbeat)

		if elapsed > m.timeoutDuration && state.Status == NodeStatusOnline {
			// 节点心跳超时
			log.WarnContextf(ctx, "[heartbeat.Manager] Node %s heartbeat timeout, elapsed: %v",
				nodeID, elapsed)

			state.Status = NodeStatusOffline
			m.NodeStates.Store(nodeID, state)

			// 使用心跳服务更新数据库状态
			m.heartbeatService.UpdateNodeStatus(ctx, nodeID, int(NodeStatusOffline))

			// 异步处理节点离线
			go m.handleNodeOffline(ctx, nodeID)
		}

		return true
	})
}

// handleNodeOffline 处理节点离线
func (m *Manager) handleNodeOffline(ctx context.Context, nodeID string) {
	log.ErrorContextf(ctx, "[heartbeat.Manager] Handling offline node: %s", nodeID)

	// 等待一个心跳周期，再次确认
	time.Sleep(5 * time.Second)

	// 再次检查节点状态
	if val, exists := m.NodeStates.Load(nodeID); exists {
		state := val.(NodeState)
		if time.Since(state.LastHeartbeat) < m.timeoutDuration {
			// 节点已恢复
			log.InfoContextf(ctx, "[heartbeat.Manager] Node %s recovered", nodeID)
			return
		}
	}

	// 节点确实离线，开始恢复流程
	log.ErrorContextf(ctx, "[heartbeat.Manager] Node %s confirmed offline, attempting initialization", nodeID)

	// 发送初始化请求
	if err := m.sendProbe(ctx, nodeID); err != nil {
		log.ErrorContextf(ctx, "[heartbeat.Manager] Failed to initialize node %s: %v", nodeID, err)

		// 节点无法探活，标记节点需要重新部署
		log.ErrorContextf(ctx, "[heartbeat.Manager] Node %s probe failed, may need redeployment", nodeID)
		// 使用 nodeService 更新节点状态
		if node, err := m.nodeService.GetNode(ctx, nodeID); err == nil && node != nil {
			node.Status = model.NodeStatusOffline
			m.nodeService.UpdateNode(ctx, node)
		}
	}
}

// sendProbe 发送探测请求（初始化请求）
func (m *Manager) sendProbe(ctx context.Context, nodeID string) error {
	// 使用 nodeService 获取节点信息
	node, err := m.nodeService.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node info: %w", err)
	}
	if node == nil {
		return fmt.Errorf("node not found")
	}

	// 根据节点的云账户ID获取对应的云客户端
	cloudProvider := m.getCloudProvider(node.CloudAccountID)
	if cloudProvider == nil {
		return fmt.Errorf("cloud provider not found for account: %s", node.CloudAccountID)
	}

	// 构建初始化请求
	initRequest := InitRequest{
		NodeID:  nodeID,
		MooxURL: m.mooxServiceURL,
	}

	// 调用云函数的初始化接口
	invokeReq := &provider.InvokeFunctionRequest{
		FunctionName: node.NodeID,
		Namespace:    node.Namespace,
		EventData: map[string]interface{}{
			"_action": "init", // 使用_action避免tencent SCF覆盖
			"data":    initRequest,
		},
		InvokeType: provider.InvokeTypeSync,
	}
	log.InfoContextf(ctx, "[heartbeat.Manager] Sending init probe to node %s (account: %s)", nodeID, node.CloudAccountID)

	resp, err := cloudProvider.InvokeFunction(ctx, invokeReq)
	if err != nil {
		return fmt.Errorf("invoke probe failed: %w", err)
	}

	// 检查探测结果
	if resp.StatusCode == 0 || resp.StatusCode == 200 {
		log.InfoContextf(ctx, "[heartbeat.Manager] Node %s initialization successful", nodeID)
		return nil
	}
	return fmt.Errorf("probe returned status %d: %s", resp.StatusCode, resp.ErrorMessage)
}

// updateNodeHeartbeat 更新节点心跳
func (m *Manager) updateNodeHeartbeat(ctx context.Context, data HeartbeatData) {
	// 构建任务摘要
	taskSummary := map[string]interface{}{
		"count": len(data.RunningTasks),
		"tasks": data.RunningTasks,
	}

	metricsJSON, _ := json.Marshal(taskSummary)

	heartbeat := &model.NodeHeartbeat{
		NodeID:        data.NodeID,
		LastHeartbeat: data.Timestamp,
		Status:        int(NodeStatusOnline),
		Metrics:       string(metricsJSON),
	}

	// 使用心跳服务更新心跳记录
	if err := m.heartbeatService.UpdateHeartbeat(ctx, heartbeat); err != nil {
		log.ErrorContextf(ctx, "[heartbeat.Manager] Failed to update heartbeat for node %s: %v",
			data.NodeID, err)
	}
}

// OnNodeDeployed 节点部署完成回调
func (m *Manager) OnNodeDeployed(ctx context.Context, nodeID string) {
	log.InfoContextf(ctx, "[heartbeat.Manager] Node %s deployed, sending initialization", nodeID)

	// 延迟一秒，等待云函数完全启动
	time.Sleep(1 * time.Second)

	// 发送初始化请求
	if err := m.sendProbe(ctx, nodeID); err != nil {
		log.ErrorContextf(ctx, "[heartbeat.Manager] Failed to initialize newly deployed node %s: %v", nodeID, err)
	}
}
