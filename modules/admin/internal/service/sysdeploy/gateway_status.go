package sysdeploy

import (
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"google.golang.org/protobuf/proto"
)

// HostGatewayStatus 是网关最近一次拉取快照时在进程内记下的运行态：
// 约每 5 秒拉取一次，不落库；Admin 重启后由下一次拉取自然填回。
type HostGatewayStatus struct {
	HostID        string
	AppliedHash   string
	LastSeenAt    *time.Time
	RelaySessions []*pb.RelaySessionStatus
}

// GatewayStatusStore 在进程内存中保存各主机网关的运行态，由 GatewayControl 写入、SysDeploy.GetHostRoutes 读取。
type GatewayStatusStore struct {
	mu     sync.RWMutex
	states map[string]HostGatewayStatus
}

// NewGatewayStatusStore 创建空的网关运行态内存表。
func NewGatewayStatusStore() *GatewayStatusStore {
	return &GatewayStatusStore{states: map[string]HostGatewayStatus{}}
}

// Record 更新指定主机网关最近一次拉取快照的运行态。
func (s *GatewayStatusStore) Record(hostID, appliedHash string, seenAt time.Time, sessions []*pb.RelaySessionStatus) {
	if s == nil {
		return
	}
	at := seenAt.UTC()
	entry := HostGatewayStatus{
		HostID:        hostID,
		AppliedHash:   appliedHash,
		LastSeenAt:    &at,
		RelaySessions: cloneRelaySessions(sessions),
	}
	s.mu.Lock()
	s.states[hostID] = entry
	s.mu.Unlock()
}

// Get 读取指定主机网关的运行态副本。
func (s *GatewayStatusStore) Get(hostID string) (HostGatewayStatus, bool) {
	if s == nil {
		return HostGatewayStatus{}, false
	}
	s.mu.RLock()
	entry, ok := s.states[hostID]
	s.mu.RUnlock()
	if !ok {
		return HostGatewayStatus{}, false
	}
	if entry.LastSeenAt != nil {
		at := *entry.LastSeenAt
		entry.LastSeenAt = &at
	}
	entry.RelaySessions = cloneRelaySessions(entry.RelaySessions)
	return entry, true
}

func cloneRelaySessions(sessions []*pb.RelaySessionStatus) []*pb.RelaySessionStatus {
	if len(sessions) == 0 {
		return nil
	}
	out := make([]*pb.RelaySessionStatus, len(sessions))
	for i, item := range sessions {
		out[i] = proto.Clone(item).(*pb.RelaySessionStatus)
	}
	return out
}
