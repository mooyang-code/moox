package cloudnode

import (
	"time"

	cloudnodeconfig "github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
	cmap "github.com/orcaman/concurrent-map/v2"
)

// HeartbeatInfo 心跳信息（内存存储）
type HeartbeatInfo struct {
	NodeID          string
	NodeType        string
	SourceService   string
	LastHeartbeat   time.Time
	TotalHeartbeats int64
	Metadata        map[string]interface{}
}

// HeartbeatStore 心跳内存存储
// 使用 concurrent-map 存储节点字段，避免并发写入冲突
type HeartbeatStore struct {
	nodeType        cmap.ConcurrentMap[string, string]
	sourceService   cmap.ConcurrentMap[string, string]
	lastHeartbeat   cmap.ConcurrentMap[string, time.Time]
	totalHeartbeats cmap.ConcurrentMap[string, int64]
	metadata        cmap.ConcurrentMap[string, map[string]interface{}]
}

// NewHeartbeatStore 创建心跳存储实例
func NewHeartbeatStore() *HeartbeatStore {
	return &HeartbeatStore{
		nodeType:        cmap.New[string](),
		sourceService:   cmap.New[string](),
		lastHeartbeat:   cmap.New[time.Time](),
		totalHeartbeats: cmap.New[int64](),
		metadata:        cmap.New[map[string]interface{}](),
	}
}

// UpdateHeartbeat 更新心跳信息
// 如果节点不存在则创建，存在则更新
func (s *HeartbeatStore) UpdateHeartbeat(req *types.ReportHeartbeatRequest) {
	now := time.Now()
	if req.Timestamp != nil {
		now = *req.Timestamp
	}

	s.lastHeartbeat.Set(req.NodeID, now)
	if req.NodeType != "" {
		s.nodeType.Set(req.NodeID, req.NodeType)
	}
	if req.SourceService != "" {
		s.sourceService.Set(req.NodeID, req.SourceService)
	}
	s.totalHeartbeats.Upsert(req.NodeID, int64(1), func(exist bool, valueInMap int64, newValue int64) int64 {
		if exist {
			return valueInMap + 1
		}
		return newValue
	})
	if req.Metadata != nil {
		incoming := cloneHeartbeatMetadata(req.Metadata)
		s.metadata.Upsert(req.NodeID, incoming, func(exist bool, valueInMap map[string]interface{}, newValue map[string]interface{}) map[string]interface{} {
			if !exist {
				return newValue
			}
			return mergeHeartbeatMetadata(valueInMap, newValue)
		})
	}
}

// GetHeartbeat 获取指定节点的心跳信息
func (s *HeartbeatStore) GetHeartbeat(nodeID string) *HeartbeatInfo {
	lastHeartbeat, ok := s.lastHeartbeat.Get(nodeID)
	if !ok {
		return nil
	}
	return s.buildHeartbeatInfo(nodeID, lastHeartbeat)
}

// GetLastHeartbeat 获取节点最后心跳时间
func (s *HeartbeatStore) GetLastHeartbeat(nodeID string) *time.Time {
	info := s.GetHeartbeat(nodeID)
	if info == nil {
		return nil
	}
	return &info.LastHeartbeat
}

// DeleteHeartbeat 删除指定节点的心跳信息
func (s *HeartbeatStore) DeleteHeartbeat(nodeID string) {
	s.nodeType.Remove(nodeID)
	s.sourceService.Remove(nodeID)
	s.lastHeartbeat.Remove(nodeID)
	s.totalHeartbeats.Remove(nodeID)
	s.metadata.Remove(nodeID)
}

// IsNodeOnline 判断节点是否在线
// timeoutThreshold: 超时阈值（秒），0表示使用默认值
func (s *HeartbeatStore) IsNodeOnline(nodeID string, timeoutThreshold int) bool {
	info := s.GetHeartbeat(nodeID)
	if info == nil {
		return false
	}

	if timeoutThreshold <= 0 {
		timeoutThreshold = cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
	}

	elapsed := time.Since(info.LastHeartbeat)
	return elapsed < time.Duration(timeoutThreshold)*time.Second
}

// GetOnlineNodeIDs 获取所有在线节点ID列表
// 使用默认超时阈值判断
func (s *HeartbeatStore) GetOnlineNodeIDs() []string {
	defaultTimeout := cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
	return s.GetOnlineNodeIDsWithTimeout(defaultTimeout)
}

// GetOnlineNodeIDsWithTimeout 获取所有在线节点ID列表（指定超时阈值）
func (s *HeartbeatStore) GetOnlineNodeIDsWithTimeout(timeoutThreshold int) []string {
	var onlineNodes []string
	now := time.Now()
	threshold := time.Duration(timeoutThreshold) * time.Second

	s.lastHeartbeat.IterCb(func(key string, lastHeartbeat time.Time) {
		if now.Sub(lastHeartbeat) < threshold {
			onlineNodes = append(onlineNodes, key)
		}
	})

	return onlineNodes
}

// GetHeartbeatMap 获取所有心跳信息（用于批量查询）
func (s *HeartbeatStore) GetHeartbeatMap() map[string]*HeartbeatInfo {
	result := make(map[string]*HeartbeatInfo)
	s.lastHeartbeat.IterCb(func(key string, lastHeartbeat time.Time) {
		result[key] = s.buildHeartbeatInfo(key, lastHeartbeat)
	})
	return result
}

// GetNodeStatus 获取节点状态
func (s *HeartbeatStore) GetNodeStatus(nodeID string, timeoutThreshold int) types.NodeStatus {
	info := s.GetHeartbeat(nodeID)
	if info == nil {
		return types.NodeStatusOffline
	}

	if timeoutThreshold <= 0 {
		timeoutThreshold = cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
	}

	elapsed := time.Since(info.LastHeartbeat)
	if elapsed < time.Duration(timeoutThreshold)*time.Second {
		return types.NodeStatusOnline
	}
	return types.NodeStatusOffline
}

// FilterOnlineNodeIDs 从给定的节点ID列表中筛选在线节点
// nodeTimeouts: map[nodeID]timeoutThreshold，节点的超时阈值配置
func (s *HeartbeatStore) FilterOnlineNodeIDs(nodeIDs []string, nodeTimeouts map[string]int) []string {
	defaultTimeout := cloudnodeconfig.Get().Heartbeat.DefaultTimeoutThreshold
	now := time.Now()

	var onlineNodes []string
	for _, nodeID := range nodeIDs {
		info := s.GetHeartbeat(nodeID)
		if info == nil {
			continue
		}

		timeout := defaultTimeout
		if t, ok := nodeTimeouts[nodeID]; ok && t > 0 {
			timeout = t
		}

		if now.Sub(info.LastHeartbeat) < time.Duration(timeout)*time.Second {
			onlineNodes = append(onlineNodes, nodeID)
		}
	}

	return onlineNodes
}

// Count 获取存储的心跳记录数量
func (s *HeartbeatStore) Count() int {
	return s.lastHeartbeat.Count()
}

func cloneHeartbeatMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	clone := make(map[string]interface{}, len(metadata))
	for k, v := range metadata {
		clone[k] = v
	}
	return clone
}

func mergeHeartbeatMetadata(existing map[string]interface{}, incoming map[string]interface{}) map[string]interface{} {
	if len(existing) == 0 {
		return cloneHeartbeatMetadata(incoming)
	}
	merged := make(map[string]interface{}, len(existing)+len(incoming))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range incoming {
		merged[k] = v
	}
	return merged
}

func (s *HeartbeatStore) buildHeartbeatInfo(nodeID string, lastHeartbeat time.Time) *HeartbeatInfo {
	nodeType, _ := s.nodeType.Get(nodeID)
	sourceService, _ := s.sourceService.Get(nodeID)
	totalHeartbeats, _ := s.totalHeartbeats.Get(nodeID)
	metadata, _ := s.metadata.Get(nodeID)
	return &HeartbeatInfo{
		NodeID:          nodeID,
		NodeType:        nodeType,
		SourceService:   sourceService,
		LastHeartbeat:   lastHeartbeat,
		TotalHeartbeats: totalHeartbeats,
		Metadata:        cloneHeartbeatMetadata(metadata),
	}
}

// GetOnlineNodeIDs 由 ServiceImpl 委托调用
// 这是 OnlineNodeIDsProvider 接口的实现
func (s *ServiceImpl) GetOnlineNodeIDs() []string {
	if s.heartbeatStore == nil {
		return []string{}
	}
	return s.heartbeatStore.GetOnlineNodeIDs()
}
