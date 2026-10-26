package enginehub

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/report"
)

// heartbeatPersistInterval 是同一引擎心跳落库的最小间隔：引擎每 10 秒一次心跳，离线判定按分钟计，不需要每次都写库。
const heartbeatPersistInterval = 30 * time.Second

// HeartbeatStore 保存最近一次被接受的心跳，供离线判定在管理端重启后继续使用。
type HeartbeatStore interface {
	SaveEngineHeartbeat(ctx context.Context, heartbeat store.EngineHeartbeat) error
	LastEngineHeartbeat(ctx context.Context) (store.EngineHeartbeat, bool, error)
}

// WithHeartbeatStore 让 hub 把最近心跳落库。
func WithHeartbeatStore(s HeartbeatStore) Option { return func(h *Hub) { h.heartbeats = s } }

// persistHeartbeat 在引擎变化或距上次落库超过间隔时保存心跳；失败只影响重启后的离线判定，不拒绝心跳。
func (h *Hub) persistHeartbeat(ctx context.Context, id domain.EngineIdentity, now time.Time) error {
	if h.heartbeats == nil {
		return nil
	}
	h.mu.Lock()
	due := h.persisted.EngineID != id.EngineID || h.persisted.BootID != id.BootID || now.Sub(h.persisted.At) >= heartbeatPersistInterval
	h.mu.Unlock()
	if !due {
		return nil
	}
	heartbeat := store.EngineHeartbeat{EngineID: id.EngineID, BootID: id.BootID, At: now}
	if err := h.heartbeats.SaveEngineHeartbeat(ctx, heartbeat); err != nil {
		return err
	}
	h.mu.Lock()
	h.persisted = heartbeat
	h.mu.Unlock()
	return nil
}

// lastHeartbeat 返回最近一次被接受的心跳：本进程收到过就用内存里的，否则读库（管理端重启后引擎一直没回来的情况）。
func (h *Hub) lastHeartbeat(ctx context.Context) (string, time.Time, error) {
	h.mu.Lock()
	state := h.engine
	h.mu.Unlock()
	if state != nil {
		return state.identity.EngineID, state.lastSeen, nil
	}
	if h.heartbeats == nil {
		return "", time.Time{}, nil
	}
	saved, ok, err := h.heartbeats.LastEngineHeartbeat(ctx)
	if err != nil || !ok {
		return "", time.Time{}, err
	}
	return saved.EngineID, saved.At, nil
}

// Presence 评估引擎在线状态：存在启用的因子任务才要求有引擎；按最近一次被接受的心跳是否在 2 分钟内判断，与 45 秒的租约无关。
func (h *Hub) Presence(ctx context.Context) (state int, engineID string, lastHeartbeat time.Time, err error) {
	sets, err := h.store.ListTasks(ctx)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	required := false
	for _, set := range sets {
		if set.Status == domain.TaskStatusEnabled {
			required = true
			break
		}
	}
	engineID, lastHeartbeat, err = h.lastHeartbeat(ctx)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	return report.EvaluatePresence(required, lastHeartbeat, h.now().UTC(), report.DefaultPresenceTimeout), engineID, lastHeartbeat, nil
}
