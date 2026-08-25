package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func openNotificationTestDB(t *testing.T) *Store {
	t.Helper()
	mgr, err := Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		_ = mgr.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func TestNotificationChannelSeedDoesNotOverwriteUserValue(t *testing.T) {
	repos := openNotificationTestDB(t).Repositories()
	ctx := t.Context()
	if err := repos.Notifications.SeedIfAbsent(ctx, domain.NotificationChannel{ChannelID: domain.GlobalNotificationChannelID, ChannelType: "wecom", WebhookURL: "https://example.com/seed"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Notifications.UpdateGlobal(ctx, "feishu", "https://example.com/user"); err != nil {
		t.Fatal(err)
	}
	if err := repos.Notifications.SeedIfAbsent(ctx, domain.NotificationChannel{ChannelID: domain.GlobalNotificationChannelID, ChannelType: "wecom", WebhookURL: "https://example.com/restart"}); err != nil {
		t.Fatal(err)
	}
	got, err := repos.Notifications.GetGlobal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChannelType != "feishu" || got.WebhookURL != "https://example.com/user" {
		t.Fatalf("channel overwritten: %+v", got)
	}
}

func TestNotificationChannelRejectsNonGlobalAndUnknownType(t *testing.T) {
	repos := openNotificationTestDB(t).Repositories()
	ctx := t.Context()
	if err := repos.Notifications.SeedIfAbsent(ctx, domain.NotificationChannel{ChannelID: "other", ChannelType: "wecom"}); err == nil {
		t.Fatal("non-global channel accepted")
	}
	if err := repos.Notifications.SeedIfAbsent(ctx, domain.NotificationChannel{ChannelID: domain.GlobalNotificationChannelID, ChannelType: "sms"}); err == nil {
		t.Fatal("unknown channel accepted")
	}
}

func TestResetLegacyMonitorTablesRebuildsWebhookAlertSchema(t *testing.T) {
	mgr := openNotificationTestDB(t)
	if err := mgr.db.Exec("DROP TABLE t_monitor_alert_rules").Error; err != nil {
		t.Fatal(err)
	}
	if err := mgr.db.Exec(`CREATE TABLE t_monitor_alert_rules (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_rule_id TEXT, c_check_id TEXT, c_webhook_id TEXT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	reset, err := mgr.ResetLegacyMonitorTables()
	if err != nil || !reset {
		t.Fatalf("reset = %v, err = %v", reset, err)
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	if mgr.db.Migrator().HasColumn(&domain.AlertRule{}, "c_webhook_id") {
		t.Fatal("legacy webhook column still exists")
	}
}

func TestListEnabledFiringStatesFiltersBeforeLimit(t *testing.T) {
	repos := openNotificationTestDB(t).Repositories()
	ctx := t.Context()
	for _, check := range []domain.Check{
		{SpaceID: "crypto", CheckID: "good", Name: "good", Kind: domain.CheckKindExternal, Enabled: true},
		{SpaceID: "crypto", CheckID: "disabled", Name: "disabled", Kind: domain.CheckKindExternal, Enabled: true},
	} {
		if err := repos.Checks.Create(ctx, &check); err != nil {
			t.Fatal(err)
		}
	}
	for _, rule := range []domain.AlertRule{
		{SpaceID: "crypto", RuleID: "default:good", CheckID: "good", Enabled: true},
		{SpaceID: "crypto", RuleID: "default:disabled", CheckID: "disabled", Enabled: true},
		{SpaceID: "crypto", RuleID: "custom:good", CheckID: "good", Enabled: true},
	} {
		if err := repos.Alerts.CreateRule(ctx, &rule); err != nil {
			t.Fatal(err)
		}
		if err := repos.Alerts.UpsertState(ctx, &domain.AlertState{SpaceID: rule.SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, Status: domain.AlertStatusFiring, TriggeredAt: ptrTime(time.Now().UTC())}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.Checks.Update(ctx, &domain.Check{SpaceID: "crypto", CheckID: "disabled", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	states, err := repos.Alerts.ListEnabledFiringStates(ctx, "crypto", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].RuleID != "default:good" {
		t.Fatalf("enabled firing states = %+v", states)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
