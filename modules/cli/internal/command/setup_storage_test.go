package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
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

func TestStorageBrowserBaseURLKeepsPublicHostOnTheLocalForwardPort(t *testing.T) {
	t.Parallel()

	baseURL, err := storageBrowserBaseURL("106.53.107.122", "127.0.0.1:43127")

	require.NoError(t, err)
	require.Equal(t, "https://106.53.107.122:43127", baseURL)
	for _, input := range [][2]string{
		{"", "127.0.0.1:43127"},
		{"host.test,EXCLUDE example.com", "127.0.0.1:43127"},
		{"106.53.107.122", "invalid"},
	} {
		_, err := storageBrowserBaseURL(input[0], input[1])
		require.EqualError(t, err, "browser_e2e_control_unavailable")
	}
}

func TestStorageLifecycleCreatesActivatesAndDisablesIsolatedRows(t *testing.T) {
	t.Parallel()
	api := &fakeStorageMetadataAPI{}
	primary := &fakeStoragePrimaryAPI{}
	session := &remoteStorageSession{
		metadata:    api,
		primary:     primary,
		auth:        &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: "signed"},
		nodeAuth:    &storagepb.AuthInfo{AppId: "storage-deployer", AppKey: "deploy-signed"},
		primaryAuth: &storagepb.AuthInfo{AppId: "storage-e2e", AppKey: "primary-signed"},
	}
	result, err := runStorageLifecycle(context.Background(), session, "task16")
	require.NoError(t, err)
	require.Equal(t, "passed", result.Status)
	require.Equal(t, "completed", result.Cleanup)
	require.Equal(t, []string{
		"space_created", "data_source_created", "dataset_created_disabled", "dataset_column_created",
		"disabled_write_rejected", "activation_checks_passed", "stale_revision_rejected", "dataset_activated_locked",
		"row_written", "row_read_back", "locked_rebind_rejected", "active_node_delete_rejected",
	}, result.Assertions)
	require.Equal(t, []string{"task16_dataset"}, api.deletedDatasets)
	require.Equal(t, []string{"task16_source"}, api.deletedSources)
	require.Equal(t, []string{"task16_space"}, api.deletedSpaces)
	require.Empty(t, api.deletedNodes)
	for _, auth := range api.auths {
		if auth.GetAppId() == "storage-deployer" {
			require.Equal(t, "deploy-signed", auth.GetAppKey())
			continue
		}
		require.Equal(t, "storage-metadata", auth.GetAppId())
		require.Equal(t, "signed", auth.GetAppKey())
	}
	require.Equal(t, "storage-e2e", primary.auth.GetAppId())
	require.Equal(t, "primary-signed", primary.auth.GetAppKey())
	var encoded map[string]any
	require.NoError(t, json.Unmarshal(mustJSON(t, result), &encoded))
	require.NotContains(t, string(mustJSON(t, result)), "task16_space")
	require.NotContains(t, string(mustJSON(t, result)), "row-value")
}

func TestStorageBrowserFixtureCreatesAndCleansIsolatedAdminAndMetadataRows(t *testing.T) {
	t.Parallel()
	metadata := &fakeStorageMetadataAPI{}
	admin := &fakeStorageAdminSpaceAPI{}
	session := &remoteStorageSession{
		metadata: metadata,
		auth:     &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: "signed"},
	}
	fixture, cleanup, err := createStorageBrowserFixture(context.Background(), session, admin)
	require.NoError(t, err)
	require.Equal(t, fixture.SpaceID, fixture.Namespace+"_space")
	require.Equal(t, fixture.SpaceName, "浏览器隔离空间 "+fixture.Namespace)
	require.Equal(t, fixture.DatasetID, fixture.Namespace+"_dataset")
	require.Equal(t, fixture.DatasetName, "浏览器验证集")
	require.NoError(t, cleanup())
	require.Equal(t, []string{fixture.DatasetID}, metadata.deletedDatasets)
	require.Equal(t, []string{fixture.SourceID}, metadata.deletedSources)
	require.Equal(t, []string{fixture.SpaceID}, metadata.deletedSpaces)
	require.Equal(t, []string{fixture.SpaceID}, admin.deletedSpaces)
	require.Equal(t, []string{"internal"}, metadata.dataSourceKinds)
	require.Equal(t, []string{"数值"}, metadata.datasetColumnDisplayNames)
}

func TestStorageBrowserFixtureRemovesAbandonedE2ESpacesBeforeCreating(t *testing.T) {
	t.Parallel()
	staleCreatedAt := time.Now().UTC().Add(-2 * storageBrowserFixtureMaxAge).Format(time.RFC3339Nano)
	recentCreatedAt := time.Now().UTC().Add(-storageBrowserFixtureMaxAge / 2).Format(time.RFC3339Nano)
	var storageSpaces []*storagepb.Space
	var adminSpaceRows []*adminpb.Space
	var staleIDs []string
	for index := 0; index < 205; index++ {
		namespace := fmt.Sprintf("brstale%03d", index)
		spaceID := namespace + "_space"
		spaceName := "浏览器隔离空间 " + namespace
		staleIDs = append(staleIDs, spaceID)
		storageSpaces = append(storageSpaces, &storagepb.Space{
			SpaceId: spaceID, Name: spaceName, Owner: storageBrowserFixtureOwner,
			Status: "active", CreatedAt: staleCreatedAt,
		})
		adminSpaceRows = append(adminSpaceRows, &adminpb.Space{
			SpaceId: spaceID, Name: spaceName, Owner: storageBrowserFixtureOwner,
			Status: "active", CreatedAt: staleCreatedAt,
		})
	}
	storageSpaces = append(storageSpaces,
		&storagepb.Space{
			SpaceId: "brrecent01_space", Name: "浏览器隔离空间 brrecent01",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: recentCreatedAt,
		},
		&storagepb.Space{
			SpaceId: "business_space", Name: "业务空间",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: staleCreatedAt,
		},
		&storagepb.Space{
			SpaceId: "brmismatch_space", Name: "浏览器隔离空间 brdifferent",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: staleCreatedAt,
		},
		&storagepb.Space{
			SpaceId: "brshort_space", Name: "浏览器隔离空间 brshort",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: staleCreatedAt,
		},
		&storagepb.Space{
			SpaceId: "brother_space", Name: "浏览器隔离空间 brother",
			Owner: "storage-e2e", Status: "active", CreatedAt: staleCreatedAt,
		},
	)
	adminSpaceRows = append(adminSpaceRows,
		&adminpb.Space{
			SpaceId: "brrecent01_space", Name: "浏览器隔离空间 brrecent01",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: recentCreatedAt,
		},
		&adminpb.Space{
			SpaceId: "business_space", Name: "业务空间",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: staleCreatedAt,
		},
		&adminpb.Space{
			SpaceId: "brmismatch_space", Name: "浏览器隔离空间 brdifferent",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: staleCreatedAt,
		},
		&adminpb.Space{
			SpaceId: "brshort_space", Name: "浏览器隔离空间 brshort",
			Owner: storageBrowserFixtureOwner, Status: "active", CreatedAt: staleCreatedAt,
		},
		&adminpb.Space{
			SpaceId: "brdone_space", Name: "浏览器隔离空间 brdone",
			Owner: storageBrowserFixtureOwner, Status: "disabled", CreatedAt: staleCreatedAt,
		},
		&adminpb.Space{
			SpaceId: "brdisable0_space", Name: "浏览器隔离空间 brdisable0",
			Owner: storageBrowserFixtureOwner, Status: "disabled", CreatedAt: staleCreatedAt,
		},
	)
	metadata := &fakeStorageMetadataAPI{spaces: storageSpaces}
	admin := &fakeStorageAdminSpaceAPI{spaces: adminSpaceRows}
	session := &remoteStorageSession{
		metadata: metadata,
		auth:     &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: "signed"},
	}

	fixture, cleanup, err := createStorageBrowserFixture(context.Background(), session, admin)
	require.NoError(t, err)
	require.Equal(t, staleIDs, metadata.deletedSpaces)
	require.Equal(t, append(staleIDs, "brdisable0_space"), admin.deletedSpaces)
	require.Equal(t, 2, metadata.listSpaceCalls)
	require.Equal(t, 2, admin.listSpaceCalls)
	for _, protectedID := range []string{
		"brrecent01_space", "business_space", "brmismatch_space", "brshort_space", "brother_space",
	} {
		require.NotContains(t, metadata.deletedSpaces, protectedID)
	}
	for _, protectedID := range []string{
		"brrecent01_space", "business_space", "brmismatch_space", "brshort_space", "brdone_space",
	} {
		require.NotContains(t, admin.deletedSpaces, protectedID)
	}
	require.NoError(t, cleanup())
	require.Equal(t, append(staleIDs, fixture.SpaceID), metadata.deletedSpaces)
	require.Equal(t, append(append(staleIDs, "brdisable0_space"), fixture.SpaceID), admin.deletedSpaces)
}

func TestStorageBrowserFixtureKeepsCreateFailureWhenStorageSpaceWasNotCreated(t *testing.T) {
	t.Parallel()
	metadata := &fakeStorageMetadataAPI{failCreateSpace: true, failDeleteSpace: true}
	admin := &fakeStorageAdminSpaceAPI{}
	session := &remoteStorageSession{
		metadata: metadata,
		auth:     &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: "signed"},
	}

	_, _, err := createStorageBrowserFixture(context.Background(), session, admin)

	require.EqualError(t, err, "browser_e2e_storage_space_create_failed")
	require.Empty(t, metadata.deletedSpaces)
	require.Len(t, admin.deletedSpaces, 1)
}

func TestStorageBrowserFixtureDoesNotMaskDataSourceFailureWithCleanupFailure(t *testing.T) {
	t.Parallel()
	metadata := &fakeStorageMetadataAPI{failCreateDataSource: true, failDeleteSpace: true}
	admin := &fakeStorageAdminSpaceAPI{}
	session := &remoteStorageSession{
		metadata: metadata,
		auth:     &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: "signed"},
	}

	_, _, err := createStorageBrowserFixture(context.Background(), session, admin)

	require.EqualError(t, err, "browser_e2e_data_source_create_failed (browser_e2e_fixture_cleanup_failed)")
	require.Len(t, metadata.deletedSpaces, 1)
	require.Len(t, admin.deletedSpaces, 1)
}

func TestValidateStorageBuildProvenanceRequiresExactRemoteArtifacts(t *testing.T) {
	t.Parallel()
	commit := "0123456789abcdef0123456789abcdef01234567"
	hashes := map[string]string{
		"moox-storage-primary": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"moox-storage-node":    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"moox-storage-view":    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	local := storageBuildProvenance{SchemaVersion: 1, Commit: commit, Dirty: false, BinaryHashes: hashes}
	remote := storageBuildProvenance{SchemaVersion: 1, Commit: commit, Dirty: false, BinaryHashes: hashes}
	require.NoError(t, validateStorageBuildProvenance(local, remote, hashes))

	remote.Commit = "1123456789abcdef0123456789abcdef01234567"
	require.EqualError(t, validateStorageBuildProvenance(local, remote, hashes), "storage_provenance_mismatch")
	remote.Commit = commit
	actual := map[string]string{}
	for name, hash := range hashes {
		actual[name] = hash
	}
	actual["moox-storage-node"] = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	require.EqualError(t, validateStorageBuildProvenance(local, remote, actual), "storage_provenance_mismatch")
	local.Dirty = true
	require.EqualError(t, validateStorageBuildProvenance(local, remote, hashes), "storage_provenance_mismatch")
}

func TestSelectDeploymentDataNodeDoesNotUseAnUnrelatedActiveNode(t *testing.T) {
	items := []*storagepb.DataNodeListItem{
		{Node: &storagepb.DataNode{NodeId: "old-node", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"}},
		{Node: &storagepb.DataNode{NodeId: storageDeploymentNodeID, Status: "active", ServiceTarget: "ip://127.0.0.1:20108"}},
	}
	node, err := selectDeploymentDataNode(items)
	require.NoError(t, err)
	require.Equal(t, storageDeploymentNodeID, node.GetNodeId())
	_, err = selectDeploymentDataNode(items[:1])
	require.EqualError(t, err, "storage_deployment_node_missing")
}

func TestReadCurrentGitCommitRejectsNonRepository(t *testing.T) {
	_, err := readCurrentGitCommit(t.TempDir())
	require.EqualError(t, err, "storage_provenance_unavailable")
}

func TestStorageBrowserEndpointUsesDiscoveredControlHost(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	host, err := resolveStorageBrowserHost(snapshot.Manifest, "compute")
	require.NoError(t, err)
	require.Equal(t, "control", host.Name)
	require.Equal(t, "203.0.113.8", host.Address)
	_, err = resolveStorageBrowserHost(snapshot.Manifest, "missing")
	require.Error(t, err)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

type fakeStorageMetadataAPI struct {
	auths                     []*storagepb.AuthInfo
	spaces                    []*storagepb.Space
	deletedSpaces             []string
	deletedSources            []string
	deletedDatasets           []string
	deletedNodes              []string
	dataSourceKinds           []string
	datasetColumnDisplayNames []string
	temporaryNode             *storagepb.DataNode
	temporaryReferenced       bool
	failCreateSpace           bool
	failCreateDataSource      bool
	failDeleteSpace           bool
	listSpaceCalls            int
}

type fakeStorageAdminSpaceAPI struct {
	deletedSpaces  []string
	spaces         []*adminpb.Space
	listSpaceCalls int
}

func (f *fakeStorageAdminSpaceAPI) CreateSpace(_ context.Context, req *adminpb.CreateSpaceReq) (*adminpb.CreateSpaceRsp, error) {
	return &adminpb.CreateSpaceRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}, Space: req.GetSpace()}, nil
}

func (f *fakeStorageAdminSpaceAPI) DeleteSpace(_ context.Context, req *adminpb.DeleteSpaceReq) (*adminpb.DeleteSpaceRsp, error) {
	f.deletedSpaces = append(f.deletedSpaces, req.GetSpaceId())
	return &adminpb.DeleteSpaceRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}}, nil
}

func (f *fakeStorageAdminSpaceAPI) ListSpaces(
	_ context.Context,
	req *adminpb.ListSpacesReq,
) (*adminpb.ListSpacesRsp, error) {
	f.listSpaceCalls++
	var spaces []*adminpb.Space
	for _, space := range f.spaces {
		if req.GetOwner() != "" && space.GetOwner() != req.GetOwner() {
			continue
		}
		if req.GetStatus() != "" && space.GetStatus() != req.GetStatus() {
			continue
		}
		spaces = append(spaces, space)
	}
	page, size, items, hasMore := paginateStorageBrowserSpaces(spaces, req.GetPage())
	return &adminpb.ListSpacesRsp{
		RetInfo:    &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS},
		Spaces:     items,
		PageResult: &commonpb.PageResult{Page: page, Size: size, Total: uint32(len(spaces)), HasMore: hasMore},
	}, nil
}

func (f *fakeStorageMetadataAPI) remember(auth *storagepb.AuthInfo) { f.auths = append(f.auths, auth) }
func storageOK() *storagepb.RetInfo                                 { return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS} }

func (f *fakeStorageMetadataAPI) CreateSpace(_ context.Context, req *storagepb.CreateSpaceReq) (*storagepb.CreateSpaceRsp, error) {
	f.remember(req.GetAuthInfo())
	if f.failCreateSpace {
		return &storagepb.CreateSpaceRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INVALID_PARAM}}, nil
	}
	return &storagepb.CreateSpaceRsp{RetInfo: storageOK(), Space: req.GetSpace()}, nil
}
func (f *fakeStorageMetadataAPI) UpdateSpace(_ context.Context, req *storagepb.UpdateSpaceReq) (*storagepb.UpdateSpaceRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.UpdateSpaceRsp{RetInfo: storageOK(), Space: req.GetSpace()}, nil
}
func (f *fakeStorageMetadataAPI) DeleteSpace(_ context.Context, req *storagepb.DeleteSpaceReq) (*storagepb.DeleteSpaceRsp, error) {
	f.remember(req.GetAuthInfo())
	f.deletedSpaces = append(f.deletedSpaces, req.GetSpaceId())
	if f.failDeleteSpace {
		return &storagepb.DeleteSpaceRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INVALID_PARAM}}, nil
	}
	return &storagepb.DeleteSpaceRsp{RetInfo: storageOK()}, nil
}
func (f *fakeStorageMetadataAPI) ListSpaces(
	_ context.Context,
	req *storagepb.ListSpacesReq,
) (*storagepb.ListSpacesRsp, error) {
	f.remember(req.GetAuthInfo())
	f.listSpaceCalls++
	var spaces []*storagepb.Space
	for _, space := range f.spaces {
		if req.GetOwner() == "" || space.GetOwner() == req.GetOwner() {
			spaces = append(spaces, space)
		}
	}
	page, size, items, hasMore := paginateStorageBrowserSpaces(spaces, req.GetPage())
	return &storagepb.ListSpacesRsp{
		RetInfo: storageOK(), Spaces: items,
		PageResult: &commonpb.PageResult{Page: page, Size: size, Total: uint32(len(spaces)), HasMore: hasMore},
	}, nil
}

func paginateStorageBrowserSpaces[T any](items []T, request *commonpb.Page) (uint32, uint32, []T, bool) {
	page, size := uint32(1), uint32(20)
	if request != nil {
		if request.GetPage() > 0 {
			page = request.GetPage()
		}
		if request.GetSize() > 0 {
			size = request.GetSize()
		}
	}
	start := int((page - 1) * size)
	if start >= len(items) {
		return page, size, nil, false
	}
	end := min(start+int(size), len(items))
	return page, size, items[start:end], end < len(items)
}
func (f *fakeStorageMetadataAPI) CreateDataSource(_ context.Context, req *storagepb.CreateDataSourceReq) (*storagepb.CreateDataSourceRsp, error) {
	f.remember(req.GetAuthInfo())
	f.dataSourceKinds = append(f.dataSourceKinds, req.GetDataSource().GetKind())
	if f.failCreateDataSource {
		return &storagepb.CreateDataSourceRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INVALID_PARAM}}, nil
	}
	return &storagepb.CreateDataSourceRsp{RetInfo: storageOK(), DataSource: req.GetDataSource()}, nil
}
func (f *fakeStorageMetadataAPI) UpdateDataSource(_ context.Context, req *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.UpdateDataSourceRsp{RetInfo: storageOK(), DataSource: req.GetDataSource()}, nil
}
func (f *fakeStorageMetadataAPI) DeleteDataSource(_ context.Context, req *storagepb.DeleteDataSourceReq) (*storagepb.DeleteDataSourceRsp, error) {
	f.remember(req.GetAuthInfo())
	f.deletedSources = append(f.deletedSources, req.GetDataSourceId())
	return &storagepb.DeleteDataSourceRsp{RetInfo: storageOK()}, nil
}
func (f *fakeStorageMetadataAPI) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	dataset := *req.GetDataset()
	dataset.Revision = 7
	if f.temporaryNode != nil && dataset.GetDataNodeId() == f.temporaryNode.GetNodeId() {
		f.temporaryReferenced = true
	}
	return &storagepb.CreateDatasetRsp{RetInfo: storageOK(), Dataset: &dataset}, nil
}
func (f *fakeStorageMetadataAPI) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.GetDatasetRsp{RetInfo: storageOK(), Dataset: &storagepb.Dataset{
		SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Status: "disabled", Revision: 7,
	}}, nil
}
func (f *fakeStorageMetadataAPI) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.UpdateDatasetRsp{RetInfo: storageOK(), Dataset: req.GetDataset()}, nil
}
func (f *fakeStorageMetadataAPI) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	f.deletedDatasets = append(f.deletedDatasets, req.GetDatasetId())
	if req.GetDatasetId() == "task16_constraint" {
		f.temporaryReferenced = false
	}
	return &storagepb.DeleteDatasetRsp{RetInfo: storageOK()}, nil
}
func (f *fakeStorageMetadataAPI) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.remember(req.GetAuthInfo())
	f.datasetColumnDisplayNames = append(f.datasetColumnDisplayNames, req.GetColumn().GetAttributes()["display_name"])
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: storageOK(), Column: req.GetColumn()}, nil
}
func (f *fakeStorageMetadataAPI) RebindDatasetDataNode(_ context.Context, req *storagepb.RebindDatasetDataNodeReq) (*storagepb.RebindDatasetDataNodeRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.RebindDatasetDataNodeRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INVALID_PARAM}}, nil
}
func (f *fakeStorageMetadataAPI) RegisterDataNode(_ context.Context, req *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error) {
	f.remember(req.GetAuthInfo())
	f.temporaryNode = &storagepb.DataNode{NodeId: req.GetNodeId(), Name: req.GetInitialName(), ServiceTarget: req.GetServiceTarget(), Status: "active"}
	return &storagepb.RegisterDataNodeRsp{RetInfo: storageOK(), Node: f.temporaryNode}, nil
}
func (f *fakeStorageMetadataAPI) UpdateDataNode(_ context.Context, req *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error) {
	f.remember(req.GetAuthInfo())
	if f.temporaryNode != nil && req.GetNodeId() == f.temporaryNode.GetNodeId() {
		f.temporaryNode.Status = req.GetStatus()
		f.temporaryNode.Name = req.GetName()
	}
	return &storagepb.UpdateDataNodeRsp{RetInfo: storageOK(), Node: f.temporaryNode}, nil
}
func (f *fakeStorageMetadataAPI) ListDataNodes(_ context.Context, req *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.ListDataNodesRsp{RetInfo: storageOK(), Items: []*storagepb.DataNodeListItem{{Node: &storagepb.DataNode{NodeId: storageDeploymentNodeID, Name: "部署节点", ServiceTarget: "ip://127.0.0.1:20107", Status: "active"}}}, PageResult: &storagepb.PageResult{}}, nil
}
func (f *fakeStorageMetadataAPI) DeleteDataNode(_ context.Context, req *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error) {
	f.remember(req.GetAuthInfo())
	if req.GetNodeId() == storageDeploymentNodeID {
		return &storagepb.DeleteDataNodeRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INVALID_PARAM}}, nil
	}
	if f.temporaryNode == nil || f.temporaryNode.GetStatus() != "disabled" || f.temporaryReferenced {
		return &storagepb.DeleteDataNodeRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INVALID_PARAM}}, nil
	}
	f.deletedNodes = append(f.deletedNodes, req.GetNodeId())
	return &storagepb.DeleteDataNodeRsp{RetInfo: storageOK(), Node: f.temporaryNode}, nil
}
func (f *fakeStorageMetadataAPI) CheckDatasetActivation(_ context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	f.remember(req.GetAuthInfo())
	return &storagepb.CheckDatasetActivationRsp{RetInfo: storageOK(), DatasetRevision: 7, Ready: true, Checks: []*storagepb.DatasetActivationCheck{{CheckId: "internal-id", Ready: true}}}, nil
}
func (f *fakeStorageMetadataAPI) ActivateDataset(_ context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	f.remember(req.GetAuthInfo())
	if req.GetExpectedRevision() != 7 {
		return &storagepb.ActivateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_CONFLICT}}, nil
	}
	return &storagepb.ActivateDatasetRsp{RetInfo: storageOK(), Dataset: &storagepb.Dataset{DatasetId: req.GetDatasetId(), Status: "active", BindingLocked: true, Revision: req.GetExpectedRevision() + 1}}, nil
}

type fakeStoragePrimaryAPI struct {
	auth   *storagepb.AuthInfo
	row    *storagepb.RowFieldUpsert
	writes int
}

func (f *fakeStoragePrimaryAPI) UpsertFields(_ context.Context, req *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	f.auth = req.GetAuthInfo()
	f.row = req.GetRows()[0]
	if f.writes == 0 {
		f.writes++
		return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INVALID_PARAM}}, nil
	}
	f.writes++
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: storageOK(), Keys: []*storagepb.RowKey{f.row.GetKey()}}, nil
}

func (f *fakeStoragePrimaryAPI) ReadFields(_ context.Context, req *storagepb.PrimaryReadFieldsReq) (*storagepb.PrimaryReadFieldsRsp, error) {
	f.auth = req.GetAuthInfo()
	return &storagepb.PrimaryReadFieldsRsp{
		RetInfo:      storageOK(),
		Rows:         []*storagepb.RowFieldValues{{Key: f.row.GetKey(), Fields: f.row.GetFields()}},
		ExistingKeys: []*storagepb.RowKey{f.row.GetKey()},
	}, nil
}
