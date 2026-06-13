package cloudnode

import (
	"time"

	cmap "github.com/orcaman/concurrent-map/v2"
)

// ProbeInfo 存储保活探测结果
type ProbeInfo struct {
	NodeID    string
	LastProbe time.Time
	Success   bool
}

// ProbeStore 保活探测内存存储
// 使用 concurrent-map 避免并发写入冲突
type ProbeStore struct {
	lastProbeTime    cmap.ConcurrentMap[string, time.Time]
	lastProbeSuccess cmap.ConcurrentMap[string, bool]
}

// NewProbeStore 创建保活探测存储实例
func NewProbeStore() *ProbeStore {
	return &ProbeStore{
		lastProbeTime:    cmap.New[time.Time](),
		lastProbeSuccess: cmap.New[bool](),
	}
}

// UpdateProbe 更新节点探测结果
func (s *ProbeStore) UpdateProbe(nodeID string, when time.Time, success bool) {
	if nodeID == "" {
		return
	}
	s.lastProbeTime.Set(nodeID, when)
	s.lastProbeSuccess.Set(nodeID, success)
}

// GetProbeInfo 获取指定节点的探测结果
func (s *ProbeStore) GetProbeInfo(nodeID string) *ProbeInfo {
	lastProbe, ok := s.lastProbeTime.Get(nodeID)
	if !ok {
		return nil
	}
	success, _ := s.lastProbeSuccess.Get(nodeID)
	return &ProbeInfo{
		NodeID:    nodeID,
		LastProbe: lastProbe,
		Success:   success,
	}
}

// Count 获取已记录的探测数量
func (s *ProbeStore) Count() int {
	return s.lastProbeTime.Count()
}
