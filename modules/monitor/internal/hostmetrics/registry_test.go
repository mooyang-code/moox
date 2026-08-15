package hostmetrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRegistryPersistsAcrossRestartAndRejectsLateObservation(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	first := NewRegistry(db)
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)

	result, err := first.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.True(t, result.Current)
	require.Nil(t, result.Transition)

	restarted := NewRegistry(db)
	rows, err := restarted.List(ctx, t0.Add(60*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "agent-a", rows[0].AgentID)
	require.True(t, rows[0].Reachable)
	require.Equal(t, int64(60), rows[0].StaleSeconds)

	result, err = restarted.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-new", BootID: "boot-new",
		OccurredAt: t0.Add(100 * time.Second), EventID: "event-100",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.True(t, result.Current)

	result, err = restarted.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-late", BootID: "boot-late",
		OccurredAt: t0.Add(50 * time.Second), EventID: "event-50",
	})
	require.NoError(t, err)
	require.False(t, result.Updated)
	require.False(t, result.Current)

	rows, err = restarted.List(ctx, t0.Add(110*time.Second))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "host-new", rows[0].Hostname)
	require.Equal(t, "boot-new", rows[0].BootID)
	require.Equal(t, "event-100", rows[0].LastEventID)
	require.Equal(t, t0.Add(100*time.Second), rows[0].LastSeenAt)
}

func TestRegistryObserveNewerSampleRecoversUnreachableAgent(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	_, err := registry.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)
	transitions, err := registry.MarkUnreachable(ctx, t0.Add(91*time.Second), 90*time.Second)
	require.NoError(t, err)
	require.Len(t, transitions, 1)

	result, err := registry.Observe(ctx, HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-b",
		OccurredAt: t0.Add(100 * time.Second), EventID: "event-100",
	})
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.Equal(t, &PresenceTransition{
		AgentID: "agent-a", Hostname: "host-a",
		From: PresenceUnreachable, To: PresenceReachable,
		ObservedAt: t0.Add(100 * time.Second),
	}, result.Transition)
}

func TestRegistryMigratesLegacyHostToCompactIDAndKeepsAlias(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	t0 := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	legacy := "550e8400-e29b-41d4-a716-446655440000"
	compact, err := hostmetricpb.CompactAgentIDForLegacy(legacy)
	require.NoError(t, err)
	_, err = registry.Observe(ctx, HostObservation{AgentID: legacy, Hostname: "host-a", BootID: "boot-a", OccurredAt: t0, EventID: "event-0"})
	require.NoError(t, err)
	result, err := registry.Observe(ctx, HostObservation{AgentID: compact, Hostname: "host-a", BootID: "boot-b", OccurredAt: t0.Add(time.Minute), EventID: "event-1"})
	require.NoError(t, err)
	require.Equal(t, compact, result.AgentID)
	ids, err := registry.Aliases(ctx, compact)
	require.NoError(t, err)
	require.Equal(t, []string{compact, legacy}, ids)
	ids, err = registry.Aliases(ctx, legacy)
	require.NoError(t, err)
	require.Equal(t, []string{compact, legacy}, ids)
}

func TestRegistryResolvesLegacyUUIDAfterCompactObservation(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry(openRegistryTestDB(t))
	legacy := "550e8400-e29b-41d4-a716-446655440000"
	compact, err := hostmetricpb.CompactAgentIDForLegacy(legacy)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = registry.Observe(ctx, HostObservation{AgentID: compact, Hostname: "host-a", BootID: "boot-a", OccurredAt: now, EventID: "event-0"})
	require.NoError(t, err)
	result, err := registry.Observe(ctx, HostObservation{AgentID: legacy, Hostname: "host-a", BootID: "boot-b", OccurredAt: now.Add(time.Minute), EventID: "event-1"})
	require.NoError(t, err)
	require.Equal(t, compact, result.AgentID)
	ids, err := registry.Aliases(ctx, legacy)
	require.NoError(t, err)
	require.Equal(t, []string{compact, legacy}, ids)
}

func TestRegistryMigrateLegacyIDsChangesPersistedEntity(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	registry := NewRegistry(db)
	_, err := registry.Observe(ctx, HostObservation{AgentID: "550e8400-e29b-41d4-a716-446655440000", Hostname: "host-a", BootID: "boot-a", OccurredAt: time.Now().UTC(), EventID: "event-0"})
	require.NoError(t, err)
	require.NoError(t, registry.MigrateLegacyIDs(ctx))
	rows, err := registry.List(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, rows[0].AgentID, 4)
	require.Regexp(t, `^[A-Za-z0-9]{4}$`, rows[0].AgentID)
	expected, err := hostmetricpb.CompactAgentIDForLegacy("550e8400-e29b-41d4-a716-446655440000")
	require.NoError(t, err)
	require.Equal(t, expected, rows[0].AgentID)
}

func TestRegistryMigrationUpdatesAlertIdentityAcrossStateAndHistory(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	registry := NewRegistry(db)
	legacy := "550e8400-e29b-41d4-a716-446655440000"
	require.NoError(t, db.Exec(`
CREATE TABLE t_monitor_alert_rules (c_rule_id TEXT, c_check_id TEXT);
CREATE TABLE t_monitor_alert_states (c_rule_id TEXT, c_check_id TEXT, c_dedupe_key TEXT);
CREATE TABLE t_monitor_alert_events (c_rule_id TEXT, c_check_id TEXT, c_message TEXT, c_payload TEXT);
INSERT INTO t_monitor_alert_rules(c_rule_id,c_check_id) VALUES ('default:host:550e8400-e29b-41d4-a716-446655440000:cpu','host:550e8400-e29b-41d4-a716-446655440000:cpu');
INSERT INTO t_monitor_alert_states(c_rule_id,c_check_id,c_dedupe_key) VALUES ('default:host:550e8400-e29b-41d4-a716-446655440000:cpu','host:550e8400-e29b-41d4-a716-446655440000:cpu','host:550e8400-e29b-41d4-a716-446655440000');
INSERT INTO t_monitor_alert_events(c_rule_id,c_check_id,c_message,c_payload) VALUES ('default:host:550e8400-e29b-41d4-a716-446655440000:cpu','host:550e8400-e29b-41d4-a716-446655440000:cpu','host 550e8400-e29b-41d4-a716-446655440000','{"agent_id":"550e8400-e29b-41d4-a716-446655440000"}');
`).Error)
	require.NoError(t, db.Create(&hostAgentRecord{AgentID: legacy, Hostname: "host-a", BootID: "boot-a", LastSeenAt: time.Now().UTC(), LastEventID: "event-0", Status: PresenceReachable}).Error)
	require.NoError(t, registry.MigrateLegacyIDs(ctx))
	compact, err := hostmetricpb.CompactAgentIDForLegacy(legacy)
	require.NoError(t, err)
	var rule struct {
		RuleID  string `gorm:"column:c_rule_id"`
		CheckID string `gorm:"column:c_check_id"`
	}
	var state struct {
		RuleID  string `gorm:"column:c_rule_id"`
		CheckID string `gorm:"column:c_check_id"`
		Dedupe  string `gorm:"column:c_dedupe_key"`
	}
	var event struct {
		RuleID  string `gorm:"column:c_rule_id"`
		CheckID string `gorm:"column:c_check_id"`
		Message string `gorm:"column:c_message"`
		Payload string `gorm:"column:c_payload"`
	}
	require.NoError(t, db.Table("t_monitor_alert_rules").First(&rule).Error)
	require.NoError(t, db.Table("t_monitor_alert_states").First(&state).Error)
	require.NoError(t, db.Table("t_monitor_alert_events").First(&event).Error)
	require.Equal(t, "default:host:"+compact+":cpu", rule.RuleID)
	require.Equal(t, "host:"+compact+":cpu", rule.CheckID)
	require.Equal(t, rule.RuleID, state.RuleID)
	require.Equal(t, rule.CheckID, state.CheckID)
	require.Equal(t, "host:"+compact, state.Dedupe)
	require.Equal(t, rule.RuleID, event.RuleID)
	require.Equal(t, rule.CheckID, event.CheckID)
	require.Contains(t, event.Message, compact)
	require.Contains(t, event.Payload, compact)
}

func openRegistryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "monitor.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE t_monitor_host_agents (
    c_agent_id TEXT PRIMARY KEY,
    c_hostname TEXT NOT NULL,
    c_boot_id TEXT NOT NULL,
    c_last_seen_at DATETIME NOT NULL,
    c_last_event_id TEXT NOT NULL,
    c_status TEXT NOT NULL CHECK (c_status IN ('reachable', 'unreachable')),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE t_monitor_host_agent_aliases (
    c_alias_id TEXT PRIMARY KEY,
    c_agent_id TEXT NOT NULL,
    c_created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error)
	return db
}
