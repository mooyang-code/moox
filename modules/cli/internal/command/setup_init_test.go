package command

import (
	"bytes"
	"context"
	"errors"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSetupInitStorage struct {
	stages []string
	record func(string)
}

type fakeSetupInitFactor struct {
	items []setupFactorItem
}

func (f *fakeSetupInitFactor) Apply(_ context.Context, items []setupFactorItem) (setupFactorSummary, error) {
	f.items = append([]setupFactorItem(nil), items...)
	return setupFactorSummary{Enabled: true, Planned: len(items), Imported: len(items), Bound: len(items)}, nil
}

func (f *fakeSetupInitFactor) Close() error { return nil }

func (f *fakeSetupInitStorage) recordStage(stage string) {
	f.stages = append(f.stages, stage)
	if f.record != nil {
		f.record(stage)
	}
}

func (f *fakeSetupInitStorage) Apply(_ context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	f.recordStage("storage-apply")
	return metadataImportSummary{Status: "ok", Planned: len(calls), Applied: len(calls)}, nil
}

func (f *fakeSetupInitStorage) Activate(_ context.Context, datasets []seedDataset) (setupDatasetActivationSummary, error) {
	f.recordStage("dataset-activate")
	return setupDatasetActivationSummary{Total: len(datasets), Activated: len(datasets)}, nil
}

func (f *fakeSetupInitStorage) Verify(_ context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	f.recordStage("verify")
	return metadataImportSummary{Status: "ok", Planned: len(calls), Unchanged: len(calls)}, nil
}

func (f *fakeSetupInitStorage) Close() error { return nil }

func TestSetupInitRequiresStorageHost(t *testing.T) {
	cmd := newSetupCommand(setupDeps{})
	cmd.SetArgs([]string{"init"})
	require.Error(t, cmd.Execute())
}

func TestSetupFactorsCommandLoadsConfiguredSources(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.Factors.Enabled = true
	snapshot.Manifest.Factors.SourceDir = "../../../../examples/factors"
	snapshot.Manifest.Factors.Items = []setupconfig.FactorSetupItem{{
		FactorID: "bias", File: "timeseries/bias.py", InputColumns: []string{"close"},
		Outputs: []string{"bias_5"}, ParamsJSON: `{"windows":[5]}`, LookbackPeriods: 5,
		SpaceID: "crypto", SourceViewID: "binance_spot_kline_1m_view", Freq: "1m",
	}}
	factor := &fakeSetupInitFactor{}
	cmd := newSetupCommand(setupDeps{
		load:           func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		openInitFactor: func(context.Context, *setupconfig.Snapshot) (setupInitFactor, error) { return factor, nil },
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"factors", "--file", "custom.toml"})
	require.NoError(t, cmd.Execute())
	require.Len(t, factor.items, 1)
	assert.Equal(t, "bias", factor.items[0].FactorID)
	assert.Contains(t, output.String(), `"status":"ready"`)
}

func TestLoadSetupInitBundleUsesDefaultMetadata(t *testing.T) {
	bundle, err := loadSetupInitBundle(defaultSetupBundlePath())
	require.NoError(t, err)
	require.Len(t, bundle.Spaces, 4)
	require.NotEmpty(t, bundle.Calls)
	require.Len(t, bundle.Datasets, 18)
	assert.Equal(t, "crypto", bundle.Spaces[0].SpaceID)
	assert.Equal(t, "stock_cn", bundle.Spaces[1].SpaceID)
	assert.Equal(t, "stock_hk", bundle.Spaces[2].SpaceID)
	assert.Equal(t, "stock_us", bundle.Spaces[3].SpaceID)
}

func TestSetupInitRunsStagesInOrder(t *testing.T) {
	snapshot := setupSnapshot(t)
	stages := []string{}
	storage := &fakeSetupInitStorage{record: func(stage string) {
		stages = append(stages, stage)
	}}
	spaces := []setupclient.Space{{
		SpaceID: "crypto", Name: "加密货币市场", Market: "crypto",
		Timezone: "UTC", Status: "active", AttributesJSON: "{}",
	}}
	deps := setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) {
			stages = append(stages, "load-bundle")
			return setupInitBundle{
				Spaces: spaces,
				Datasets: []seedDataset{
					{SpaceID: "crypto", DatasetID: "spot_kline_1h"},
					{SpaceID: "crypto", DatasetID: "perpetual_kline_1h"},
				},
				Calls: make([]metadataImportCall, 3),
			}, nil
		},
		load: func(string) (*setupconfig.Snapshot, error) {
			stages = append(stages, "load-config")
			return snapshot, nil
		},
		applySpaces: func(_ context.Context, _ *setupconfig.Snapshot, got []setupclient.Space) (setupclient.ApplyResult, error) {
			stages = append(stages, "admin-apply")
			require.Equal(t, spaces, got)
			return setupclient.ApplyResult{Action: "created", Spaces: 1, SpacesCreated: 1}, nil
		},
		statusSpaces: func(_ context.Context, _ *setupconfig.Snapshot, got []setupclient.Space) (setupclient.StatusResult, error) {
			stages = append(stages, "admin-status")
			require.Equal(t, spaces, got)
			return setupclient.StatusResult{State: "completed", Spaces: 1}, nil
		},
		login: func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			stages = append(stages, "login")
			return setupclient.LoginResult{LoginAPI: "valid"}, nil
		},
		openInitStorage: func(context.Context, *setupconfig.Snapshot, string) (setupInitStorage, error) {
			return storage, nil
		},
	}
	var output bytes.Buffer
	cmd := newSetupCommand(deps)
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"init", "--file", "custom.toml", "--config-dir", "bundle", "--storage-host", "compute"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{
		"load-bundle",
		"load-config",
		"admin-apply",
		"admin-status",
		"login",
		"storage-apply",
		"dataset-activate",
		"verify",
		"admin-status",
	}, stages)
	require.JSONEq(t, `{
		"status":"ready",
		"business_spaces":1,
		"business_space_ids":["crypto"],
		"admin":{"action":"created","users":0,"secrets":0,"hosts":0,"spaces":1,"spaces_created":1,"spaces_unchanged":0},
		"admin_state":"completed",
		"login_api":"valid",
		"metadata":{"planned":3,"applied":3,"unchanged":0},
		"datasets":{"total":2,"activated":2,"unchanged":0},
		"verification":{"planned":3,"unchanged":3}
	}`, output.String())
}

func TestSetupInitStopsAfterAdminFailure(t *testing.T) {
	snapshot := setupSnapshot(t)
	stages := []string{}
	cmd := newSetupCommand(setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) {
			stages = append(stages, "load-bundle")
			return setupInitBundle{}, nil
		},
		load: func(string) (*setupconfig.Snapshot, error) {
			stages = append(stages, "load-config")
			return snapshot, nil
		},
		applySpaces: func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.ApplyResult, error) {
			stages = append(stages, "admin-apply")
			return setupclient.ApplyResult{}, errors.New("setup_conflict")
		},
	})
	cmd.SetArgs([]string{"init", "--storage-host", "compute"})
	require.ErrorContains(t, cmd.Execute(), "setup_conflict")
	assert.Equal(t, []string{"load-bundle", "load-config", "admin-apply"}, stages)
}

type fakeDatasetActivationAPI struct {
	datasets  map[string]*storagepb.Dataset
	checks    map[string]*storagepb.CheckDatasetActivationRsp
	activated []string
}

func (f *fakeDatasetActivationAPI) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	return &storagepb.GetDatasetRsp{RetInfo: storageOK(), Dataset: f.datasets[req.GetSpaceId()+"/"+req.GetDatasetId()]}, nil
}

func (f *fakeDatasetActivationAPI) CheckDatasetActivation(_ context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return f.checks[req.GetSpaceId()+"/"+req.GetDatasetId()], nil
}

func (f *fakeDatasetActivationAPI) ActivateDataset(_ context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	f.activated = append(f.activated, req.GetSpaceId()+"/"+req.GetDatasetId())
	return &storagepb.ActivateDatasetRsp{
		RetInfo: storageOK(),
		Dataset: &storagepb.Dataset{
			SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(),
			Status: "active", BindingLocked: true, Revision: req.GetExpectedRevision() + 1,
		},
	}, nil
}

func TestSetupInitActivatesReadyDatasets(t *testing.T) {
	api := &fakeDatasetActivationAPI{
		datasets: map[string]*storagepb.Dataset{
			"crypto/spot_kline_1h": {SpaceId: "crypto", DatasetId: "spot_kline_1h", Status: "disabled", Revision: 7},
		},
		checks: map[string]*storagepb.CheckDatasetActivationRsp{
			"crypto/spot_kline_1h": {RetInfo: storageOK(), DatasetRevision: 7, Ready: true},
		},
	}
	result, err := activateSetupDatasets(context.Background(), api, &storagepb.AuthInfo{}, []seedDataset{{
		SpaceID: "crypto", DatasetID: "spot_kline_1h",
	}})
	require.NoError(t, err)
	assert.Equal(t, setupDatasetActivationSummary{Total: 1, Activated: 1}, result)
	assert.Equal(t, []string{"crypto/spot_kline_1h"}, api.activated)
}

func TestSetupInitLeavesActiveDatasetsUnchanged(t *testing.T) {
	api := &fakeDatasetActivationAPI{
		datasets: map[string]*storagepb.Dataset{
			"crypto/spot_kline_1h": {
				SpaceId: "crypto", DatasetId: "spot_kline_1h",
				Status: "active", BindingLocked: true, Revision: 8,
			},
		},
		checks: map[string]*storagepb.CheckDatasetActivationRsp{},
	}
	result, err := activateSetupDatasets(context.Background(), api, &storagepb.AuthInfo{}, []seedDataset{{
		SpaceID: "crypto", DatasetID: "spot_kline_1h",
	}})
	require.NoError(t, err)
	assert.Equal(t, setupDatasetActivationSummary{Total: 1, Unchanged: 1}, result)
	assert.Empty(t, api.activated)
}

func TestSetupInitReportsDatasetReadinessChecks(t *testing.T) {
	api := &fakeDatasetActivationAPI{
		datasets: map[string]*storagepb.Dataset{
			"crypto/spot_kline_1h": {SpaceId: "crypto", DatasetId: "spot_kline_1h", Status: "disabled", Revision: 7},
		},
		checks: map[string]*storagepb.CheckDatasetActivationRsp{
			"crypto/spot_kline_1h": {
				RetInfo: storageOK(), DatasetRevision: 7, Ready: false,
				Checks: []*storagepb.DatasetActivationCheck{{
					CheckId: "data_node_ready", Ready: false, Summary: "storage-node-0 unavailable",
				}},
			},
		},
	}
	_, err := activateSetupDatasets(context.Background(), api, &storagepb.AuthInfo{}, []seedDataset{{
		SpaceID: "crypto", DatasetID: "spot_kline_1h",
	}})
	require.ErrorContains(t, err, "data_node_ready=not_ready(storage-node-0 unavailable)")
	assert.Empty(t, api.activated)
}

func TestSetupInitRejectsMissingMetadataDependencies(t *testing.T) {
	err := validateSetupMetadataDependencies(metadataSeed{
		Spaces: []seedSpace{{SpaceID: "crypto"}},
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "missing",
		}},
	})
	require.ErrorContains(t, err, `undefined data_source "missing"`)
}

func TestSetupInitRejectsMissingDatasetColumnOrigin(t *testing.T) {
	base := metadataSeed{
		Spaces:      []seedSpace{{SpaceID: "crypto"}},
		DataSources: []seedDataSource{{SpaceID: "crypto", DataSourceID: "market"}},
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "market",
			DataKind: "time_series", Freqs: []string{"1H"},
		}},
	}
	tests := []struct {
		name   string
		column seedDatasetColumn
		want   string
	}{
		{
			name: "field",
			column: seedDatasetColumn{
				SpaceID: "crypto", DatasetID: "kline", ColumnName: "close",
				OriginType: "field", OriginID: "missing",
			},
			want: `references undefined field "missing"`,
		},
		{
			name: "factor",
			column: seedDatasetColumn{
				SpaceID: "crypto", DatasetID: "kline", ColumnName: "ma",
				OriginType: "factor", OriginID: "missing",
			},
			want: `references undefined factor "missing"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seed := base
			seed.DatasetColumns = []seedDatasetColumn{tc.column}
			err := validateSetupMetadataDependencies(seed)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestSetupInitRejectsDuplicateDatasetAndViewColumns(t *testing.T) {
	base := metadataSeed{
		Spaces:      []seedSpace{{SpaceID: "crypto"}},
		DataSources: []seedDataSource{{SpaceID: "crypto", DataSourceID: "market"}},
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "market",
			DataKind: "time_series", Freqs: []string{"1H"},
		}},
		Fields: []seedField{{SpaceID: "crypto", FieldID: "close"}},
		DatasetColumns: []seedDatasetColumn{
			{SpaceID: "crypto", DatasetID: "kline", ColumnName: "close", OriginType: "field", OriginID: "close"},
			{SpaceID: "crypto", DatasetID: "kline", ColumnName: "close", OriginType: "field", OriginID: "close"},
		},
	}
	require.ErrorContains(t, validateSetupMetadataDependencies(base), "duplicate metadata dataset_column")

	base.DatasetColumns = base.DatasetColumns[:1]
	base.Views = []seedView{testCanonicalTimeSeriesView("crypto", "kline", "kline", "1H")}
	base.ViewColumns = []seedViewColumn{
		{SpaceID: "crypto", ViewID: "kline", ColumnName: "kline.close", OriginType: "dataset_column", OriginID: "kline.close"},
		{SpaceID: "crypto", ViewID: "kline", ColumnName: "kline.close", OriginType: "dataset_column", OriginID: "kline.close"},
	}
	require.ErrorContains(t, validateSetupMetadataDependencies(base), "duplicate metadata view_column")
}

func TestSetupInitRejectsMissingViewColumnOrigin(t *testing.T) {
	err := validateSetupMetadataDependencies(metadataSeed{
		Spaces:      []seedSpace{{SpaceID: "crypto"}},
		DataSources: []seedDataSource{{SpaceID: "crypto", DataSourceID: "market"}},
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "market",
			DataKind: "time_series", Freqs: []string{"1H"},
		}},
		Views: []seedView{testCanonicalTimeSeriesView("crypto", "kline", "kline", "1H")},
		ViewColumns: []seedViewColumn{{
			SpaceID: "crypto", ViewID: "kline", ColumnName: "kline.close",
			OriginType: "dataset_column", OriginID: "kline.close",
		}},
	})
	require.ErrorContains(t, err, `references undefined dataset_column "kline.close"`)
}

func TestSetupInitRejectsViewColumnFromUndeclaredDataset(t *testing.T) {
	err := validateSetupMetadataDependencies(metadataSeed{
		Spaces:      []seedSpace{{SpaceID: "crypto"}},
		DataSources: []seedDataSource{{SpaceID: "crypto", DataSourceID: "market"}},
		Datasets: []seedDataset{
			{SpaceID: "crypto", DatasetID: "spot", DataSourceID: "market", DataKind: "time_series", Freqs: []string{"1H"}},
			{SpaceID: "crypto", DatasetID: "perpetual", DataSourceID: "market", DataKind: "time_series", Freqs: []string{"1H"}},
		},
		Fields: []seedField{{SpaceID: "crypto", FieldID: "close"}},
		DatasetColumns: []seedDatasetColumn{
			{SpaceID: "crypto", DatasetID: "spot", ColumnName: "close", OriginType: "field", OriginID: "close"},
			{SpaceID: "crypto", DatasetID: "perpetual", ColumnName: "close", OriginType: "field", OriginID: "close"},
		},
		Views: []seedView{testCanonicalTimeSeriesView("crypto", "spot_view", "spot", "1H")},
		ViewColumns: []seedViewColumn{{
			SpaceID: "crypto", ViewID: "spot_view", ColumnName: "perpetual.close",
			OriginType: "dataset_column", OriginID: "perpetual.close",
		}},
	})
	require.ErrorContains(t, err, `references dataset "perpetual" not declared by view`)
}

func TestSetupInitRejectsViewsThatStorageWouldNormalize(t *testing.T) {
	base := metadataSeed{
		Spaces:      []seedSpace{{SpaceID: "crypto"}},
		DataSources: []seedDataSource{{SpaceID: "crypto", DataSourceID: "market"}},
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "kline", DataSourceID: "market",
			DataKind: "time_series", Freqs: []string{"1H"},
		}},
	}
	canonical := seedView{
		SpaceID: "crypto", ViewID: "kline_view", PrimaryDatasetID: "kline",
		DatasetIDs: []string{"kline"},
		GrainKeys:  []string{"subject_id", "freq", "data_time", "series_tag"},
		FilterJSON: `{"freq":"1H"}`,
		Engine:     "duckdb",
	}
	tests := []struct {
		name string
		view seedView
		want string
	}{
		{
			name: "missing primary",
			view: func() seedView {
				item := canonical
				item.PrimaryDatasetID = ""
				return item
			}(),
			want: "primary_dataset_id must be explicit",
		},
		{
			name: "dataset order",
			view: func() seedView {
				item := canonical
				item.DatasetIDs = []string{"kline", "kline"}
				return item
			}(),
			want: "dataset_ids must be canonical",
		},
		{
			name: "grain keys",
			view: func() seedView {
				item := canonical
				item.GrainKeys = []string{"subject_id", "data_time"}
				return item
			}(),
			want: "grain_keys must be canonical",
		},
		{
			name: "engine",
			view: func() seedView {
				item := canonical
				item.Engine = "pebble"
				return item
			}(),
			want: `engine must be "duckdb"`,
		},
		{
			name: "filter json",
			view: func() seedView {
				item := canonical
				item.FilterJSON = `{ "freq": "1H" }`
				return item
			}(),
			want: "filter_json must be canonical",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seed := base
			seed.Views = []seedView{tc.view}
			require.ErrorContains(t, validateSetupMetadataDependencies(seed), tc.want)
		})
	}
	seed := base
	seed.Views = []seedView{canonical}
	require.NoError(t, validateSetupMetadataDependencies(seed))

	seed.Datasets[0].Freqs = []string{" 1H "}
	require.NoError(t, validateSetupMetadataDependencies(seed))
}

func testCanonicalTimeSeriesView(spaceID, viewID, datasetID, freq string) seedView {
	return seedView{
		SpaceID: spaceID, ViewID: viewID, PrimaryDatasetID: datasetID,
		DatasetIDs: []string{datasetID},
		GrainKeys:  []string{"subject_id", "freq", "data_time", "series_tag"},
		FilterJSON: `{"freq":"` + freq + `"}`,
		Engine:     "duckdb",
	}
}

func TestSetupInitJSONDoesNotExposeSecrets(t *testing.T) {
	snapshot := setupSnapshot(t)
	storage := &fakeSetupInitStorage{}
	cmd := newSetupCommand(setupDeps{
		loadInitBundle: func(string) (setupInitBundle, error) { return setupInitBundle{}, nil },
		load:           func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		applySpaces: func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.ApplyResult, error) {
			return setupclient.ApplyResult{Action: "unchanged"}, nil
		},
		statusSpaces: func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.StatusResult, error) {
			return setupclient.StatusResult{State: "completed"}, nil
		},
		login: func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			return setupclient.LoginResult{LoginAPI: "valid"}, nil
		},
		openInitStorage: func(context.Context, *setupconfig.Snapshot, string) (setupInitStorage, error) {
			return storage, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"init", "--storage-host", "compute"})
	require.NoError(t, cmd.Execute())
	for _, secret := range []string{
		"admin-test-password", "control-ssh-password", "other-ssh-password",
		"AKID-test-secret", "cloud-test-secret",
	} {
		assert.NotContains(t, output.String(), secret)
	}
}
