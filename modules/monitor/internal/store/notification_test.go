package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"gorm.io/gorm"
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

// TableExists 让启动流程分辨首次建库：只有首次建库才写入部署配置里的推送渠道。
func TestTableExistsDistinguishesFreshDatabase(t *testing.T) {
	mgr, err := Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	table := domain.NotificationChannel{}.TableName()
	if exists, err := mgr.TableExists(table); err != nil || exists {
		t.Fatalf("fresh database reports %s exists=%t err=%v", table, exists, err)
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	if exists, err := mgr.TableExists(table); err != nil || !exists {
		t.Fatalf("after schema %s exists=%t err=%v", table, exists, err)
	}
}

func TestNotificationChannelsAddListDelete(t *testing.T) {
	repos := openNotificationTestDB(t).Repositories()
	ctx := t.Context()
	first, err := repos.Notifications.Add(ctx, "wecom", "https://example.com/first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repos.Notifications.Add(ctx, "feishu", " https://example.com/second ")
	if err != nil {
		t.Fatal(err)
	}
	if first.ChannelID == "" || first.ChannelID == second.ChannelID {
		t.Fatalf("channels need distinct ids: %q %q", first.ChannelID, second.ChannelID)
	}
	if _, err := repos.Notifications.Add(ctx, "feishu", "https://example.com/first"); !errors.Is(err, ErrNotificationChannelExists) {
		t.Fatalf("duplicate URL accepted: %v", err)
	}
	if _, err := repos.Notifications.Add(ctx, "sms", "https://example.com/third"); err == nil {
		t.Fatal("unknown channel type accepted")
	}
	channels, err := repos.Notifications.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 || channels[0].ChannelID != first.ChannelID || channels[1].WebhookURL != "https://example.com/second" {
		t.Fatalf("channels = %+v", channels)
	}
	if err := repos.Notifications.Delete(ctx, first.ChannelID); err != nil {
		t.Fatal(err)
	}
	if err := repos.Notifications.Delete(ctx, first.ChannelID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleting a missing channel = %v", err)
	}
	channels, err = repos.Notifications.List(ctx)
	if err != nil || len(channels) != 1 || channels[0].ChannelID != second.ChannelID {
		t.Fatalf("channels after delete = %+v err=%v", channels, err)
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
