package hostmetrics

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	PresenceReachable   = "reachable"
	PresenceUnreachable = "unreachable"
)

type hostAgentRecord struct {
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
	AgentID    string
	Hostname   string
	BootID     string
	OccurredAt time.Time
	EventID    string
}

type PresenceTransition struct {
	AgentID    string
	Hostname   string
	From       string
	To         string
	ObservedAt time.Time
}

type ObserveResult struct {
	Updated    bool
	Current    bool
	Transition *PresenceTransition
}

type RegistryAgent struct {
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
}

func NewRegistry(db *gorm.DB) *Registry { return &Registry{db: db} }

func (r *Registry) Observe(ctx context.Context, observation HostObservation) (ObserveResult, error) {
	if r == nil || r.db == nil {
		return ObserveResult{}, errors.New("host agent registry is unavailable")
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
		var current hostAgentRecord
		err := tx.Where("c_agent_id = ?", observation.AgentID).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			record := hostAgentRecord{
				AgentID: observation.AgentID, Hostname: observation.Hostname,
				BootID: observation.BootID, LastSeenAt: observation.OccurredAt,
				LastEventID: observation.EventID, Status: PresenceReachable,
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
				"c_hostname": observation.Hostname, "c_boot_id": observation.BootID,
				"c_last_seen_at": observation.OccurredAt, "c_last_event_id": observation.EventID,
				"c_status": PresenceReachable, "c_mtime": time.Now().UTC(),
			})
		if update.Error != nil {
			return update.Error
		}
		result.Updated = update.RowsAffected == 1
		result.Current = result.Updated
		if result.Updated && current.Status == PresenceUnreachable {
			result.Transition = &PresenceTransition{
				AgentID: observation.AgentID, Hostname: observation.Hostname,
				From: PresenceUnreachable, To: PresenceReachable,
				ObservedAt: observation.OccurredAt,
			}
		}
		return nil
	})
	return result, err
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

	r.mu.Lock()
	defer r.mu.Unlock()

	var transitions []PresenceTransition
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stale []hostAgentRecord
		if err := tx.Where("c_status = ? AND c_last_seen_at < ?", PresenceReachable, cutoff).
			Order("c_agent_id ASC").Find(&stale).Error; err != nil {
			return err
		}
		for _, record := range stale {
			update := tx.Model(&hostAgentRecord{}).
				Where("c_agent_id = ? AND c_status = ? AND c_last_seen_at = ?", record.AgentID, PresenceReachable, record.LastSeenAt).
				Updates(map[string]any{"c_status": PresenceUnreachable, "c_mtime": now})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected == 1 {
				transitions = append(transitions, PresenceTransition{
					AgentID: record.AgentID, Hostname: record.Hostname,
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
	if err := r.db.WithContext(ctx).Order("c_hostname ASC, c_agent_id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	now = now.UTC()
	out := make([]RegistryAgent, 0, len(records))
	for _, record := range records {
		stale := int64(now.Sub(record.LastSeenAt.UTC()) / time.Second)
		if stale < 0 {
			stale = 0
		}
		out = append(out, RegistryAgent{
			AgentID: record.AgentID, Hostname: record.Hostname, BootID: record.BootID,
			LastSeenAt: record.LastSeenAt.UTC(), LastEventID: record.LastEventID,
			Reachable: record.Status == PresenceReachable, StaleSeconds: stale,
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
