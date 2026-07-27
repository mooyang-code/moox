package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestExecuteCreateNodeItemCreatesSCFAndCatalogNode(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{Status: "Active"}},
	}}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.NoError(t, err)
	assert.Equal(t, "created function collector-000", summary)
	node, err := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "moox-collector_dev", node.PackageID)
	require.Len(t, fake.created, 1)
	assert.Equal(t, "moox-collector_dev", fake.created[0].Environment["MOOX_CODE_PACKAGE_ID"])
}

func TestExecuteCreateNodeItemDoesNotPersistWhenPostCreateStatusFails(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{err: errors.New("permission denied")},
	}}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.ErrorContains(t, err, "get scf function collector-000 status")
	node, getErr := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	assert.Nil(t, node)
}

func TestExecuteCreateNodeItemReconcilesFunctionCreatedBeforeRestart(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Active",
			ClsLogsetID: "logset-a",
			ClsTopicID:  "topic-a",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
		},
	}}}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.NoError(t, err)
	assert.Equal(t, "created function collector-000", summary)
	assert.Empty(t, fake.created)
	node, err := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "moox-collector_dev", node.PackageID)
	assert.Contains(t, node.Metadata, `"cls_logset_id":"logset-a"`)
	assert.Contains(t, node.Metadata, `"cls_topic_id":"topic-a"`)
}

func TestExecuteCreateNodeItemRejectsUnownedExistingFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"},
		},
	}}}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.ErrorContains(t, err, `already exists with code package "old-package"`)
	node, getErr := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	assert.Nil(t, node)
	assert.Empty(t, fake.created)
}

func TestExecuteCreateNodeItemRejectsFailedExistingFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Failed",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
		},
	}}}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.ErrorContains(t, err, `entered status "Failed"`)
}

func TestExecuteCreateNodeItemReconcilesAcceptedCreateTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{
		createErr: context.DeadlineExceeded,
		getResults: []fakeSCFGetResult{
			{err: errors.New("ResourceNotFound.FunctionName")},
			{info: &tencentscf.FunctionInfo{
				Status:      "Active",
				Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
			}},
		},
	}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.NoError(t, err)
	assert.Equal(t, "created function collector-000", summary)
	require.Len(t, fake.created, 1)
	assert.Equal(t, "moox-collector_dev", fake.created[0].Environment["MOOX_CODE_PACKAGE_ID"])
	node, getErr := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	require.NotNil(t, node)
	assert.Equal(t, "moox-collector_dev", node.PackageID)
}

func TestExecuteCreateNodeItemReconcilesAfterItemDeadline(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	fake := &fakeSCFClient{
		respectContext:       true,
		createWaitForContext: true,
		getResults: []fakeSCFGetResult{
			{err: errors.New("ResourceNotFound.FunctionName")},
			{info: &tencentscf.FunctionInfo{
				Status:      "Active",
				Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
			}},
		},
	}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = svc.executeCreateNodeItem(ctx, "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	node, getErr := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	require.NotNil(t, node)
}

func TestExecuteDeployNodeItemUpdatesCodeAndCatalogPackage(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	seedNodeForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{info: &tencentscf.FunctionInfo{Status: "Active", Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"}}},
		{info: &tencentscf.FunctionInfo{Status: "Active"}},
	}}
	svc := newNodeItemTestService(catalog, fake)

	summary, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "moox-collector_dev",
	})

	require.NoError(t, err)
	assert.Equal(t, "deployed package moox-collector_dev to node-a", summary)
	node, err := catalog.GetNode(context.Background(), "crypto", "node-a")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "moox-collector_dev", node.PackageID)
}

func TestExecuteDeployNodeItemRejectsFailedFunctionWithMatchingMarker(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	seedNodeForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{
		Status:      "Failed",
		Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
	}}}}
	svc := newNodeItemTestService(catalog, fake)

	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "moox-collector_dev",
	})

	require.ErrorContains(t, err, `entered status "Failed"`)
}

func TestExecuteDeployNodeItemRejectsFailedStatusAfterConfigurationUpdate(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	seedNodeForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"},
		}},
		{info: &tencentscf.FunctionInfo{Status: "Active"}},
		{info: &tencentscf.FunctionInfo{Status: "Failed"}},
	}}
	svc := newNodeItemTestService(catalog, fake)

	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "moox-collector_dev",
	})

	require.ErrorContains(t, err, `entered status "Failed"`)
	node, getErr := catalog.GetNode(context.Background(), "crypto", "node-a")
	require.NoError(t, getErr)
	require.NotNil(t, node)
	assert.Equal(t, "old-package", node.PackageID)
}

func TestExecuteDeployNodeItemRejectsMissingNode(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	svc := newNodeItemTestService(catalog, &fakeSCFClient{})

	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "missing", PackageId: "pkg",
	})

	require.ErrorContains(t, err, "node not found")
}

func TestExecuteDeployNodeItemRejectsUnavailablePackage(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	seedNodeForDeploy(t, catalog)
	pkg, err := catalog.GetPackage(context.Background(), "crypto", "moox-collector_dev")
	require.NoError(t, err)
	require.NotNil(t, pkg)
	pkg.Status = "uploading"
	require.NoError(t, catalog.UpsertPackage(context.Background(), *pkg))
	svc := newNodeItemTestService(catalog, &fakeSCFClient{})

	_, err = svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "moox-collector_dev",
	})

	require.ErrorContains(t, err, "not available")
}

func TestExecuteDeployNodeItemReconcilesAcceptedTencentTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	seedNodeForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{
		Status:      "Updating",
		Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
	}}}}
	svc := newNodeItemTestService(catalog, fake)

	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "moox-collector_dev",
	})

	require.NoError(t, err)
	assert.Empty(t, fake.updated)
	assert.Empty(t, fake.configured)
}

func newNodeItemTestService(catalog *store.CatalogRepository, fake scfProvisioner) *Service {
	return &Service{
		catalog: catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{
			SecretID: "secret-id", SecretKey: "secret-key",
		}},
		scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
}

func seedNodeForDeploy(t *testing.T, catalog *store.CatalogRepository) {
	t.Helper()
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "node-a", CloudAccountID: "account-a",
		PackageID: "old-package", NodeType: "scf-event", Provider: "tencent-scf",
		Region: "ap-guangzhou", Namespace: "collector", FunctionName: "collector-0",
		Metadata: `{"handler":"main"}`,
	}))
}
