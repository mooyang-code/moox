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

func response(url string) *monitorpb.SyncHostAgentRsp {
	return &monitorpb.SyncHostAgentRsp{
		RetInfo:  &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Channels: []*monitorpb.HostNotificationChannel{{ChannelType: "wecom", WebhookUrl: url}},
	}
}

// 已有的缓存被中心的值覆盖；内容不变不重写；渠道变化时版本号加一；拉取失败保留旧文件。
func TestSyncOverridesCacheAndKeepsItOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-agent", "host-policy.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	seed := hostpolicy.Policy{Version: 3, NotificationChannels: []hostpolicy.Channel{{Type: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=seed"}}}
	require.NoError(t, hostpolicy.Write(path, seed))

	current := response(wecom)
	var fetchErr error
	syncer, err := New(path, func(context.Context, *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error) {
		return current, fetchErr
	})
	require.NoError(t, err)

	result, err := syncer.SyncOnce(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, Written, result)
	saved, err := hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, int64(4), saved.Version)
	require.Equal(t, wecom, saved.NotificationChannels[0].WebhookURL)

	result, err = syncer.SyncOnce(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, Unchanged, result)

	current = response("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=rotated")
	result, err = syncer.SyncOnce(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, Written, result)
	saved, err = hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, int64(5), saved.Version)

	fetchErr = errors.New("gateway unavailable")
	_, err = syncer.SyncOnce(t.Context(), nil)
	require.ErrorContains(t, err, "gateway unavailable")
	fetchErr = nil
	current = &monitorpb.SyncHostAgentRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "db"}}
	_, err = syncer.SyncOnce(t.Context(), nil)
	require.Error(t, err)
	current = &monitorpb.SyncHostAgentRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Channels: []*monitorpb.HostNotificationChannel{{ChannelType: "sms", WebhookUrl: "x"}}}
	_, err = syncer.SyncOnce(t.Context(), nil)
	require.ErrorContains(t, err, "invalid host policy")
	kept, err := hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, saved, kept, "失败时旧文件不变")
}

// Monitor 删光渠道时也同步（主机侧随之停止推送），缓存不存在时从版本 1 开始。
func TestSyncWritesEmptyChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-agent", "host-policy.json")
	syncer, err := New(path, func(context.Context, *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error) {
		return &monitorpb.SyncHostAgentRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
	})
	require.NoError(t, err)
	result, err := syncer.SyncOnce(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, Written, result)
	saved, err := hostpolicy.Read(path)
	require.NoError(t, err)
	require.Equal(t, int64(1), saved.Version)
	require.Empty(t, saved.NotificationChannels)
}

// 主机快照随同一次拉取带给 Monitor。
func TestSyncCarriesHostReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-agent", "host-policy.json")
	var got *monitorpb.SyncHostAgentReq
	syncer, err := New(path, func(_ context.Context, req *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error) {
		got = req
		return &monitorpb.SyncHostAgentRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
	})
	require.NoError(t, err)
	report := &monitorpb.HostReport{ReportId: "r1"}
	_, err = syncer.SyncOnce(t.Context(), report)
	require.NoError(t, err)
	require.Same(t, report, got.GetHost())
}
