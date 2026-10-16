package rpc

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// 推送渠道按添加的方式管理：可以加多个、逐个删除；列出时返回完整 URL（控制台只允许全局管理员读取）。
func TestNotificationChannelsAreAddedListedAndDeleted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	service := New(db.Repositories(), Options{})

	const wecom = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=0f6c3c1e-1234-4abc-9def-000000000000"
	const feishu = "https://open.feishu.cn/open-apis/bot/v2/hook/0f6c3c1e-1234-4abc-9def-111111111111"
	added, err := service.AddNotificationChannel(t.Context(), &monitorpb.AddNotificationChannelReq{ChannelType: "wecom", WebhookUrl: wecom})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, added.GetRetInfo().GetCode())
	require.Equal(t, wecom, added.GetChannel().GetWebhookUrl())
	second, err := service.AddNotificationChannel(t.Context(), &monitorpb.AddNotificationChannelReq{ChannelType: "feishu", WebhookUrl: feishu})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, second.GetRetInfo().GetCode())

	for _, req := range []*monitorpb.AddNotificationChannelReq{
		{ChannelType: "feishu", WebhookUrl: feishu},                        // 重复
		{ChannelType: "feishu", WebhookUrl: wecom},                         // 域名与平台不符
		{ChannelType: "wecom", WebhookUrl: "http://qyapi.weixin.qq.com/x"}, // 不是 HTTPS
		{ChannelType: "wecom"},                                             // 空 URL
	} {
		rsp, err := service.AddNotificationChannel(t.Context(), req)
		require.NoError(t, err)
		require.NotEqual(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "%+v", req)
	}

	listed, err := service.ListNotificationChannels(t.Context(), &monitorpb.ListNotificationChannelsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, listed.GetRetInfo().GetCode())
	require.Len(t, listed.GetChannels(), 2)
	require.Equal(t, wecom, listed.GetChannels()[0].GetWebhookUrl())
	require.Equal(t, "feishu", listed.GetChannels()[1].GetChannelType())
	require.NotEmpty(t, listed.GetChannels()[0].GetCreatedAt())

	deleted, err := service.DeleteNotificationChannel(t.Context(), &monitorpb.DeleteNotificationChannelReq{ChannelId: added.GetChannel().GetChannelId()})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode())
	missing, err := service.DeleteNotificationChannel(t.Context(), &monitorpb.DeleteNotificationChannelReq{ChannelId: added.GetChannel().GetChannelId()})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())

	listed, err = service.ListNotificationChannels(t.Context(), &monitorpb.ListNotificationChannelsReq{})
	require.NoError(t, err)
	require.Len(t, listed.GetChannels(), 1)
	require.Equal(t, feishu, listed.GetChannels()[0].GetWebhookUrl())
}
