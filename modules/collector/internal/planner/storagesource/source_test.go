package storagesource

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeMetadataClient struct {
	subjects []*storagepb.DatasetSubject
	symbols  []*storagepb.SubjectSymbol
}

func (f *fakeMetadataClient) ListDatasetSubjects(_ context.Context, _ *storagepb.ListDatasetSubjectsReq, _ ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return &storagepb.ListDatasetSubjectsRsp{
		RetInfo:          &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		DatasetSubjects:  f.subjects,
		PageResult:       &storagepb.PageResult{HasMore: false},
	}, nil
}

func (f *fakeMetadataClient) ListSubjectSymbols(_ context.Context, _ *storagepb.ListSubjectSymbolsReq, _ ...client.Option) (*storagepb.ListSubjectSymbolsRsp, error) {
	return &storagepb.ListSubjectSymbolsRsp{
		RetInfo:        &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		SubjectSymbols: f.symbols,
		PageResult:     &storagepb.PageResult{HasMore: false},
	}, nil
}

func TestDatasetSource_ListSubjects_EmptyDatasetID_ShouldReturnError(t *testing.T) {
	src := newDatasetSourceWithClient(&fakeMetadataClient{})
	_, err := src.ListSubjects(context.Background(), "space-1", "", "ds-src")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dataset_id is required")
}

func TestDatasetSource_ListSubjects_ValidBindings_ShouldMergeSymbols(t *testing.T) {
	src := newDatasetSourceWithClient(&fakeMetadataClient{
		subjects: []*storagepb.DatasetSubject{
			{SubjectId: "BTC-USDT", Status: "active"},
			{SubjectId: "ETH-USDT", Status: "inactive"},
		},
		symbols: []*storagepb.SubjectSymbol{
			{SubjectId: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
		},
	})
	subjects, err := src.ListSubjects(context.Background(), "space-1", "ds-1", "src-1")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTCUSDT", subjects[0].ExternalSymbol)
}

func TestMergeDatasetSubjects_AttributeSymbol_ShouldTakePrecedence(t *testing.T) {
	subjects := mergeDatasetSubjects([]*storagepb.DatasetSubject{
		{SubjectId: "BTC-USDT", Status: "active", Attributes: map[string]string{"external_symbol": "BTCUSDT_ATTR"}},
	}, map[string]string{"BTC-USDT": "BTCUSDT_MAP"})
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTCUSDT_ATTR", subjects[0].ExternalSymbol)
}

func TestIsInactive_StatusValues_ShouldClassifyCorrectly(t *testing.T) {
	assert.False(t, isInactive("active"))
	assert.False(t, isInactive("enabled"))
	assert.False(t, isInactive(""))
	assert.True(t, isInactive("inactive"))
}

func TestEnsureStorageOK_ErrorCode_ShouldReturnError(t *testing.T) {
	err := ensureStorageOK("list subjects", &storagepb.RetInfo{Code: 1, Msg: "boom"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestNormalizeTRPCTarget_RawFormats_ShouldNormalize(t *testing.T) {
	assert.Equal(t, "ip://127.0.0.1:20100", normalizeTRPCTarget("", "20100"))
	assert.Equal(t, "ip://10.0.0.1:20100", normalizeTRPCTarget("10.0.0.1:20100", "20100"))
	assert.Equal(t, "ip://custom", normalizeTRPCTarget("ip://custom", "20100"))
}
