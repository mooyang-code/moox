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

type hostAgentRecord struct {
	HostID      string    `gorm:"column:c_host_id"`
	AgentID     string    `gorm:"column:c_agent_id;primaryKey"`
	Hostname    string    `gorm:"column:c_hostname"`
	BootID      string    `gorm:"column:c_boot_id"`
	LastSeenAt  time.Time `gorm:"column:c_last_seen_at"`
	LastEventID string    `gorm:"column:c_last_event_id"`
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

type ObserveResult struct {
	Updated bool
	Current bool
	AgentID string
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

// Registry 只持久化各采集器最近一次上报的时间，快照历史在 t_monitor_host_samples（SampleStore）里；
// 这张小表足以让 Monitor 重启后知道每台主机最后一次上报是什么时候。是否在线只按“距最近一次上报多久”判断，不另存状态；
// 失联与恢复的告警由主机类检查按这个时间产出（见 bootstrap 的 hostCheckItems）。
type Registry struct {
	db *gorm.DB
	mu sync.Mutex
}

func NewRegistry(db *gorm.DB) *Registry { return &Registry{db: db} }

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
				LastEventID: observation.EventID,
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			result.Updated = true
			result.Current = true
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
				"c_mtime": time.Now().UTC(),
			})
		if update.Error != nil {
			return update.Error
		}
		result.Updated = update.RowsAffected == 1
		result.Current = result.Updated
		return nil
	})
	return result, err
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
			Reachable: now.Sub(record.LastSeenAt) <= DefaultHostStaleAfter, StaleSeconds: stale,
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
