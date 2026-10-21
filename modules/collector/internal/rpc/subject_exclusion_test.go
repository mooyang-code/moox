package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

type recordedActivity struct {
	entries []marketfetch.ActivityEntry
	err     error
}

func (r *recordedActivity) AppendActivityLogs(_ context.Context, _ string, entries []marketfetch.ActivityEntry) error {
	r.entries = append(r.entries, entries...)
	return r.err
}

func TestSubjectExclusionsSetListClearAndLogActivity(t *testing.T) {
	service, _ := newReregisterFixture(t)
	activity := &recordedActivity{}
	service.SetActivityLogger(activity)
	ctx := context.Background()

	for _, bad := range []*pb.SetSubjectExclusionsReq{
		{SubjectIds: []string{"BTC-USDT"}, Reason: "x"},
		{SpaceId: "crypto", Reason: "x"},
		{SpaceId: "crypto", SubjectIds: []string{"BTC-USDT"}},
		{SpaceId: "crypto", SubjectIds: []string{"BTC-USDT"}, Reason: "x", ExpiresAt: "tomorrow"},
		{SpaceId: "crypto", SubjectIds: []string{"BTC-USDT"}, Reason: "x", ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
	} {
		rsp, err := service.SetSubjectExclusions(ctx, bad)
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), bad.String())
	}

	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	rsp, err := service.SetSubjectExclusions(ctx, &pb.SetSubjectExclusionsReq{SpaceId: "Crypto", SubjectIds: []string{"btc-usdt", "ETH-USDT", "btc-usdt"}, MarketType: "swap", Reason: "公告下架", Operator: "collector-operator", ExpiresAt: expires})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetExclusions(), 2)
	require.Equal(t, "BTC-USDT", rsp.GetExclusions()[0].GetSubjectId())
	require.Equal(t, "swap", rsp.GetExclusions()[0].GetMarketType())
	require.Equal(t, "collector-operator", rsp.GetExclusions()[0].GetOperator())
	require.Equal(t, expires, rsp.GetExclusions()[0].GetExpiresAt())
	require.Len(t, activity.entries, 1)
	require.Equal(t, "collector.subject_change", activity.entries[0].Category)
	require.Contains(t, activity.entries[0].Title, "排除 2 个标的")

	listed, err := service.ListSubjectExclusions(ctx, &pb.ListSubjectExclusionsReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Len(t, listed.GetExclusions(), 2)

	// 活动日志写入失败不影响排除生效。
	activity.err = errors.New("admin down")
	cleared, err := service.ClearSubjectExclusions(ctx, &pb.ClearSubjectExclusionsReq{SpaceId: "crypto", SubjectIds: []string{"btc-usdt"}, Operator: "moox-cli"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, cleared.GetRetInfo().GetCode())
	require.EqualValues(t, 1, cleared.GetRemoved())
	require.Len(t, activity.entries, 2)
	require.Contains(t, activity.entries[1].Title, "解除排除")
	listed, _ = service.ListSubjectExclusions(ctx, &pb.ListSubjectExclusionsReq{SpaceId: "crypto"})
	require.Len(t, listed.GetExclusions(), 1)

	// 没有可移除的不写日志。
	cleared, _ = service.ClearSubjectExclusions(ctx, &pb.ClearSubjectExclusionsReq{SpaceId: "crypto", SubjectIds: []string{"btc-usdt"}})
	require.Zero(t, cleared.GetRemoved())
	require.Len(t, activity.entries, 2)
}
