package resample

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeViewGetter struct {
	views []*storagepb.View
	index int
}

func (f *fakeViewGetter) GetView(_ context.Context, _ *storagepb.GetViewReq, _ ...client.Option) (*storagepb.GetViewRsp, error) {
	if len(f.views) == 0 {
		return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
	}
	view := f.views[f.index]
	if f.index < len(f.views)-1 {
		f.index++
	}
	return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: view}, nil
}

func TestTargetViewRevisionReadyRequiresActiveIndexAndRevision(t *testing.T) {
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "active", ActiveViewRevision: 2, ActiveIndexId: "idx"}, 3))
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "active", ActiveViewRevision: 3}, 3))
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "building", ActiveViewRevision: 3, ActiveIndexId: "idx"}, 3))
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "active", DesiredViewRevision: 4, ActiveViewRevision: 3, ActiveIndexId: "idx"}, 3))
	assert.True(t, targetViewRevisionReady(&storagepb.View{Status: "active", ActiveViewRevision: 3, ActiveIndexId: "idx"}, 3))
}

func TestWaitTargetViewRevisionPollsUntilDesiredIndexIsActive(t *testing.T) {
	getter := &fakeViewGetter{views: []*storagepb.View{
		{Status: "active", DesiredViewRevision: 3, ActiveViewRevision: 2, ActiveIndexId: "idx-a"},
		{Status: "active", DesiredViewRevision: 3, ActiveViewRevision: 3, ActiveIndexId: "idx-b"},
	}}
	require.NoError(t, waitTargetViewRevision(context.Background(), getter, nil, "crypto", "view", 3, 500*time.Millisecond))
	assert.Equal(t, 1, getter.index)
}

func TestWaitTargetViewRevisionReturnsNotReadyWhenTimeoutExpires(t *testing.T) {
	getter := &fakeViewGetter{views: []*storagepb.View{{Status: "active", DesiredViewRevision: 2, ActiveViewRevision: 1, ActiveIndexId: "idx"}}}
	err := waitTargetViewRevision(context.Background(), getter, nil, "crypto", "view", 2, 20*time.Millisecond)
	assert.ErrorIs(t, err, ErrTargetViewNotReady)
}

func TestValidateTargetDatasetChecksImmutableLineageAndPlacement(t *testing.T) {
	want := map[string]string{
		"owner_module":          "collector",
		"managed_by":            "collector",
		"market_type":           "spot",
		"storage_model":         "wide_common_metrics",
		"dataset_role":          "kline_resample_result",
		"resample_rule_id":      "rule-5m",
		"source_dataset_id":     "binance_spot_kline_1m",
		"source_data_source_id": "binance",
		"source_freq":           "1m",
		"source_series_tag":     "venue:binance",
		"target_freq":           "5m",
		"alignment":             "epoch_utc",
	}
	dataset := &storagepb.Dataset{
		DataSourceId: "crypto",
		DataNodeId:   "storage-node-0",
		DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"5m"},
		Attributes:   cloneStringMap(want),
	}
	require.NoError(t, validateTargetDataset(dataset, want, "5m", "crypto", "storage-node-0"))

	for key, value := range want {
		t.Run("attribute/"+key, func(t *testing.T) {
			copy := proto.Clone(dataset).(*storagepb.Dataset)
			copy.Attributes = cloneStringMap(dataset.Attributes)
			copy.Attributes[key] = value + "-drift"
			require.ErrorContains(t, validateTargetDataset(copy, want, "5m", "crypto", "storage-node-0"), "immutable lineage attribute")
		})
	}
	wrongSource := proto.Clone(dataset).(*storagepb.Dataset)
	wrongSource.DataSourceId = "binance"
	require.ErrorContains(t, validateTargetDataset(wrongSource, want, "5m", "crypto", "storage-node-0"), "data source")
	wrongNode := proto.Clone(dataset).(*storagepb.Dataset)
	wrongNode.DataNodeId = "storage-node-1"
	require.ErrorContains(t, validateTargetDataset(wrongNode, want, "5m", "crypto", "storage-node-0"), "data node")
	monthly := proto.Clone(dataset).(*storagepb.Dataset)
	monthly.Freqs = []string{"5M"}
	require.ErrorContains(t, validateTargetDataset(monthly, want, "5m", "crypto", "storage-node-0"), "does not enable frequency")
}
