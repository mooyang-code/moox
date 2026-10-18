package policysync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/stretchr/testify/require"
)

const wecom = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=sync"

func response(url string) *monitorpb.GetHostPolicyRsp {
	return &monitorpb.GetHostPolicyRsp{
		RetInfo:  &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Channels: []*monitorpb.HostPolicyChannel{{ChannelType: "wecom", WebhookUrl: url}},
		Watchdog: &monitorpb.HostWatchdogPolicy{MergeWindow: "15m0s"},
	}
}

// 种子被中心的值覆盖；内容不变不重写；渠道变化时版本号加一；拉取失败保留旧文件。
func TestSyncOverridesSeedAndKeepsCacheOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", hostpolicy.FileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	seed := hostpolicy.Policy{Source: hostpolicy.SourceSeed, Version: 3, NotificationChannels: []hostpolicy.Channel{{Type: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=seed"}}}
	require.NoError(t, hostpolicy.Write(path, seed))

	current := response(wecom)
	var fetchErr error
	syncer, err := New(path, func(context.Context) (*monitorpb.GetHostPolicyRsp, error) { return current, fetchErr })
	require.NoError(t, err)

	result, err := syncer.SyncOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, Written, result)
	saved, err := hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, hostpolicy.SourceMonitor, saved.Source)
	require.Equal(t, int64(4), saved.Version)
	require.Equal(t, wecom, saved.NotificationChannels[0].WebhookURL)
	require.Equal(t, "15m0s", saved.Watchdog.MergeWindow)

	result, err = syncer.SyncOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, Unchanged, result)

	current = response("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=rotated")
	result, err = syncer.SyncOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, Written, result)
	saved, err = hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, int64(5), saved.Version)

	fetchErr = errors.New("gateway unavailable")
	_, err = syncer.SyncOnce(t.Context())
	require.ErrorContains(t, err, "gateway unavailable")
	fetchErr = nil
	current = &monitorpb.GetHostPolicyRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "db"}}
	_, err = syncer.SyncOnce(t.Context())
	require.Error(t, err)
	current = &monitorpb.GetHostPolicyRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Channels: []*monitorpb.HostPolicyChannel{{ChannelType: "sms", WebhookUrl: "x"}}}
	_, err = syncer.SyncOnce(t.Context())
	require.ErrorContains(t, err, "invalid host policy")
	kept, err := hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, saved, kept, "失败时旧文件不变")
	_, lastErr := syncer.Status()
	require.NotEmpty(t, lastErr)
}

// Monitor 删光渠道时也同步（主机侧随之停止推送），缓存不存在时从版本 1 开始。
func TestSyncWritesEmptyChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", hostpolicy.FileName)
	syncer, err := New(path, func(context.Context) (*monitorpb.GetHostPolicyRsp, error) {
		return &monitorpb.GetHostPolicyRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
	})
	require.NoError(t, err)
	result, err := syncer.SyncOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, Written, result)
	saved, err := hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, int64(1), saved.Version)
	require.Empty(t, saved.NotificationChannels)
}
