package merge

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestMergeAssemblerStorageCommitterSendsCompleteRow(t *testing.T) {
	fake := &fakePrimaryCommit{}
	committer := NewStorageCommitter(fake, &commonpb.AuthInfo{AppId: "moox-merge"}, "crypto", []string{
		"dataset_binance_spot_kline_1m__open", "dataset_binance_swap_kline_1m__close",
	})
	key := mergeKey("BTC-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, committer.CommitInput(context.Background(), stableCommitID(key), key, map[string]float64{
		"dataset_binance_spot_kline_1m__open": 1, "dataset_binance_swap_kline_1m__close": 2,
	}, true))
	require.Equal(t, "moox-merge", fake.req.GetAuthInfo().GetAppId())
	require.Equal(t, stableCommitID(key), fake.req.GetCommitId())
	require.Equal(t, "mdataset_binance_kline_1m", fake.req.GetRow().GetKey().GetDatasetId())
	require.Equal(t, "BTC-USDT", fake.req.GetRow().GetKey().GetTimeSeries().GetSubjectId())
	require.Len(t, fake.req.GetRow().GetFields(), 2)
}

func TestMergeAssemblerStorageCommitterRejectsUnreadyRow(t *testing.T) {
	committer := NewStorageCommitter(&fakePrimaryCommit{}, &commonpb.AuthInfo{AppId: "moox-merge"}, "crypto", []string{"open"})
	err := committer.CommitInput(context.Background(), "id", mergeKey("BTC-USDT", time.Now().UTC()), map[string]float64{"open": 1}, false)
	require.ErrorContains(t, err, "incomplete")
}

type fakePrimaryCommit struct {
	req *storagepb.PrimaryCommitInputReq
}

func (f *fakePrimaryCommit) CommitInput(_ context.Context, req *storagepb.PrimaryCommitInputReq, _ ...client.Option) (*storagepb.PrimaryCommitInputRsp, error) {
	f.req = req
	return &storagepb.PrimaryCommitInputRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}}, nil
}
