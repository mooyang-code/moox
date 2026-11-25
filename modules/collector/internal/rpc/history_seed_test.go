package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

// seedOps 只实现历史补齐相关的两个方法，其余方法留空（嵌入的接口为 nil，调用即 panic）。
type seedOps struct {
	KlineOps
	views    []klinecheck.SeedView
	skipped  int
	skipArgs [2]string
	onlyOpen bool
}

func (o *seedOps) SeedViews(_ context.Context, spaceID string, onlyActive bool) ([]klinecheck.SeedView, error) {
	o.skipArgs[0], o.onlyOpen = spaceID, onlyActive
	return o.views, nil
}

func (o *seedOps) SkipSeeds(_ context.Context, spaceID, datasetID string) (int, error) {
	o.skipArgs = [2]string{spaceID, datasetID}
	return o.skipped, nil
}

func TestListHistorySeedsMapsProgress(t *testing.T) {
	now := time.Date(2026, 11, 25, 1, 0, 0, 0, time.UTC)
	finished := now.Add(time.Hour)
	ops := &seedOps{views: []klinecheck.SeedView{{
		HistorySeed: store.HistorySeed{SpaceID: "stockus", DatasetID: "ds", Frequency: "5m", Status: store.SeedRunning, TargetFrom: now.Add(-24 * time.Hour), EffectiveFrom: now, StartedAt: now, Note: "说明"},
		TotalChunks: 100, DoneChunks: 40, FailedChunks: 2, ETA: 90 * time.Second,
	}, {
		HistorySeed: store.HistorySeed{SpaceID: "stockus", DatasetID: "ds2", Frequency: "1m", Status: store.SeedDone, FinishedAt: &finished},
	}, {
		HistorySeed: store.HistorySeed{SpaceID: "stockus", DatasetID: "ds3", Frequency: "1m", Status: store.SeedPreparing},
	}}}
	svc := &Service{klineOps: ops}
	rsp, err := svc.ListHistorySeeds(context.Background(), &pb.ListHistorySeedsReq{SpaceId: " stockus ", OnlyRunning: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "stockus", ops.skipArgs[0], "空间名去掉首尾空白")
	require.True(t, ops.onlyOpen)
	require.Len(t, rsp.GetSeeds(), 3)
	require.Equal(t, "running", rsp.GetSeeds()[2].GetStatus(), "拼作业对下游就是补齐中")
	first := rsp.GetSeeds()[0]
	require.Equal(t, "running", first.GetStatus())
	require.Equal(t, int32(100), first.GetTotalChunks())
	require.Equal(t, int32(40), first.GetDoneChunks())
	require.Equal(t, int32(2), first.GetFailedChunks())
	require.Equal(t, int64(90), first.GetEtaSeconds())
	require.Equal(t, now.Format(time.RFC3339), first.GetEffectiveFrom())
	require.Empty(t, first.GetFinishedAt())
	require.Equal(t, finished.Format(time.RFC3339), rsp.GetSeeds()[1].GetFinishedAt())
}

func TestSkipHistorySeedRequiresSpace(t *testing.T) {
	ops := &seedOps{skipped: 3}
	svc := &Service{klineOps: ops}
	rsp, err := svc.SkipHistorySeed(context.Background(), &pb.SkipHistorySeedReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.SkipHistorySeed(context.Background(), &pb.SkipHistorySeedReq{SpaceId: "crypto", DatasetId: " ds "})
	require.NoError(t, err)
	require.Equal(t, int32(3), rsp.GetSkipped())
	require.Equal(t, [2]string{"crypto", "ds"}, ops.skipArgs)
}

func TestHistorySeedRPCsNeedKlineOps(t *testing.T) {
	svc := &Service{}
	list, err := svc.ListHistorySeeds(context.Background(), &pb.ListHistorySeedsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, list.GetRetInfo().GetCode())
	skip, err := svc.SkipHistorySeed(context.Background(), &pb.SkipHistorySeedReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, skip.GetRetInfo().GetCode())
}
