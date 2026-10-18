package rpc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// 主机策略返回全部渠道的完整 URL 与守护参数；未配置的守护参数为空串，主机用自己的默认值。
func TestGetHostPolicyReturnsChannelsAndWatchdog(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	service := New(db.Repositories(), Options{HostWatchdog: config.HostWatchdogConfig{MergeWindow: 15 * time.Minute, CrashLoopFailures: 4}})

	empty, err := service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, empty.GetRetInfo().GetCode())
	require.Empty(t, empty.GetChannels())

	const wecom = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=0f6c3c1e-1234-4abc-9def-000000000000"
	_, err = service.AddNotificationChannel(t.Context(), &monitorpb.AddNotificationChannelReq{ChannelType: "wecom", WebhookUrl: wecom})
	require.NoError(t, err)
	policy, err := service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.Len(t, policy.GetChannels(), 1)
	require.Equal(t, "wecom", policy.GetChannels()[0].GetChannelType())
	require.Equal(t, wecom, policy.GetChannels()[0].GetWebhookUrl())
	require.Equal(t, "15m0s", policy.GetWatchdog().GetMergeWindow())
	require.Equal(t, int32(4), policy.GetWatchdog().GetCrashLoopFailures())
	require.Empty(t, policy.GetWatchdog().GetStableAfter())

	unavailable, err := (&Service{}).GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, unavailable.GetRetInfo().GetCode())
}
