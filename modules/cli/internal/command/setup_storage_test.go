package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestSetupStorageCommandsRequireExplicitHostAndSanitizeResults(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	const secret = "admin-test-password"
	verifyCalls, e2eCalls, browserCalls := 0, 0, 0
	deps := setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		verifyStorage: func(_ context.Context, _ *setupconfig.Snapshot, host string) (storageVerifyResult, error) {
			verifyCalls++
			require.Equal(t, "compute", host)
			return storageVerifyResult{
				Status: "passed", Commit: "0123456789abcdef0123456789abcdef01234567", SchemaVersion: 5,
				Components:   map[string]storageComponent{"storage-primary": {Status: "ready"}},
				BinaryHashes: map[string]string{"moox-storage-node": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				DataNode:     storageDataNodeIdentity{NodeID: "node-1", Status: "READY"}, NodeCount: 1, DatasetCount: 2,
			}, nil
		},
		e2eStorage: func(_ context.Context, _ *setupconfig.Snapshot, host, namespace string) (storageE2EResult, error) {
			e2eCalls++
			require.Equal(t, "compute", host)
			require.Equal(t, "task16", namespace)
			return storageE2EResult{Status: "passed", Namespace: namespace, Assertions: []string{"space_created", "dataset_activated_locked"}, Cleanup: "completed"}, nil
		},
		browserE2EStorage: func(_ context.Context, _ *setupconfig.Snapshot, host, root string) (storageBrowserResult, error) {
			browserCalls++
			require.Equal(t, "compute", host)
			require.Equal(t, "/repo", root)
			return storageBrowserResult{Status: "passed", Desktop: "passed", Mobile: "passed"}, nil
		},
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "verify", args: []string{"verify-storage", "--file", "custom.toml", "--host", "compute"}, want: "schema_version"},
		{name: "e2e", args: []string{"e2e-storage", "--file", "custom.toml", "--host", "compute", "--namespace", "task16"}, want: "cleanup"},
		{name: "browser", args: []string{"browser-e2e-storage", "--file", "custom.toml", "--host", "compute", "--repo-root", "/repo"}, want: "desktop"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := newSetupCommand(deps)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs(test.args)
			require.NoError(t, cmd.Execute())
			require.Contains(t, output.String(), test.want)
			require.NotContains(t, output.String(), secret)
			require.NotContains(t, output.String(), "custom.toml")
			require.NotContains(t, output.String(), "/repo")
		})
	}
	require.Equal(t, 1, verifyCalls)
	require.Equal(t, 1, e2eCalls)
	require.Equal(t, 1, browserCalls)
}

func TestSetupStorageCommandsRejectMissingRequiredFlags(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	deps := setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }}
	for _, args := range [][]string{
		{"verify-storage", "--file", "custom.toml"},
		{"e2e-storage", "--file", "custom.toml", "--host", "compute"},
		{"browser-e2e-storage", "--file", "custom.toml", "--host", "compute"},
	} {
		cmd := newSetupCommand(deps)
		cmd.SetArgs(args)
		require.Error(t, cmd.Execute(), args)
	}
}

func TestStorageTargetAddressAndNamespaceValidation(t *testing.T) {
	t.Parallel()
	address, err := storageTargetAddress("ip://127.0.0.1:20107")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:20107", address)
	for _, raw := range []string{"", "http://127.0.0.1:20107", "ip://127.0.0.1", "ip://127.0.0.1:0", "ip://127.0.0.1:70000", "ip://user@127.0.0.1:20107/path"} {
		_, err := storageTargetAddress(raw)
		require.Error(t, err, raw)
	}
	for _, namespace := range []string{"task16", "a-1", "_bad", "bad/value", ""} {
		err := validateStorageNamespace(namespace)
		if namespace == "task16" || namespace == "a-1" {
			require.NoError(t, err, namespace)
		} else {
			require.Error(t, err, namespace)
		}
	}
}

func TestStorageLifecycleCreatesActivatesAndDisablesIsolatedRows(t *testing.T) {
	t.Parallel()
	api := &fakeStorageMetadataAPI{}
	session := &remoteStorageSession{metadata: api, auth: &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: "signed"}}
	result, err := runStorageLifecycle(context.Background(), session, "task16")
	require.NoError(t, err)
	require.Equal(t, "passed", result.Status)
	require.Equal(t, "completed", result.Cleanup)
	require.Equal(t, []string{"space_created", "data_source_created", "dataset_created_disabled", "activation_checks_passed", "dataset_activated_locked"}, result.Assertions)
	require.Equal(t, []string{"active", "disabled"}, api.spaceStatuses)
	require.Equal(t, []string{"active", "disabled"}, api.sourceStatuses)
	require.Equal(t, []string{"disabled", "disabled"}, api.datasetStatuses)
	for _, auth := range api.auths {
		require.Equal(t, "storage-metadata", auth.GetAppId())
		require.Equal(t, "signed", auth.GetAppKey())
	}
	var encoded map[string]any
	require.NoError(t, json.Unmarshal(mustJSON(t, result), &encoded))
	require.NotContains(t, string(mustJSON(t, result)), "task16_space")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

type fakeStorageMetadataAPI struct {
	spaceStatuses   []string
	sourceStatuses  []string
	datasetStatuses []string
	auths           []*storagepb.AuthInfo
}

func (f *fakeStorageMetadataAPI) remember(auth *storagepb.AuthInfo) { f.auths = append(f.auths, auth) }
func storageOK() *storagepb.RetInfo                                 { return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS} }

func (f *fakeStorageMetadataAPI) CreateSpace(_ context.Context, req *storagepb.CreateSpaceReq) (*storagepb.CreateSpaceRsp, error) {
	f.remember(req.GetAuthInfo())
	f.spaceStatuses = append(f.spaceStatuses, req.GetSpace().GetStatus())
	return &storagepb.CreateSpaceRsp{RetInfo: storageOK(), Space: req.GetSpace()}, nil
}
func (f *fakeStorageMetadataAPI) UpdateSpace(_ context.Context, req *storagepb.UpdateSpaceReq) (*storagepb.UpdateSpaceRsp, error) {
	f.remember(req.GetAuthInfo())
	f.spaceStatuses = append(f.spaceStatuses, req.GetSpace().GetStatus())
	return &storagepb.UpdateSpaceRsp{RetInfo: storageOK(), Space: req.GetSpace()}, nil
}
func (f *fakeStorageMetadataAPI) CreateDataSource(_ context.Context, req *storagepb.CreateDataSourceReq) (*storagepb.CreateDataSourceRsp, error) {
	f.remember(req.GetAuthInfo())
	f.sourceStatuses = append(f.sourceStatuses, req.GetDataSource().GetStatus())
	return &storagepb.CreateDataSourceRsp{RetInfo: storageOK(), DataSource: req.GetDataSource()}, nil
}
func (f *fakeStorageMetadataAPI) UpdateDataSource(_ context.Context, req *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error) {
	f.remember(req.GetAuthInfo())
	f.sourceStatuses = append(f.sourceStatuses, req.GetDataSource().GetStatus())
	return &storagepb.UpdateDataSourceRsp{RetInfo: storageOK(), DataSource: req.GetDataSource()}, nil
}
func (f *fakeStorageMetadataAPI) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	f.datasetStatuses = append(f.datasetStatuses, req.GetDataset().GetStatus())
	dataset := *req.GetDataset()
	dataset.Revision = 7
	return &storagepb.CreateDatasetRsp{RetInfo: storageOK(), Dataset: &dataset}, nil
}
func (f *fakeStorageMetadataAPI) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	f.datasetStatuses = append(f.datasetStatuses, req.GetDataset().GetStatus())
	return &storagepb.UpdateDatasetRsp{RetInfo: storageOK(), Dataset: req.GetDataset()}, nil
}
func (f *fakeStorageMetadataAPI) ListDataNodes(_ context.Context, req *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.ListDataNodesRsp{RetInfo: storageOK(), Items: []*storagepb.DataNodeListItem{{Node: &storagepb.DataNode{NodeId: "node-1", Status: "active"}}}, PageResult: &storagepb.PageResult{}}, nil
}
func (f *fakeStorageMetadataAPI) CheckDatasetActivation(_ context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.CheckDatasetActivationRsp{RetInfo: storageOK(), DatasetRevision: 7, Ready: true, Checks: []*storagepb.DatasetActivationCheck{{CheckId: "internal-id", Ready: true}}}, nil
}
func (f *fakeStorageMetadataAPI) ActivateDataset(_ context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.ActivateDatasetRsp{RetInfo: storageOK(), Dataset: &storagepb.Dataset{DatasetId: req.GetDatasetId(), Status: "active", BindingLocked: true, Revision: req.GetExpectedRevision() + 1}}, nil
}
