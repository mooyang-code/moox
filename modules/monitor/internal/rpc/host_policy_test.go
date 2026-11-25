package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// 主机策略返回全部渠道的完整 URL 。
func TestGetHostPolicyReturnsChannels(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	service := New(db.Repositories(), Options{})

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

	// 检查评估停滞时返回失败（HostAgent 连续拉取失败后直推告警）；恢复后照常返回。
	stale := "last complete business evaluation was 5m0s ago"
	service.evalStale = func() string { return stale }
	stalled, err := service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, stalled.GetRetInfo().GetCode())
	require.Contains(t, stalled.GetRetInfo().GetMsg(), "检查评估停滞")
	require.Empty(t, stalled.GetChannels())
	stale = ""
	recovered, err := service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, recovered.GetRetInfo().GetCode())
	require.Len(t, recovered.GetChannels(), 1)

	unavailable, err := (&Service{}).GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, unavailable.GetRetInfo().GetCode())
}

// 主机快照先收下再判评估停滞：评估停了快照照常入库；快照收不下时策略照常返回，不让 HostAgent 当成 Monitor 不可达。
func TestGetHostPolicyIngestsHostReportBeforeStalenessCheck(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	var received []*monitorpb.HostReport
	var ingestErr error
	service := New(db.Repositories(), Options{
		HostReports: func(_ context.Context, report *monitorpb.HostReport) error {
			received = append(received, report)
			return ingestErr
		},
		EvaluationStale: func() string { return "last complete business evaluation was 5m0s ago" },
	})
	report := &monitorpb.HostReport{ReportId: "r1"}
	stalled, err := service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{Host: report})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, stalled.GetRetInfo().GetCode())
	require.Equal(t, []*monitorpb.HostReport{report}, received)

	service.evalStale = nil
	ingestErr = errors.New("主机快照的采集时间不合法")
	policy, err := service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{Host: report})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, policy.GetRetInfo().GetCode())
	require.Len(t, received, 2)

	// 采集失败的那一分钟不带快照，只拉策略。
	_, err = service.GetHostPolicy(t.Context(), &monitorpb.GetHostPolicyReq{})
	require.NoError(t, err)
	require.Len(t, received, 2)
}
