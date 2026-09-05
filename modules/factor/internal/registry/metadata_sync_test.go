package registry

import (
	"context"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncTargetDatasetReconcilesOutputsForActiveLockedTarget(t *testing.T) {
	client := newFakeMetadataClient()
	client.datasets["source"] = &storagepb.Dataset{
		SpaceId: "space", DatasetId: "source", DataSourceId: "market",
		DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, DataNodeId: "node-1",
		KeepDuration: "30d", Status: "active",
	}
	client.datasets["target"] = &storagepb.Dataset{
		SpaceId: "space", DatasetId: "target", DataSourceId: "market",
		DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, DataNodeId: "node-1",
		KeepDuration: "30d", Status: "active", BindingLocked: true,
	}
	client.subjects["source"] = []*storagepb.DatasetSubject{{
		SpaceId: "space", DatasetId: "source", SubjectId: "BTC-USDT",
	}}
	syncer := NewMetadataSync(client, nil)

	err := syncer.SyncTargetDataset(context.Background(), "space", "source", "target", "1m", []domain.FactorDef{{
		FactorID: "excess", Name: "ExcessReturn", InputColumns: []string{"nav", "benchmark_return"},
		Outputs: []string{"excess_return", "rolling_rank"}, ParamsJSON: `{}`,
		LookbackPeriods: 20, Status: domain.FactorStatusEnabled,
	}})
	require.NoError(t, err)
	require.Equal(t, []string{"excess_return", "rolling_rank"}, client.upsertedColumnNames())
	require.Zero(t, client.checkActivationCalls)
	require.Zero(t, client.activateCalls)
	require.Len(t, client.updatedDatasets, 1)
	require.Equal(t, "factor_result", client.updatedDatasets[0].GetAttributes()["dataset_role"])
	require.Equal(t, []string{"1m"}, client.updatedDatasets[0].GetFreqs())
	require.Len(t, client.boundSubjects, 1)
	require.Equal(t, "target", client.boundSubjects[0].GetDatasetId())
	require.Equal(t, "BTC-USDT", client.boundSubjects[0].GetSubjectId())
}

func TestResolveManagedResultIDsUsesSourceViewIdentity(t *testing.T) {
	client := &fakeViewMetadataClient{
		fakeMetadataClient: newFakeMetadataClient(),
		views: map[string]*storagepb.View{
			"view_crypto_spot_kline_1m": {
				SpaceId:          "space",
				ViewId:           "view_crypto_spot_kline_1m",
				PrimaryDatasetId: "dataset_binance_spot_kline_1m",
				DatasetIds:       []string{"dataset_binance_spot_kline_1m"},
			},
		},
	}
	datasetID, viewID, err := NewMetadataSync(client, nil).ResolveManagedResultIDs(
		context.Background(), "space", "view_crypto_spot_kline_1m",
	)
	require.NoError(t, err)
	require.Equal(t, "dataset_crypto_spot_kline_1m_factor", datasetID)
	require.Equal(t, "view_crypto_spot_kline_1m_factor", viewID)
}

func TestResolveManagedResultIDsSeparatesViewsSharingPrimaryDataset(t *testing.T) {
	client := &fakeViewMetadataClient{
		fakeMetadataClient: newFakeMetadataClient(),
		views: map[string]*storagepb.View{
			"view_a": {SpaceId: "space", ViewId: "view_a", PrimaryDatasetId: "bars"},
			"view_b": {SpaceId: "space", ViewId: "view_b", PrimaryDatasetId: "bars"},
		},
	}
	syncer := NewMetadataSync(client, nil)
	firstDataset, firstView, err := syncer.ResolveManagedResultIDs(context.Background(), "space", "view_a")
	require.NoError(t, err)
	secondDataset, secondView, err := syncer.ResolveManagedResultIDs(context.Background(), "space", "view_b")
	require.NoError(t, err)
	require.NotEqual(t, firstDataset, secondDataset)
	require.NotEqual(t, firstView, secondView)
}

func TestResultViewReadyWaitsForDesiredRevision(t *testing.T) {
	client := &fakeViewMetadataClient{
		fakeMetadataClient: newFakeMetadataClient(),
		views: map[string]*storagepb.View{
			"result_view": {
				SpaceId: "space", ViewId: "result_view", Status: "active", ActiveIndexId: "index-a",
				ActiveViewRevision: 1, DesiredViewRevision: 2,
				ActiveColumns: []*storagepb.ViewColumn{{ColumnName: "result.bias__bias_20"}},
			},
		},
	}
	ready, err := NewMetadataSync(client, nil).resultViewReady(context.Background(), domain.FactorBinding{
		SpaceID: "space", ResultDatasetID: "result", ResultViewID: "result_view",
	}, []domain.FactorDef{{FactorID: "bias", Outputs: []string{"bias_20"}}})
	require.NoError(t, err)
	require.False(t, ready)
}

func TestSyncTargetDatasetUpdatesExistingFactorMetadata(t *testing.T) {
	client := newFakeMetadataClient()
	client.datasets["source"] = testSourceDataset()
	client.factors["excess"] = &storagepb.Factor{SpaceId: "space", FactorId: "excess", Name: "Old"}
	syncer := NewMetadataSync(client, nil)

	err := syncer.SyncTargetDataset(context.Background(), "space", "source", "target", "1m", []domain.FactorDef{{
		FactorID: "excess", Name: "ExcessReturn", InputColumns: []string{"nav", "benchmark_return"},
		Outputs: []string{"excess_return", "rolling_rank"}, ParamsJSON: `{"window":20}`,
		LookbackPeriods: 21, Status: domain.FactorStatusEnabled,
	}})
	require.NoError(t, err)
	require.Empty(t, client.createdFactors)
	require.Len(t, client.updatedFactors, 1)
	updated := client.updatedFactors[0]
	require.Equal(t, `["nav","benchmark_return"]`, updated.GetAttributes()["input_columns_json"])
	require.Equal(t, `["excess_return","rolling_rank"]`, updated.GetAttributes()["outputs_json"])
	require.Equal(t, "21", updated.GetAttributes()["lookback_periods"])
	require.Equal(t, `{"window":20}`, updated.GetParamsJson())
	require.Equal(t, "active", updated.GetStatus())
	require.Len(t, client.upsertedColumns, 2)
	require.Equal(t, "excess.excess_return", client.upsertedColumns[0].GetOriginId())
	require.Equal(t, map[string]string{
		"display_name":     "excess_return",
		"origin_factor_id": "excess",
		"factor_output":    "excess_return",
	}, client.upsertedColumns[0].GetAttributes())
}

func TestSyncTargetDatasetCreatesMissingFactorMetadata(t *testing.T) {
	client := newFakeMetadataClient()
	client.datasets["source"] = testSourceDataset()
	syncer := NewMetadataSync(client, nil)

	err := syncer.SyncTargetDataset(context.Background(), "space", "source", "target", "1m", []domain.FactorDef{{
		FactorID: "excess", Name: "ExcessReturn", InputColumns: []string{"nav"},
		Outputs: []string{"excess_return"}, ParamsJSON: `{}`, LookbackPeriods: 5,
		Status: domain.FactorStatusDisabled,
	}})
	require.NoError(t, err)
	require.Len(t, client.createdFactors, 1)
	require.Empty(t, client.updatedFactors)
	require.Equal(t, "disabled", client.createdFactors[0].GetStatus())
}

func TestSyncTargetDatasetDoesNotCreateFactorForNonNotFoundRetInfo(t *testing.T) {
	for _, code := range []commonpb.ErrorCode{commonpb.ErrorCode_INVALID_PARAM, commonpb.ErrorCode_INNER_ERR} {
		t.Run(code.String(), func(t *testing.T) {
			client := newFakeMetadataClient()
			client.getFactorRet = &commonpb.RetInfo{Code: code, Msg: "factor not found"}
			syncer := NewMetadataSync(client, nil)

			err := syncer.SyncTargetDataset(context.Background(), "space", "source", "target", "1m", []domain.FactorDef{{
				FactorID: "excess", Name: "ExcessReturn", InputColumns: []string{"nav"},
				Outputs: []string{"excess_return"}, ParamsJSON: `{}`, LookbackPeriods: 5,
			}})
			require.ErrorContains(t, err, "GetFactor")
			require.Empty(t, client.createdFactors)
			require.Empty(t, client.updatedFactors)
		})
	}
}

func TestSyncTargetDatasetRejectsNonTimeSeriesSource(t *testing.T) {
	client := newFakeMetadataClient()
	client.datasets["source"] = testSourceDataset()
	client.datasets["source"].DataKind = storagepb.DataKind_DATA_KIND_RECORD
	syncer := NewMetadataSync(client, nil)

	err := syncer.SyncTargetDataset(context.Background(), "space", "source", "target", "1m", nil)
	require.ErrorContains(t, err, "must be time-series")
	require.Empty(t, client.updatedDatasets)
	require.Empty(t, client.upsertedColumns)
}

func TestSyncTargetDatasetRequiresExplicitSourceDataSourceID(t *testing.T) {
	client := newFakeMetadataClient()
	client.datasets["source"] = testSourceDataset()
	client.datasets["source"].DataSourceId = ""

	err := NewMetadataSync(client, nil).SyncTargetDataset(
		context.Background(), "space", "source", "target", "1m", nil,
	)
	require.ErrorContains(t, err, "data_source_id")
	require.Empty(t, client.updatedDatasets)
}

func TestSyncTargetDatasetRejectsExistingNonTimeSeriesTargetBeforeMutation(t *testing.T) {
	client := newFakeMetadataClient()
	client.datasets["source"] = testSourceDataset()
	client.datasets["target"] = &storagepb.Dataset{
		SpaceId: "space", DatasetId: "target", DataSourceId: "market",
		DataKind: storagepb.DataKind_DATA_KIND_RECORD, DataNodeId: "node-1",
		KeepDuration: "30d", Status: "active", BindingLocked: true,
	}
	client.subjects["source"] = []*storagepb.DatasetSubject{{
		SpaceId: "space", DatasetId: "source", SubjectId: "BTC-USDT",
	}}
	syncer := NewMetadataSync(client, nil)

	err := syncer.SyncTargetDataset(context.Background(), "space", "source", "target", "1m", nil)
	require.ErrorContains(t, err, "target dataset space/target must be time-series")
	require.Empty(t, client.updatedDatasets)
	require.Empty(t, client.boundSubjects)
	require.Empty(t, client.upsertedColumns)
	require.Zero(t, client.checkActivationCalls)
	require.Zero(t, client.activateCalls)
}

func testSourceDataset() *storagepb.Dataset {
	return &storagepb.Dataset{
		SpaceId: "space", DatasetId: "source", DataSourceId: "market",
		DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, DataNodeId: "node-1",
		KeepDuration: "30d", Status: "active",
	}
}

type fakeMetadataClient struct {
	datasets             map[string]*storagepb.Dataset
	factors              map[string]*storagepb.Factor
	subjects             map[string][]*storagepb.DatasetSubject
	upsertedColumns      []*storagepb.DatasetColumn
	updatedDatasets      []*storagepb.Dataset
	boundSubjects        []*storagepb.DatasetSubject
	createdFactors       []*storagepb.Factor
	updatedFactors       []*storagepb.Factor
	getFactorRet         *commonpb.RetInfo
	checkActivationCalls int
	activateCalls        int
}

type fakeViewMetadataClient struct {
	*fakeMetadataClient
	views map[string]*storagepb.View
}

func (f *fakeViewMetadataClient) CreateView(_ context.Context, req *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	f.views[req.GetView().GetViewId()] = req.GetView()
	return &storagepb.CreateViewRsp{RetInfo: successRet(), View: req.GetView()}, nil
}

func (f *fakeViewMetadataClient) GetView(_ context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	view := f.views[req.GetViewId()]
	if view == nil {
		return &storagepb.GetViewRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_FOUND}}, nil
	}
	return &storagepb.GetViewRsp{RetInfo: successRet(), View: view}, nil
}

func (f *fakeViewMetadataClient) UpsertViewColumn(_ context.Context, _ *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	return &storagepb.UpsertViewColumnRsp{RetInfo: successRet()}, nil
}

func newFakeMetadataClient() *fakeMetadataClient {
	return &fakeMetadataClient{
		datasets: map[string]*storagepb.Dataset{},
		factors:  map[string]*storagepb.Factor{},
		subjects: map[string][]*storagepb.DatasetSubject{},
	}
}

func (f *fakeMetadataClient) upsertedColumnNames() []string {
	names := make([]string, 0, len(f.upsertedColumns))
	for _, column := range f.upsertedColumns {
		names = append(names, column.GetColumnName())
	}
	sort.Strings(names)
	return names
}

func (f *fakeMetadataClient) CreateFactor(_ context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	f.createdFactors = append(f.createdFactors, req.GetFactor())
	f.factors[req.GetFactor().GetFactorId()] = req.GetFactor()
	return &storagepb.CreateFactorRsp{RetInfo: successRet()}, nil
}

func (f *fakeMetadataClient) UpdateFactor(_ context.Context, req *storagepb.UpdateFactorReq) (*storagepb.UpdateFactorRsp, error) {
	f.updatedFactors = append(f.updatedFactors, req.GetFactor())
	f.factors[req.GetFactor().GetFactorId()] = req.GetFactor()
	return &storagepb.UpdateFactorRsp{RetInfo: successRet()}, nil
}

func (f *fakeMetadataClient) GetFactor(_ context.Context, req *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error) {
	if f.getFactorRet != nil {
		return &storagepb.GetFactorRsp{RetInfo: f.getFactorRet}, nil
	}
	factor := f.factors[req.GetFactorId()]
	if factor == nil {
		return &storagepb.GetFactorRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_FACTOR_NOT_FOUND}}, nil
	}
	return &storagepb.GetFactorRsp{RetInfo: successRet(), Factor: factor}, nil
}

func (f *fakeMetadataClient) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	f.datasets[req.GetDataset().GetDatasetId()] = req.GetDataset()
	return &storagepb.CreateDatasetRsp{RetInfo: successRet()}, nil
}

func (f *fakeMetadataClient) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	f.updatedDatasets = append(f.updatedDatasets, req.GetDataset())
	f.datasets[req.GetDataset().GetDatasetId()] = req.GetDataset()
	return &storagepb.UpdateDatasetRsp{RetInfo: successRet()}, nil
}

func (f *fakeMetadataClient) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	dataset := f.datasets[req.GetDatasetId()]
	if dataset == nil {
		return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: successRet(), Dataset: dataset}, nil
}

func (f *fakeMetadataClient) CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	f.checkActivationCalls++
	return &storagepb.CheckDatasetActivationRsp{RetInfo: successRet(), Ready: true}, nil
}

func (f *fakeMetadataClient) ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	f.activateCalls++
	return &storagepb.ActivateDatasetRsp{RetInfo: successRet()}, nil
}

func (f *fakeMetadataClient) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.upsertedColumns = append(f.upsertedColumns, req.GetColumn())
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: successRet()}, nil
}

func (f *fakeMetadataClient) ListDatasetSubjects(_ context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	return &storagepb.ListDatasetSubjectsRsp{
		RetInfo: successRet(), DatasetSubjects: f.subjects[req.GetDatasetId()],
	}, nil
}

func (f *fakeMetadataClient) BindDatasetSubject(_ context.Context, req *storagepb.BindDatasetSubjectReq) (*storagepb.BindDatasetSubjectRsp, error) {
	f.boundSubjects = append(f.boundSubjects, req.GetDatasetSubject())
	return &storagepb.BindDatasetSubjectRsp{RetInfo: successRet()}, nil
}

func successRet() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
}

func TestDatasetDisplayName(t *testing.T) {
	name := datasetDisplayName("dataset_spot_kline_1h")
	assert.Contains(t, name, "因子")
	assert.LessOrEqual(t, len([]rune(name)), 10)
}

func TestFactorOutputAttributesUseDefinitionOutputAsDisplayName(t *testing.T) {
	assert.Equal(t, map[string]string{
		"display_name":     "bias_20",
		"origin_factor_id": "bias",
		"factor_output":    "bias_20",
	}, factorOutputAttributes("bias", "bias_20"))
}

func TestFactorResultAttributesAndMerge(t *testing.T) {
	attrs := factorResultDatasetAttributes("src_ds", "1m")
	assert.Equal(t, "factor", attrs["owner_module"])
	assert.Equal(t, "src_ds", attrs["source_dataset_id"])

	merged, err := mergeFactorResultDatasetAttributes("ds", nil, "src_ds", "1m")
	require.NoError(t, err)
	assert.Equal(t, "factor_result", merged["dataset_role"])

	_, err = mergeFactorResultDatasetAttributes("ds", map[string]string{
		"owner_module": "other",
	}, "src_ds", "1m")
	require.Error(t, err)

	keepFreq, err := mergeFactorResultDatasetAttributes("ds", map[string]string{
		"owner_module":      "factor",
		"dataset_role":      "factor_result",
		"managed_by":        "factor",
		"source_dataset_id": "src_ds",
		"source_freq":       "5m",
	}, "src_ds", "1m")
	require.NoError(t, err)
	assert.Equal(t, "5m", keepFreq["source_freq"])
}

func TestMergeDatasetFreq(t *testing.T) {
	assert.Equal(t, []string{"1m"}, mergeDatasetFreq(nil, "1m"))
	assert.Equal(t, []string{"1m"}, mergeDatasetFreq([]string{"1m"}, "1m"))
	assert.Equal(t, []string{"1m", "5m"}, mergeDatasetFreq([]string{"1m"}, "5m"))
	assert.Equal(t, []string{"1m"}, mergeDatasetFreq([]string{"1m"}, ""))
}

func TestFactorColumnOriginAndStatus(t *testing.T) {
	assert.Equal(t, "sma.output", factorColumnOriginID("sma", "output"))
	assert.Equal(t, "disabled", storageFactorStatus(domain.FactorStatusDisabled))
	assert.Equal(t, "active", storageFactorStatus("enabled"))
}

func TestRetInfoHelpers(t *testing.T) {
	assert.True(t, retOK(nil))
	assert.True(t, retOK(&commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}))
	assert.False(t, retOK(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM}))

	assert.False(t, isDuplicateRet(nil))
	assert.True(t, isDuplicateRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "duplicate key"}))
	assert.True(t, isDuplicateRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "Already Exists"}))
	assert.False(t, isDuplicateRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "duplicate"}))

	assert.False(t, isDatasetNotFoundRet(nil))
	assert.True(t, isDatasetNotFoundRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}))
	assert.True(t, isDatasetNotFoundRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "dataset not found"}))

	assert.False(t, isFactorNotFoundRet(nil))
	assert.True(t, isFactorNotFoundRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_FACTOR_NOT_FOUND}))
	assert.False(t, isFactorNotFoundRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "factor not found"}))

	assert.False(t, isRefreshInProgressRet(nil))
	assert.True(t, isRefreshInProgressRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "refresh already in progress"}))

	assert.Nil(t, retInfoError("op", nil))
	err := retInfoError("CreateFactor", &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CreateFactor")
}

func TestStringMapAndSliceHelpers(t *testing.T) {
	assert.Nil(t, cloneStringMap(nil))
	assert.Nil(t, cloneStringMap(map[string]string{}))
	cloned := cloneStringMap(map[string]string{"a": "1"})
	require.Equal(t, map[string]string{"a": "1"}, cloned)
	cloned["a"] = "2"
	assert.Equal(t, "1", map[string]string{"a": "1"}["a"])

	assert.True(t, stringMapsEqual(nil, nil))
	assert.False(t, stringMapsEqual(map[string]string{"a": "1"}, nil))
	assert.True(t, stringMapsEqual(map[string]string{"a": "1"}, map[string]string{"a": "1"}))
	assert.False(t, stringMapsEqual(map[string]string{"a": "1"}, map[string]string{"a": "2"}))

	assert.True(t, stringSlicesEqual(nil, nil))
	assert.False(t, stringSlicesEqual([]string{"a"}, nil))
	assert.True(t, stringSlicesEqual([]string{"a", "b"}, []string{"a", "b"}))
	assert.False(t, stringSlicesEqual([]string{"a"}, []string{"b"}))
}
