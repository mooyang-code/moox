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
}

func (f *fakeSetupInitStorage) Apply(_ context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	f.stages = append(f.stages, "storage-apply")
	return metadataImportSummary{Status: "ok", Planned: len(calls), Applied: len(calls)}, nil
}

func (f *fakeSetupInitStorage) Activate(_ context.Context, datasets []seedDataset) (setupDatasetActivationSummary, error) {
	f.stages = append(f.stages, "dataset-activate")
	return setupDatasetActivationSummary{Total: len(datasets), Activated: len(datasets)}, nil
}

func (f *fakeSetupInitStorage) Verify(_ context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	f.stages = append(f.stages, "verify")
	return metadataImportSummary{Status: "ok", Planned: len(calls), Unchanged: len(calls)}, nil
}

func (f *fakeSetupInitStorage) Close() error { return nil }

func TestSetupInitRequiresStorageHost(t *testing.T) {
	cmd := newSetupCommand(setupDeps{})
	cmd.SetArgs([]string{"init"})
	require.Error(t, cmd.Execute())
}

func TestLoadSetupInitBundleUsesDefaultMetadata(t *testing.T) {
	bundle, err := loadSetupInitBundle(defaultSetupBundlePath())
	require.NoError(t, err)
	require.Len(t, bundle.Spaces, 2)
	require.NotEmpty(t, bundle.Calls)
	require.Len(t, bundle.Datasets, 11)
	assert.Equal(t, "crypto", bundle.Spaces[0].SpaceID)
	assert.Equal(t, "stock_cn", bundle.Spaces[1].SpaceID)
}

func TestSetupInitRunsStagesInOrder(t *testing.T) {
	snapshot := setupSnapshot(t)
	stages := []string{}
	storage := &fakeSetupInitStorage{}
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
	stages = append(stages, storage.stages...)
	assert.Equal(t, []string{
		"load-bundle",
		"load-config",
		"admin-apply",
		"admin-status",
		"login",
		"storage-apply",
		"dataset-activate",
		"verify",
	}, stages)
	require.JSONEq(t, `{
		"status":"ready",
		"business_spaces":1,
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
