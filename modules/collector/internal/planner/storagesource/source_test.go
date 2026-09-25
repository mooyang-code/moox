package storagesource

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeMetadataClient struct {
	dataset  *storagepb.Dataset
	subjects []*storagepb.Subject
}

func (f *fakeMetadataClient) GetDataset(_ context.Context, _ *storagepb.GetDatasetReq, _ ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: f.dataset}, nil
}

func (f *fakeMetadataClient) ResolveSubjects(_ context.Context, _ *storagepb.ResolveSubjectsReq, _ ...client.Option) (*storagepb.ResolveSubjectsRsp, error) {
	return &storagepb.ResolveSubjectsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Subjects: f.subjects}, nil
}

func TestDatasetSourceGetDatasetReturnsTagsAndValidationContract(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{dataset: &storagepb.Dataset{
		DataSourceId: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"},
		SubjectTags: []string{"binance_spot"}, Attributes: map[string]string{"market_type": "spot"},
	}}}
	info, err := src.GetDataset(context.Background(), "crypto", "kline")
	require.NoError(t, err)
	require.Equal(t, "binance", info.DataSourceID)
	require.Equal(t, []string{"binance_spot"}, info.SubjectTags)
	require.Equal(t, "spot", info.Attributes["market_type"])
}

func TestDatasetSourceResolveSubjectsUsesStorageUnion(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{subjects: []*storagepb.Subject{
		{SpaceId: "crypto", SubjectId: "BTC-USDT", Name: "BTC/USDT", Status: "active"},
		{SpaceId: "crypto", SubjectId: "ETH-USDT", Status: "active"},
	}}}
	items, err := src.ResolveSubjects(context.Background(), "crypto", []string{"binance_spot"})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "BTC-USDT", items[0].SubjectID)
}

func TestDatasetSourceResolveSubjectsDerivesFromTagsWithoutSymbolMapping(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{
		dataset:  &storagepb.Dataset{SpaceId: "crypto", SubjectTags: []string{"binance_spot"}},
		subjects: []*storagepb.Subject{{SpaceId: "crypto", SubjectId: "BTC-USDT", Name: "BTC/USDT", Status: "active"}},
	}}
	items, err := src.ResolveSubjects(context.Background(), "crypto", []string{"binance_spot"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "BTC-USDT", items[0].SubjectID)
}

func TestNormalizeTRPCTargetRawFormats(t *testing.T) {
	require.Equal(t, "ip://127.0.0.1:20100", normalizeTRPCTarget("", "20100"))
	require.Equal(t, "ip://10.0.0.1:20100", normalizeTRPCTarget("10.0.0.1:20100", "20100"))
	require.Equal(t, "ip://custom", normalizeTRPCTarget("ip://custom", "20100"))
}

func TestDirectMetadataTargetDerivesPrivateMetadataListener(t *testing.T) {
	require.Equal(t, "ip://146.56.196.204:20100", directMetadataTarget("ip://146.56.196.204:11003"))
	require.Empty(t, directMetadataTarget("http://127.0.0.1:11002"))
	t.Setenv("MOOX_COLLECTOR_STORAGE_METADATA_TARGET", "146.56.196.204:20100")
	require.Equal(t, "ip://146.56.196.204:20100", directMetadataTarget("ip://127.0.0.1:11003"))
}
