package hostmetrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
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

type hostAgentAliasRecord struct {
	AliasID   string    `gorm:"column:c_alias_id;primaryKey"`
	AgentID   string    `gorm:"column:c_agent_id"`
	CreatedAt time.Time `gorm:"column:c_created_at"`
}

func (hostAgentAliasRecord) TableName() string { return "t_monitor_host_agent_aliases" }

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
	AgentID    string
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

// MigrateLegacyIDs assigns compact IDs to rows created by older releases.
// The old value is kept in the alias table so Storage history and queued
// UUID events remain readable during a rolling HostAgent upgrade.
func (r *Registry) MigrateLegacyIDs(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("host agent registry is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var records []hostAgentRecord
		if err := tx.Order("c_agent_id ASC").Find(&records).Error; err != nil {
			return err
		}
		for _, record := range records {
			if hostmetricpb.IsAgentID(record.AgentID) {
				continue
			}
			compact, mappingErr := hostmetricpb.CompactAgentIDForLegacy(record.AgentID)
			if mappingErr != nil {
				// Non-UUID values can exist in hand-created pre-release rows. Keep
				// those rows migratable, but all UUID migrations must be deterministic
				// so HostAgent and Monitor converge on the same identity.
				for attempt := 0; attempt < 32; attempt++ {
					candidate, err := hostmetricpb.NewAgentID()
					if err != nil {
						return err
					}
					var count int64
					if err := tx.Model(&hostAgentRecord{}).Where("c_agent_id = ?", candidate).Count(&count).Error; err != nil {
						return err
					}
					if count == 0 {
						compact = candidate
						break
					}
				}
				if compact == "" {
					return errors.New("unable to allocate a unique compact host agent id")
				}
			}
			var existing hostAgentRecord
			if err := tx.Where("c_agent_id = ?", compact).First(&existing).Error; err == nil {
				return fmt.Errorf("legacy host agent id %q maps to occupied compact id %q (hostname %q)", record.AgentID, compact, existing.Hostname)
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var aliasCount int64
			if err := tx.Model(&hostAgentAliasRecord{}).Where("c_alias_id = ? OR c_agent_id = ?", compact, compact).Count(&aliasCount).Error; err != nil {
				return err
			}
			if aliasCount > 0 {
				return fmt.Errorf("legacy host agent id %q maps to aliased compact id %q", record.AgentID, compact)
			}
			if err := tx.Model(&hostAgentRecord{}).Where("c_agent_id = ?", record.AgentID).Update("c_agent_id", compact).Error; err != nil {
				return err
			}
			if err := ensureHostAgentAlias(tx, record.AgentID, compact); err != nil {
				return err
			}
			if err := migrateAlertIdentity(tx, record.AgentID, compact); err != nil {
				return err
			}
		}
		return nil
	})
}

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
		canonicalID, err := r.resolveOrAssignCanonicalID(tx, observation.AgentID, observation.Hostname)
		if err != nil {
			return err
		}
		result.AgentID = canonicalID
		observation.AgentID = canonicalID
		var current hostAgentRecord
		err = tx.Where("c_agent_id = ?", canonicalID).First(&current).Error
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

// resolveOrAssignCanonicalID maps queued UUID observations and pre-existing
// rows to the new four-character identity. Hostname is only used during the
// migration window; once an alias is stored, every old event follows it.
func (r *Registry) resolveOrAssignCanonicalID(tx *gorm.DB, incomingID, hostname string) (string, error) {
	var alias hostAgentAliasRecord
	if err := tx.Where("c_alias_id = ?", incomingID).First(&alias).Error; err == nil {
		return alias.AgentID, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	if hostmetricpb.IsLegacyAgentID(incomingID) {
		compact, err := hostmetricpb.CompactAgentIDForLegacy(incomingID)
		if err != nil {
			return "", err
		}
		var compactRecord hostAgentRecord
		compactErr := tx.Where("c_agent_id = ?", compact).First(&compactRecord).Error
		if compactErr != nil && !errors.Is(compactErr, gorm.ErrRecordNotFound) {
			return "", compactErr
		}
		var legacyRecord hostAgentRecord
		legacyErr := tx.Where("c_agent_id = ?", incomingID).First(&legacyRecord).Error
		if legacyErr != nil && !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
			return "", legacyErr
		}
		if compactErr == nil && legacyErr == nil {
			return "", fmt.Errorf("legacy host agent id %q and compact id %q are both registered", incomingID, compact)
		}
		if legacyErr == nil {
			if err := tx.Model(&hostAgentRecord{}).Where("c_agent_id = ?", incomingID).Update("c_agent_id", compact).Error; err != nil {
				return "", err
			}
			if err := migrateAlertIdentity(tx, incomingID, compact); err != nil {
				return "", err
			}
		}
		if err := ensureHostAgentAlias(tx, incomingID, compact); err != nil {
			return "", err
		}
		return compact, nil
	}
	if hostmetricpb.IsAgentID(incomingID) {
		var legacy hostAgentRecord
		err := tx.Where("c_hostname = ? AND c_agent_id <> ?", hostname, incomingID).
			Order("c_mtime ASC").First(&legacy).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return incomingID, nil
		}
		if err != nil {
			return "", err
		}
		if hostmetricpb.IsAgentID(legacy.AgentID) {
			return incomingID, nil
		}
		if err := tx.Model(&hostAgentRecord{}).Where("c_agent_id = ?", legacy.AgentID).
			Update("c_agent_id", incomingID).Error; err != nil {
			return "", err
		}
		if err := ensureHostAgentAlias(tx, legacy.AgentID, incomingID); err != nil {
			return "", err
		}
		if err := migrateAlertIdentity(tx, legacy.AgentID, incomingID); err != nil {
			return "", err
		}
	}
	return incomingID, nil
}

func ensureHostAgentAlias(tx *gorm.DB, aliasID, agentID string) error {
	var existing hostAgentAliasRecord
	err := tx.Where("c_alias_id = ?", aliasID).First(&existing).Error
	if err == nil {
		if existing.AgentID != agentID {
			return fmt.Errorf("host agent alias %q already points to %q", aliasID, existing.AgentID)
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&hostAgentAliasRecord{AliasID: aliasID, AgentID: agentID, CreatedAt: time.Now().UTC()}).Error
}

func migrateAlertIdentity(tx *gorm.DB, oldID, newID string) error {
	oldPrefix, newPrefix := HostRulePrefix+oldID+":", HostRulePrefix+newID+":"
	for _, table := range []string{"t_monitor_alert_rules", "t_monitor_alert_states", "t_monitor_alert_events"} {
		if !tx.Migrator().HasTable(table) {
			continue
		}
		if err := tx.Exec("UPDATE "+table+" SET c_check_id = REPLACE(c_check_id, ?, ?) WHERE instr(c_check_id, ?) > 0", oldPrefix, newPrefix, oldPrefix).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable("t_monitor_alert_states") {
		if err := tx.Exec("UPDATE t_monitor_alert_states SET c_dedupe_key = REPLACE(c_dedupe_key, ?, ?) WHERE instr(c_dedupe_key, ?) > 0", oldID, newID, oldID).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable("t_monitor_alert_rules") {
		if err := tx.Exec("UPDATE t_monitor_alert_rules SET c_rule_id = REPLACE(c_rule_id, ?, ?) WHERE instr(c_rule_id, ?) > 0", "default:"+oldPrefix, "default:"+newPrefix, "default:"+oldPrefix).Error; err != nil {
			return err
		}
	}
	for _, table := range []string{"t_monitor_alert_states", "t_monitor_alert_events"} {
		if !tx.Migrator().HasTable(table) {
			continue
		}
		if err := tx.Exec("UPDATE "+table+" SET c_rule_id = REPLACE(c_rule_id, ?, ?) WHERE instr(c_rule_id, ?) > 0", "default:"+oldPrefix, "default:"+newPrefix, "default:"+oldPrefix).Error; err != nil {
			return err
		}
	}
	if !tx.Migrator().HasTable("t_monitor_alert_events") {
		return nil
	}
	return tx.Exec("UPDATE t_monitor_alert_events SET c_message = REPLACE(c_message, ?, ?), c_payload = REPLACE(c_payload, ?, ?) WHERE instr(c_message, ?) > 0 OR instr(c_payload, ?) > 0", oldID, newID, oldID, newID, oldID, oldID).Error
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

// Aliases returns the canonical ID followed by legacy IDs whose historical
// Storage rows still use the old subject. It lets the read path expose one
// compact host identity without rewriting large historical datasets online.
func (r *Registry) Aliases(ctx context.Context, agentID string) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("host agent registry is unavailable")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, errors.New("agent id is required")
	}
	canonical := agentID
	var alias hostAgentAliasRecord
	if err := r.db.WithContext(ctx).Where("c_alias_id = ?", agentID).First(&alias).Error; err == nil {
		canonical = alias.AgentID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var rows []hostAgentAliasRecord
	if err := r.db.WithContext(ctx).Where("c_agent_id = ?", canonical).Order("c_alias_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []string{canonical}
	for _, row := range rows {
		if row.AliasID != canonical {
			out = append(out, row.AliasID)
		}
	}
	return out, nil
}
