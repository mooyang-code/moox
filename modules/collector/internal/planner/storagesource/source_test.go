package storagesource

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeMetadataClient struct {
	dataset     *storagepb.Dataset
	allSubjects []*storagepb.Subject
	subjects    []*storagepb.DatasetSubject
	symbols     []*storagepb.SubjectSymbol
}

func (f *fakeMetadataClient) ListSubjects(_ context.Context, _ *storagepb.ListSubjectsReq, _ ...client.Option) (*storagepb.ListSubjectsRsp, error) {
	return &storagepb.ListSubjectsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Subjects: f.allSubjects, PageResult: &storagepb.PageResult{HasMore: false}}, nil
}

func (f *fakeMetadataClient) GetDataset(_ context.Context, _ *storagepb.GetDatasetReq, _ ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return &storagepb.GetDatasetRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Dataset: f.dataset,
	}, nil
}

func (f *fakeMetadataClient) ListDatasetSubjects(_ context.Context, _ *storagepb.ListDatasetSubjectsReq, _ ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return &storagepb.ListDatasetSubjectsRsp{
		RetInfo:         &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		DatasetSubjects: f.subjects,
		PageResult:      &storagepb.PageResult{HasMore: false},
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
	src := &DatasetSource{metadata: &fakeMetadataClient{}}
	_, err := src.ListSubjects(context.Background(), "space-1", "", "ds-src")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dataset_id is required")
}

func TestDatasetSourceGetDatasetReturnsValidationContract(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{dataset: &storagepb.Dataset{
		DataSourceId: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, Attributes: map[string]string{"market_type": "spot"},
	}}}
	info, err := src.GetDataset(context.Background(), "crypto", "kline")
	require.NoError(t, err)
	assert.Equal(t, "binance", info.DataSourceID)
	assert.Equal(t, storagepb.DataKind_DATA_KIND_TIME_SERIES, info.DataKind)
	assert.Equal(t, "active", info.Status)
	assert.Equal(t, []string{"1m"}, info.Freqs)
	assert.Equal(t, "spot", info.Attributes["market_type"])
}

func TestDatasetSource_ListSubjects_ValidBindings_ShouldMergeSymbols(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{
		subjects: []*storagepb.DatasetSubject{
			{SubjectId: "BTC-USDT", Status: "active"},
			{SubjectId: "ETH-USDT", Status: "inactive"},
		},
		symbols: []*storagepb.SubjectSymbol{
			{SubjectId: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
		},
	}}
	subjects, err := src.ListSubjects(context.Background(), "space-1", "ds-1", "src-1")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTC-USDT", subjects[0].SubjectID)
	assert.Equal(t, "BTCUSDT", subjects[0].ExternalSymbol)
	assert.Equal(t, "active", subjects[0].Status)
}

func TestDatasetSource_ListResampleSubjectsUsesSharedActiveSubjectSet(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{
		dataset:     &storagepb.Dataset{DataSourceId: "crypto", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
		allSubjects: []*storagepb.Subject{{SubjectId: "BTC-USDT", Name: "BTC/USDT", Status: "active"}, {SubjectId: "ETH-USDT", Status: "inactive"}},
	}}
	subjects, err := src.ListResampleSubjects(context.Background(), "crypto", "dataset_spot_kline_1h")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTC-USDT", subjects[0].SubjectID)
	assert.Equal(t, "BTC-USDT", subjects[0].ExternalSymbol)
}

func TestDatasetSource_ListResampleSubjectsForRuleFiltersSharedActiveSubjectSetByVenue(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{
		dataset:     &storagepb.Dataset{DataSourceId: "crypto", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
		allSubjects: []*storagepb.Subject{{SubjectId: "BTC-USDT", Status: "active"}, {SubjectId: "OKX-ONLY", Status: "active"}},
		symbols:     []*storagepb.SubjectSymbol{{SubjectId: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}},
	}}
	subjects, err := src.ListResampleSubjectsForRule(context.Background(), "crypto", "dataset_spot_kline_1h", "moox", "venue:binance")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTC-USDT", subjects[0].SubjectID)
	assert.Equal(t, "BTCUSDT", subjects[0].ExternalSymbol)
}

func TestDatasetSource_ListResampleSubjectsProviderUsesOwnSymbols(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{
		dataset:     &storagepb.Dataset{DataSourceId: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
		allSubjects: []*storagepb.Subject{{SubjectId: "BTC-USDT", Status: "active"}, {SubjectId: "OKX-ONLY", Status: "active"}},
		symbols:     []*storagepb.SubjectSymbol{{SubjectId: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}},
	}}
	subjects, err := src.ListResampleSubjects(context.Background(), "crypto", "dataset_binance_spot_kline_1m")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTC-USDT", subjects[0].SubjectID)
	assert.Equal(t, "BTCUSDT", subjects[0].ExternalSymbol)
}

func TestDatasetSource_ListResampleSubjectsProviderBindingsKeepExternalSymbols(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{
		dataset:  &storagepb.Dataset{DataSourceId: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
		subjects: []*storagepb.DatasetSubject{{SubjectId: "BTC-USDT", Status: "active"}},
		symbols:  []*storagepb.SubjectSymbol{{SubjectId: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}},
	}}
	subjects, err := src.ListResampleSubjects(context.Background(), "crypto", "dataset_binance_spot_kline_1m")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTCUSDT", subjects[0].ExternalSymbol)
}

func TestMergeDatasetSubjectsBySymbolKeepsExplicitDisjointBindingsEmpty(t *testing.T) {
	subjects, err := mergeDatasetSubjectsBySymbol([]*storagepb.DatasetSubject{
		{SubjectId: "OKX-ONLY", Status: "active"},
	}, map[string]string{"BTC-USDT": "BTCUSDT"})
	require.NoError(t, err)
	assert.Empty(t, subjects)
}

func TestMergeDatasetSubjectsBySymbolKeepsAllDisabledBindingsEmpty(t *testing.T) {
	subjects, err := mergeDatasetSubjectsBySymbol([]*storagepb.DatasetSubject{
		{SubjectId: "BTC-USDT", Status: "disabled"},
	}, map[string]string{"BTC-USDT": "BTCUSDT"})
	require.NoError(t, err)
	assert.Empty(t, subjects)
}

func TestMergeDatasetSubjectsRequiresActiveSubjectSymbol(t *testing.T) {
	_, err := mergeDatasetSubjects([]*storagepb.DatasetSubject{
		{SubjectId: "BTC-USDT", Status: "active", Attributes: map[string]string{"external_symbol": "BTCUSDT_ATTR"}},
	}, nil)
	require.ErrorContains(t, err, "no active external symbol")

	subjects, err := mergeDatasetSubjects([]*storagepb.DatasetSubject{
		{SubjectId: "BTC-USDT", Status: "active"},
	}, map[string]string{"BTC-USDT": "BTCUSDT_MAP"})
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	assert.Equal(t, "BTCUSDT_MAP", subjects[0].ExternalSymbol)
}

func TestDatasetSourceRejectsDuplicateActiveSubjectSymbols(t *testing.T) {
	src := &DatasetSource{metadata: &fakeMetadataClient{
		subjects: []*storagepb.DatasetSubject{{SubjectId: "BTC-USDT", Status: "active"}},
		symbols: []*storagepb.SubjectSymbol{
			{SubjectId: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
			{SubjectId: "BTC-USDT", ExternalSymbol: "BTCUSDT2", Status: "active"},
		},
	}}
	_, err := src.ListSubjects(context.Background(), "crypto", "symbols", "binance")
	require.ErrorContains(t, err, "duplicate active external symbols")
}

func TestIsActiveRequiresLiteralActiveStatus(t *testing.T) {
	assert.True(t, isActive("active"))
	assert.True(t, isActive(" ACTIVE "))
	assert.False(t, isActive("enabled"))
	assert.False(t, isActive(""))
	assert.False(t, isActive("inactive"))
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
