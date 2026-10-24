package hostmetrics

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

const (
	PresenceReachable   = "reachable"
	PresenceUnreachable = "unreachable"
)

type hostAgentRecord struct {
	HostID      string    `gorm:"column:c_host_id"`
	AgentID     string    `gorm:"column:c_agent_id;primaryKey"`
	Hostname    string    `gorm:"column:c_hostname"`
	BootID      string    `gorm:"column:c_boot_id"`
	LastSeenAt  time.Time `gorm:"column:c_last_seen_at"`
	LastEventID string    `gorm:"column:c_last_event_id"`
	Status      string    `gorm:"column:c_status"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (hostAgentRecord) TableName() string { return "t_monitor_host_agents" }

type HostObservation struct {
	HostID     string
	AgentID    string
	Hostname   string
	BootID     string
	OccurredAt time.Time
	EventID    string
}

type PresenceTransition struct {
	HostID     string
	AgentID    string
	Hostname   string
	From       string
	To         string
	ObservedAt time.Time
}

type ObserveResult struct {
	Updated    bool
	Current    bool
	AgentID    string
	Transition *PresenceTransition
}

type RegistryAgent struct {
	HostID       string
	AgentID      string
	Hostname     string
	BootID       string
	LastSeenAt   time.Time
	LastEventID  string
	Reachable    bool
	StaleSeconds int64
}

// Registry persists only host presence. Full snapshots and history remain in
// Storage; this small SQLite table is enough to survive Monitor restarts.
type Registry struct {
	db *gorm.DB
	mu sync.Mutex
	// registered 返回当前登记的主机集合；返回 nil 表示拓扑还未知，此时不按登记过滤。
	registered func(context.Context) (map[string]bool, error)
}

func NewRegistry(db *gorm.DB) *Registry { return &Registry{db: db} }

// SetRegisteredHosts 接入主机登记：只有登记主机上最近上报的采集器才产生上下线事件和通知，
// 旧采集器超时、没有 host_id 或未登记的采集器只更新状态，不产生事件。
func (r *Registry) SetRegisteredHosts(provider func(context.Context) (map[string]bool, error)) {
	r.registered = provider
}

// registeredHosts 读取登记主机集合；读取失败按“拓扑未知”处理（不过滤），不能因此丢掉采集事件或漏发告警。
func (r *Registry) registeredHosts(ctx context.Context) map[string]bool {
	if r.registered == nil {
		return nil
	}
	hosts, err := r.registered(ctx)
	if err != nil {
		return nil
	}
	return hosts
}

// notifiable 判断某个采集器的上下线是否要产生事件：登记主机上、且是该主机最近上报的采集器。
func notifiable(registered map[string]bool, hostID, agentID string, lastSeen time.Time, others []hostAgentRecord) bool {
	if registered == nil {
		return true
	}
	if hostID == "" || !registered[hostID] {
		return false
	}
	for _, other := range others {
		if other.HostID != hostID || other.AgentID == agentID {
			continue
		}
		if other.LastSeenAt.After(lastSeen) || other.LastSeenAt.Equal(lastSeen) && other.AgentID > agentID {
			return false
		}
	}
	return true
}

func (r *Registry) Observe(ctx context.Context, observation HostObservation) (ObserveResult, error) {
	if r == nil || r.db == nil {
		return ObserveResult{}, errors.New("host agent registry is unavailable")
	}
	observation.HostID = strings.TrimSpace(observation.HostID)
	if observation.HostID != "" && !servicecatalog.ValidHostID(observation.HostID) {
		return ObserveResult{}, errors.New("invalid deployment host identity")
	}
	observation.AgentID = strings.TrimSpace(observation.AgentID)
	observation.Hostname = strings.TrimSpace(observation.Hostname)
	observation.BootID = strings.TrimSpace(observation.BootID)
	observation.EventID = strings.TrimSpace(observation.EventID)
	observation.OccurredAt = observation.OccurredAt.UTC()
	if observation.AgentID == "" || observation.Hostname == "" || observation.BootID == "" || observation.EventID == "" || observation.OccurredAt.IsZero() {
		return ObserveResult{}, errors.New("host observation is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return ObserveResult{}, err
	}

	registered := r.registeredHosts(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()

	var result ObserveResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result.AgentID = observation.AgentID
		var current hostAgentRecord
		err := tx.Where("c_agent_id = ?", observation.AgentID).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			record := hostAgentRecord{
				HostID: observation.HostID, AgentID: observation.AgentID, Hostname: observation.Hostname,
				BootID: observation.BootID, LastSeenAt: observation.OccurredAt,
				LastEventID: observation.EventID, Status: PresenceReachable,
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			result.Updated = true
			result.Current = true
			// 同一台登记主机上旧采集器已失联并发过通知，新采集器开始上报就是这台主机恢复了：
			// 恢复事件沿用旧采集器的 ID，与失联通知是同一条告警。
			result.Transition = r.replacementRecovery(tx, registered, observation)
			return nil
		}
		if err != nil {
			return err
		}
		if !observation.OccurredAt.After(current.LastSeenAt) {
			result.Current = observation.OccurredAt.Equal(current.LastSeenAt) && observation.EventID == current.LastEventID
			return nil
		}
		update := tx.Model(&hostAgentRecord{}).
			Where("c_agent_id = ? AND c_last_seen_at < ?", observation.AgentID, observation.OccurredAt).
			Updates(map[string]any{
				"c_host_id": observation.HostID, "c_hostname": observation.Hostname, "c_boot_id": observation.BootID,
				"c_last_seen_at": observation.OccurredAt, "c_last_event_id": observation.EventID,
				"c_status": PresenceReachable, "c_mtime": time.Now().UTC(),
			})
		if update.Error != nil {
			return update.Error
		}
		result.Updated = update.RowsAffected == 1
		result.Current = result.Updated
		if result.Updated && current.Status == PresenceUnreachable && r.recoveryNotifiable(tx, registered, observation) {
			result.Transition = &PresenceTransition{
				HostID: observation.HostID, AgentID: observation.AgentID, Hostname: observation.Hostname,
				From: PresenceUnreachable, To: PresenceReachable,
				ObservedAt: observation.OccurredAt,
			}
		}
		return nil
	})
	return result, err
}

// recoveryNotifiable 判断恢复是否要通知：登记主机上的采集器才通知；同一主机另有采集器仍在线时，主机从未真正失联
// （这个采集器是被取代的旧采集器），不通知。
func (r *Registry) recoveryNotifiable(tx *gorm.DB, registered map[string]bool, observation HostObservation) bool {
	if registered == nil {
		return true
	}
	if observation.HostID == "" || !registered[observation.HostID] {
		return false
	}
	var other int64
	if err := tx.Model(&hostAgentRecord{}).
		Where("c_host_id = ? AND c_agent_id <> ? AND c_status = ?", observation.HostID, observation.AgentID, PresenceReachable).
		Count(&other).Error; err != nil {
		return true
	}
	return other == 0
}

// replacementRecovery 在新采集器首次上报时判断是否要补发主机恢复：登记主机上没有任何在线采集器，
// 且最近上报的那个旧采集器处于失联状态（它的失联已经通知过）。
func (r *Registry) replacementRecovery(tx *gorm.DB, registered map[string]bool, observation HostObservation) *PresenceTransition {
	if registered == nil || observation.HostID == "" || !registered[observation.HostID] {
		return nil
	}
	var reachable int64
	if err := tx.Model(&hostAgentRecord{}).
		Where("c_host_id = ? AND c_agent_id <> ? AND c_status = ?", observation.HostID, observation.AgentID, PresenceReachable).
		Count(&reachable).Error; err != nil || reachable > 0 {
		return nil
	}
	var prior hostAgentRecord
	if err := tx.Where("c_host_id = ? AND c_agent_id <> ? AND c_status = ?", observation.HostID, observation.AgentID, PresenceUnreachable).
		Order("c_last_seen_at DESC, c_agent_id DESC").First(&prior).Error; err != nil {
		return nil
	}
	return &PresenceTransition{
		HostID: observation.HostID, AgentID: prior.AgentID, Hostname: observation.Hostname,
		From: PresenceUnreachable, To: PresenceReachable, ObservedAt: observation.OccurredAt,
	}
}

// HostHasOtherReachableAgent 返回这台主机上除 agentID 外是否还有在线的采集器。
func (r *Registry) HostHasOtherReachableAgent(ctx context.Context, hostID, agentID string) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("host agent registry is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	err := r.db.WithContext(ctx).Model(&hostAgentRecord{}).
		Where("c_host_id = ? AND c_agent_id <> ? AND c_status = ?", hostID, agentID, PresenceReachable).Count(&count).Error
	return count > 0, err
}

// Status 返回 agent 当前的在线状态；未登记时返回空串。
func (r *Registry) Status(ctx context.Context, agentID string) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("host agent registry is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var record hostAgentRecord
	err := r.db.WithContext(ctx).Where("c_agent_id = ?", agentID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return record.Status, nil
}

func (r *Registry) MarkUnreachable(ctx context.Context, now time.Time, staleAfter time.Duration) ([]PresenceTransition, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("host agent registry is unavailable")
	}
	if staleAfter <= 0 {
		return nil, errors.New("host agent stale threshold must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now = now.UTC()
	cutoff := now.Add(-staleAfter)
	registered := r.registeredHosts(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()

	var transitions []PresenceTransition
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stale []hostAgentRecord
		if err := tx.Where("c_status = ? AND c_last_seen_at < ?", PresenceReachable, cutoff).
			Order("c_agent_id ASC").Find(&stale).Error; err != nil {
			return err
		}
		var all []hostAgentRecord
		if registered != nil {
			if err := tx.Limit(1501).Find(&all).Error; err != nil {
				return err
			}
		}
		for _, record := range stale {
			update := tx.Model(&hostAgentRecord{}).
				Where("c_agent_id = ? AND c_status = ? AND c_last_seen_at = ?", record.AgentID, PresenceReachable, record.LastSeenAt).
				Updates(map[string]any{"c_status": PresenceUnreachable, "c_mtime": now})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected == 1 && notifiable(registered, record.HostID, record.AgentID, record.LastSeenAt, all) {
				transitions = append(transitions, PresenceTransition{
					HostID: record.HostID, AgentID: record.AgentID, Hostname: record.Hostname,
					From: PresenceReachable, To: PresenceUnreachable,
					ObservedAt: now,
				})
			}
		}
		return nil
	})
	return transitions, err
}

func (r *Registry) List(ctx context.Context, now time.Time) ([]RegistryAgent, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("host agent registry is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var records []hostAgentRecord
	if err := r.db.WithContext(ctx).Order("c_hostname ASC, c_agent_id ASC").Limit(1501).Find(&records).Error; err != nil {
		return nil, err
	}
	if len(records) > 1500 {
		return nil, errors.New("host registry exceeds 1500 agents")
	}
	now = now.UTC()
	out := make([]RegistryAgent, 0, len(records))
	for _, record := range records {
		stale := int64(now.Sub(record.LastSeenAt.UTC()) / time.Second)
		if stale < 0 {
			stale = 0
		}
		out = append(out, RegistryAgent{
			HostID: record.HostID, AgentID: record.AgentID, Hostname: record.Hostname, BootID: record.BootID,
			LastSeenAt: record.LastSeenAt.UTC(), LastEventID: record.LastEventID,
			Reachable: record.Status == PresenceReachable && now.Sub(record.LastSeenAt) <= DefaultHostStaleAfter, StaleSeconds: stale,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out, nil
}
