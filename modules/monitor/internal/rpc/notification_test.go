package rpc

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// 通知设置接口返回完整的 Webhook URL，设置弹窗据此展示当前配置；读取权限由控制台收为仅全局管理员。
func TestNotificationChannelReturnsFullWebhookURL(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	repos := db.Repositories()
	require.NoError(t, repos.Notifications.SeedIfAbsent(t.Context(), domain.NotificationChannel{ChannelType: "wecom"}))
	service := New(repos, Options{})

	const url = "https://open.feishu.cn/open-apis/bot/v2/hook/0f6c3c1e-1234-4abc-9def-000000000000"
	updated, err := service.UpdateNotificationChannel(t.Context(), &monitorpb.UpdateNotificationChannelReq{ChannelType: "feishu", WebhookUrl: url})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode())
	require.Equal(t, "feishu", updated.GetChannel().GetChannelType())
	require.Equal(t, url, updated.GetChannel().GetWebhookUrl())

	got, err := service.GetNotificationChannel(t.Context(), &monitorpb.GetNotificationChannelReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, got.GetRetInfo().GetCode())
	require.Equal(t, url, got.GetChannel().GetWebhookUrl())
	require.NotEmpty(t, got.GetChannel().GetUpdatedAt())
}
