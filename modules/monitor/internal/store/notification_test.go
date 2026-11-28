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

func TestNotificationChannelsImportIsIdempotentByWebhookURL(t *testing.T) {
	repos := openNotificationTestDB(t).Repositories()
	ctx := t.Context()

	created, updated, err := repos.Notifications.Import(ctx, nil)
	if err != nil || created != 0 || updated != 0 {
		t.Fatalf("空列表导入应无操作: created=%d updated=%d err=%v", created, updated, err)
	}

	first, err := repos.Notifications.Add(ctx, "feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/a", "old-secret")
	if err != nil {
		t.Fatal(err)
	}

	created, updated, err = repos.Notifications.Import(ctx, []NotificationChannelInput{
		{ChannelType: "feishu", WebhookURL: " https://open.feishu.cn/open-apis/bot/v2/hook/a ", Secret: "new-secret"},
		{ChannelType: "webhook", WebhookURL: "https://alerts.example.com/hook", Secret: "s1"},
	})
	if err != nil || created != 1 || updated != 1 {
		t.Fatalf("首次导入结果不对: created=%d updated=%d err=%v", created, updated, err)
	}

	channels, err := repos.Notifications.List(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("channels = %+v err=%v", channels, err)
	}
	if channels[0].ChannelID != first.ChannelID || channels[0].Secret != "new-secret" {
		t.Fatalf("覆盖已有渠道时应保留 channel_id 并更新密钥: %+v", channels[0])
	}
	if channels[1].ChannelType != "webhook" || channels[1].Secret != "s1" {
		t.Fatalf("新建渠道内容不对: %+v", channels[1])
	}

	// 再次导入并把密钥清空：应幂等覆盖且确实清空密钥。
	created, updated, err = repos.Notifications.Import(ctx, []NotificationChannelInput{
		{ChannelType: "feishu", WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/a", Secret: ""},
		{ChannelType: "webhook", WebhookURL: "https://alerts.example.com/hook", Secret: ""},
	})
	if err != nil || created != 0 || updated != 2 {
		t.Fatalf("重复导入应全部记为更新: created=%d updated=%d err=%v", created, updated, err)
	}
	channels, err = repos.Notifications.List(ctx)
	if err != nil || channels[0].Secret != "" || channels[1].Secret != "" {
		t.Fatalf("清空密钥未生效: %+v err=%v", channels, err)
	}

	// 批次中存在非法项或重复 URL 时整体拒绝，不改库。
	if _, _, err := repos.Notifications.Import(ctx, []NotificationChannelInput{
		{ChannelType: "webhook", WebhookURL: "https://alerts.example.com/new"},
		{ChannelType: "webhook", WebhookURL: "https://alerts.example.com/new"},
	}); err == nil {
		t.Fatal("重复 URL 的导入批次应被拒绝")
	}
	channels, err = repos.Notifications.List(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("失败批次不应写入任何记录: %+v err=%v", channels, err)
	}
}
