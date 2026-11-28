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

// 钉钉与通用 Webhook 可以带密钥：密钥入库并下发给主机策略，但列表接口只返回是否设置。
func TestNotificationChannelSecretIsStoredAndNeverListed(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	service := New(db.Repositories(), Options{})

	const dingtalk = "https://oapi.dingtalk.com/robot/send?access_token=0f6c3c1e"
	const hook = "https://alerts.example.com/moox"
	for _, req := range []*monitorpb.AddNotificationChannelReq{
		{ChannelType: "dingtalk", WebhookUrl: dingtalk, Secret: " SECabc "},
		{ChannelType: "webhook", WebhookUrl: hook},
	} {
		rsp, err := service.AddNotificationChannel(t.Context(), req)
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "%+v", req)
	}
	for _, req := range []*monitorpb.AddNotificationChannelReq{
		{ChannelType: "dingtalk", WebhookUrl: "https://oapi.dingtalk.com/robot/send"},                                     // 缺 access_token
		{ChannelType: "dingtalk", WebhookUrl: "https://example.com/robot/send?access_token=x"},                            // 不是钉钉域名
		{ChannelType: "webhook", WebhookUrl: "http://alerts.example.com/other"},                                           // 不是 HTTPS
		{ChannelType: "wecom", WebhookUrl: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcd", Secret: "secret"}, // 企业微信不接受密钥
	} {
		rsp, err := service.AddNotificationChannel(t.Context(), req)
		require.NoError(t, err)
		require.NotEqual(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "%+v", req)
	}

	listed, err := service.ListNotificationChannels(t.Context(), &monitorpb.ListNotificationChannelsReq{})
	require.NoError(t, err)
	require.Len(t, listed.GetChannels(), 2)
	require.True(t, listed.GetChannels()[0].GetHasSecret())
	require.False(t, listed.GetChannels()[1].GetHasSecret())
	require.NotContains(t, listed.String(), "SECabc")

	policy, err := service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.Len(t, policy.GetChannels(), 2)
	require.Equal(t, "SECabc", policy.GetChannels()[0].GetSecret())
	require.Empty(t, policy.GetChannels()[1].GetSecret())

	exported, err := service.ExportNotificationChannels(t.Context(), &monitorpb.ExportNotificationChannelsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, exported.GetRetInfo().GetCode())
	require.Len(t, exported.GetChannels(), 2)
	require.Equal(t, listed.GetChannels()[0].GetChannelId(), exported.GetChannels()[0].GetChannelId())
	require.Equal(t, "dingtalk", exported.GetChannels()[0].GetChannelType())
	require.Equal(t, dingtalk, exported.GetChannels()[0].GetWebhookUrl())
	require.Equal(t, "SECabc", exported.GetChannels()[0].GetSecret())
	require.Equal(t, listed.GetChannels()[0].GetCreatedAt(), exported.GetChannels()[0].GetCreatedAt())
	require.Equal(t, listed.GetChannels()[1].GetChannelId(), exported.GetChannels()[1].GetChannelId())
	require.Equal(t, "webhook", exported.GetChannels()[1].GetChannelType())
	require.Equal(t, hook, exported.GetChannels()[1].GetWebhookUrl())
	require.Empty(t, exported.GetChannels()[1].GetSecret())

	uninitialized := &Service{}
	failed, err := uninitialized.ExportNotificationChannels(t.Context(), &monitorpb.ExportNotificationChannelsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, failed.GetRetInfo().GetCode())
}

// ImportNotificationChannels 按 webhook_url 幂等覆盖导入，返回 created、updated 条数，整批校验失败时不改库。
func TestImportNotificationChannelsOverwritesIdempotentlyAndValidatesBatch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	service := New(db.Repositories(), Options{})

	const feishu = "https://open.feishu.cn/open-apis/bot/v2/hook/0f6c3c1e-1234-4abc-9def-222222222222"
	const dingtalk = "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456"
	const hook = "https://alerts.example.com/moox"

	// 空列表导入成功且条数为 0。
	emptyRsp, err := service.ImportNotificationChannels(t.Context(), &monitorpb.ImportNotificationChannelsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, emptyRsp.GetRetInfo().GetCode())
	require.Zero(t, emptyRsp.GetCreated())
	require.Zero(t, emptyRsp.GetUpdated())

	// 先加一个渠道，后续导入应按 webhook_url 覆盖并新增其余渠道。
	added, err := service.AddNotificationChannel(t.Context(), &monitorpb.AddNotificationChannelReq{
		ChannelType: "feishu", WebhookUrl: feishu, Secret: "old-secret",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, added.GetRetInfo().GetCode())

	firstImport, err := service.ImportNotificationChannels(t.Context(), &monitorpb.ImportNotificationChannelsReq{
		Channels: []*monitorpb.ImportNotificationChannel{
			{ChannelType: "feishu", WebhookUrl: " " + feishu + " ", Secret: " new-secret "},
			{ChannelType: "dingtalk", WebhookUrl: dingtalk, Secret: "SEC123"},
			{ChannelType: "webhook", WebhookUrl: hook},
		},
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, firstImport.GetRetInfo().GetCode())
	require.Equal(t, int32(2), firstImport.GetCreated())
	require.Equal(t, int32(1), firstImport.GetUpdated())

	// 导出核对：已有渠道保留 channel_id 且密钥已更新，新建渠道全部入库。
	exported, err := service.ExportNotificationChannels(t.Context(), &monitorpb.ExportNotificationChannelsReq{})
	require.NoError(t, err)
	require.Len(t, exported.GetChannels(), 3)
	require.Equal(t, added.GetChannel().GetChannelId(), exported.GetChannels()[0].GetChannelId())
	require.Equal(t, "new-secret", exported.GetChannels()[0].GetSecret())
	require.Equal(t, "SEC123", exported.GetChannels()[1].GetSecret())
	require.Empty(t, exported.GetChannels()[2].GetSecret())

	// 重跑同一批导入：幂等覆盖，created=0, updated=3，支持清空密钥。
	secondImport, err := service.ImportNotificationChannels(t.Context(), &monitorpb.ImportNotificationChannelsReq{
		Channels: []*monitorpb.ImportNotificationChannel{
			{ChannelType: "feishu", WebhookUrl: feishu, Secret: ""},
			{ChannelType: "dingtalk", WebhookUrl: dingtalk, Secret: "SEC456"},
			{ChannelType: "webhook", WebhookUrl: hook, Secret: "wh-sec"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, secondImport.GetRetInfo().GetCode())
	require.Equal(t, int32(0), secondImport.GetCreated())
	require.Equal(t, int32(3), secondImport.GetUpdated())

	exported, err = service.ExportNotificationChannels(t.Context(), &monitorpb.ExportNotificationChannelsReq{})
	require.NoError(t, err)
	require.Empty(t, exported.GetChannels()[0].GetSecret())
	require.Equal(t, "SEC456", exported.GetChannels()[1].GetSecret())
	require.Equal(t, "wh-sec", exported.GetChannels()[2].GetSecret())

	// 批次里有非法项或重复 URL：整体拒绝且不泄露完整 URL，库中仍只有 3 条。
	for _, bad := range [][]*monitorpb.ImportNotificationChannel{
		{{ChannelType: "webhook", WebhookUrl: "https://alerts.example.com/fresh"}, {ChannelType: "wecom", WebhookUrl: ""}},
		{{ChannelType: "webhook", WebhookUrl: "https://alerts.example.com/fresh"}, {ChannelType: "wecom", WebhookUrl: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcd", Secret: "not-allowed"}},
		{{ChannelType: "webhook", WebhookUrl: "https://alerts.example.com/dup"}, {ChannelType: "webhook", WebhookUrl: "https://alerts.example.com/dup"}},
	} {
		rsp, err := service.ImportNotificationChannels(t.Context(), &monitorpb.ImportNotificationChannelsReq{Channels: bad})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	}
	listed, err := service.ListNotificationChannels(t.Context(), &monitorpb.ListNotificationChannelsReq{})
	require.NoError(t, err)
	require.Len(t, listed.GetChannels(), 3)

	uninitialized := &Service{}
	failed, err := uninitialized.ImportNotificationChannels(t.Context(), &monitorpb.ImportNotificationChannelsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, failed.GetRetInfo().GetCode())
}
