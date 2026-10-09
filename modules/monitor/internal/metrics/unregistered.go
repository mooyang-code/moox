package metrics

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// UnregisteredWindow 是未登记上报方的保留时长：这段时间内没有再上报就不再列出。
	UnregisteredWindow = 10 * time.Minute
	// maxUnregisteredProducers 是最多记录的未登记上报方数量，防止异常上报撑大内存。
	maxUnregisteredProducers = 100
)

// UnregisteredProducer 是一个在上报运行指标、但没有登记部署的进程。
type UnregisteredProducer struct {
	// ServiceName 是上报的服务名（组件 ID），NodeID 是主机 ID。
	ServiceName, NodeID, InstanceID, Version string
	FirstSeenAt, LastSeenAt                  time.Time
}

// UnregisteredProducers 记录被拒收的上报方，供健康概览列出「未登记」的进程。只保存在内存中：Monitor 重启后，
// 这些进程下一次上报时会重新出现。
type UnregisteredProducers struct {
	mu    sync.Mutex
	items map[string]*UnregisteredProducer
}

// NewUnregisteredProducers 创建记录器。
func NewUnregisteredProducers() *UnregisteredProducers {
	return &UnregisteredProducers{items: map[string]*UnregisteredProducer{}}
}

// Record 记录一次被拒收的上报。
func (u *UnregisteredProducers) Record(serviceName, nodeID, instanceID, version string, at time.Time) {
	if u == nil {
		return
	}
	serviceName, nodeID, instanceID = strings.TrimSpace(serviceName), strings.TrimSpace(nodeID), strings.TrimSpace(instanceID)
	if serviceName == "" {
		return
	}
	at = at.UTC()
	key := serviceName + "\x00" + nodeID + "\x00" + instanceID
	u.mu.Lock()
	defer u.mu.Unlock()
	u.expire(at)
	if item, ok := u.items[key]; ok {
		if at.After(item.LastSeenAt) {
			item.LastSeenAt, item.Version = at, version
		}
		return
	}
	if len(u.items) >= maxUnregisteredProducers {
		return
	}
	u.items[key] = &UnregisteredProducer{ServiceName: serviceName, NodeID: nodeID, InstanceID: instanceID, Version: version, FirstSeenAt: at, LastSeenAt: at}
}

// List 返回最近 UnregisteredWindow 内上报过的未登记上报方，按主机和服务名排序。
func (u *UnregisteredProducers) List(now time.Time) []UnregisteredProducer {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.expire(now.UTC())
	out := make([]UnregisteredProducer, 0, len(u.items))
	for _, item := range u.items {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		if out[i].ServiceName != out[j].ServiceName {
			return out[i].ServiceName < out[j].ServiceName
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

// expire 删除超过保留时长的记录；调用方持有锁。
func (u *UnregisteredProducers) expire(now time.Time) {
	for key, item := range u.items {
		if now.Sub(item.LastSeenAt) > UnregisteredWindow {
			delete(u.items, key)
		}
	}
}
