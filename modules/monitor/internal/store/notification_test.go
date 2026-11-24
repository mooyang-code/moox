package store

import (
	"errors"
	"path/filepath"
	"testing"

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

func TestNotificationChannelsAddListDelete(t *testing.T) {
	repos := openNotificationTestDB(t).Repositories()
	ctx := t.Context()
	first, err := repos.Notifications.Add(ctx, "wecom", "https://example.com/first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repos.Notifications.Add(ctx, "feishu", " https://example.com/second ", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ChannelID == "" || first.ChannelID == second.ChannelID {
		t.Fatalf("channels need distinct ids: %q %q", first.ChannelID, second.ChannelID)
	}
	if _, err := repos.Notifications.Add(ctx, "feishu", "https://example.com/first", ""); !errors.Is(err, ErrNotificationChannelExists) {
		t.Fatalf("duplicate URL accepted: %v", err)
	}
	if _, err := repos.Notifications.Add(ctx, "sms", "https://example.com/third", ""); err == nil {
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
