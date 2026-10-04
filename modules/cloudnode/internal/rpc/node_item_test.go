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

func seedGenericNodeItemPackage(t *testing.T, catalog *store.CatalogRepository) {
	t.Helper()
	seedSCFAccountAndPackage(t, catalog)
	pkg, err := catalog.GetPackage(context.Background(), "crypto", "moox-collector_dev")
	require.NoError(t, err)
	// Provider lifecycle tests do not model the market-fetch runtime contract.
	pkg.WorkloadType = "collect.kline"
	require.NoError(t, catalog.UpsertPackage(context.Background(), *pkg))
}

func TestExecuteCreateNodeItemCreatesSCFAndCatalogNode(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
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
	seedGenericNodeItemPackage(t, catalog)
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
	require.NotNil(t, node, "the failed SCF create must leave a discoverable cleanup reservation")
	assert.False(t, metadataBool(parseJSONMap(node.Metadata), "deployment_ready"))
}

func TestExecuteCreateNodeItemReconcilesFunctionCreatedBeforeRestart(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
	reservationID := "restart-create-reservation"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": reservationID},
		},
	}}}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId:      "account-a",
		Region:              "ap-guangzhou",
		PackageId:           "moox-collector_dev",
		CreateReservationId: reservationID,
		Metadata:            metadata,
	}, 0)

	require.NoError(t, err)
	assert.Equal(t, "created function collector-000", summary)
	assert.Empty(t, fake.created)
	node, err := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "moox-collector_dev", node.PackageID)
}

func TestExecuteCreateRejectsReservationOwnedByCompetingNonce(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "collector-000", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-guangzhou", FunctionName: "collector-000",
		LifecycleID: "reservation-first", Metadata: `{"deployment_ready":false,"create_reservation_id":"reservation-first"}`,
	}))
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}}}
	service := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = service.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-guangzhou", PackageId: "moox-collector_dev",
		CreateReservationId: "reservation-second", Metadata: metadata,
	}, 0)

	require.ErrorIs(t, err, store.ErrNodeLifecycleMismatch)
	require.Empty(t, fake.created, "a competing create must not reach SCF after losing the atomic reservation")
	node, err := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, err)
	require.NotNil(t, node)
	require.Equal(t, "reservation-first", node.LifecycleID)
	require.Equal(t, "reservation-first", metadataString(parseJSONMap(node.Metadata), "create_reservation_id"))
}

func TestExecuteCreateNodeItemRejectsUnownedExistingFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
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
	require.NotNil(t, node, "the rejected pre-existing function must leave a reservation for bounded cleanup")
	assert.False(t, metadataBool(parseJSONMap(node.Metadata), "deployment_ready"))
	assert.Empty(t, fake.created)
}

func TestExecuteCreateRejectsExistingSamePackageWithoutMatchingReservationIdentity(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
		},
	}}}
	service := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-same-package"})
	require.NoError(t, err)
	item := &pb.NodeCreateItem{
		CloudAccountId: "account-a", Region: "ap-guangzhou", PackageId: "moox-collector_dev",
		CreateReservationId: "attempt-reservation-1", Metadata: metadata,
	}

	_, err = service.executeCreateNodeItem(context.Background(), "crypto", item, 0)
	require.ErrorContains(t, err, `already exists with create reservation`)
	require.Empty(t, fake.created)
	require.Empty(t, fake.configured)
	require.Empty(t, fake.deletedFunctions, "an existing same-package function without the exact nonce must not be deleted")
}

func TestDeleteCreateReservationOnlyDeletesProviderOwnedFunction(t *testing.T) {
	tests := []struct {
		name          string
		readback      *tencentscf.FunctionInfo
		readErr       error
		wantDelete    bool
		wantCatalog   bool
		wantErrorText string
	}{
		{
			name:        "matching package ownership",
			readback:    &tencentscf.FunctionInfo{Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": "reservation-1"}},
			wantDelete:  true,
			wantCatalog: true,
		},
		{
			name:        "different package ownership",
			readback:    &tencentscf.FunctionInfo{Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "another-package", "MOOX_CREATE_RESERVATION_ID": "reservation-1"}},
			wantCatalog: true,
		},
		{
			name:        "same package but different attempt",
			readback:    &tencentscf.FunctionInfo{Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": "older-reservation"}},
			wantCatalog: true,
		},
		{
			name:          "provider read failure",
			readErr:       errors.New("provider unavailable"),
			wantErrorText: "verify SCF create reservation ownership",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
				SpaceID: "crypto", NodeID: "collector-000", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
				NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", Namespace: "collector", FunctionName: "collector-000",
				LifecycleID: "reservation-1",
				Metadata:    `{"collector_publish_fenced":true,"biz_type":"market_fetcher","deployment_ready":false}`,
			}))
			fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: test.readback, err: test.readErr}}}
			service := newNodeItemTestService(catalog, fake)

			_, err := service.executeDeleteNodeItem(context.Background(), "crypto", nodeDeleteItemForTest(t, catalog, "crypto", "collector-000"))
			if test.wantErrorText != "" {
				require.ErrorContains(t, err, test.wantErrorText)
			} else {
				require.NoError(t, err)
			}
			wantDeleteCalls := 0
			if test.wantDelete {
				wantDeleteCalls = 1
			}
			require.Len(t, fake.deletedFunctions, wantDeleteCalls)
			node, lookupErr := catalog.GetNodeIncludingDeleted(context.Background(), "crypto", "collector-000")
			require.NoError(t, lookupErr)
			require.NotNil(t, node)
			assert.Equal(t, test.wantCatalog, node.IsDeleted, "only verified ownership or explicit mismatch should remove the catalog reservation")
		})
	}
}

func TestOldDeleteCannotDeleteRecreatedNodeGeneration(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "aba-node", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", FunctionName: "aba-node",
	}))
	oldItem := nodeDeleteItemForTest(t, catalog, "crypto", "aba-node")
	fake := &fakeSCFClient{}
	service := newNodeItemTestService(catalog, fake)

	_, err := service.executeDeleteNodeItem(context.Background(), "crypto", oldItem)
	require.NoError(t, err)
	oldLifecycleID := oldItem.GetLifecycleId()
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "aba-node", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", FunctionName: "aba-node",
	}))
	current, err := catalog.GetNode(context.Background(), "crypto", "aba-node")
	require.NoError(t, err)
	require.NotNil(t, current)
	require.NotEqual(t, oldLifecycleID, current.LifecycleID)

	_, err = service.executeDeleteNodeItem(context.Background(), "crypto", oldItem)
	require.ErrorIs(t, err, store.ErrNodeLifecycleMismatch)
	require.Len(t, fake.deletedFunctions, 1, "a duplicate old delete must not reach the recreated SCF function")
	current, err = catalog.GetNode(context.Background(), "crypto", "aba-node")
	require.NoError(t, err)
	require.NotNil(t, current)
	require.False(t, current.IsDeleted)
}

func TestDeleteLegacyNodeWithoutReadinessMarkerKeepsNormalDeleteSemantics(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "legacy-node", CloudAccountID: "account-a", PackageID: "old-package",
		NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", FunctionName: "legacy-node",
	}))
	fake := &fakeSCFClient{}
	service := newNodeItemTestService(catalog, fake)

	_, err := service.executeDeleteNodeItem(context.Background(), "crypto", nodeDeleteItemForTest(t, catalog, "crypto", "legacy-node"))
	require.NoError(t, err)
	require.Equal(t, 1, fake.getCalls, "legacy nodes skip reservation ownership reads but deletion is verified before catalog cleanup")
	require.Len(t, fake.deletedFunctions, 1)
}

func nodeDeleteItemForTest(t *testing.T, catalog *store.CatalogRepository, spaceID, nodeID string) *pb.NodeDeleteItem {
	t.Helper()
	node, err := catalog.GetNode(context.Background(), spaceID, nodeID)
	require.NoError(t, err)
	require.NotNil(t, node)
	require.NotEmpty(t, node.LifecycleID)
	return &pb.NodeDeleteItem{NodeId: nodeID, LifecycleId: node.LifecycleID, OperationId: "test-delete-" + nodeID}
}

func TestExecuteCreateNodeItemRejectsFailedExistingFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
	reservationID := "failed-create-reservation"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Failed",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": reservationID},
		},
	}}}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId:      "account-a",
		Region:              "ap-guangzhou",
		PackageId:           "moox-collector_dev",
		CreateReservationId: reservationID,
		Metadata:            metadata,
	}, 0)

	require.ErrorContains(t, err, `entered status "Failed"`)
}

func TestExecuteCreateNodeItemReconcilesAcceptedCreateTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
	reservationID := "accepted-create-reservation"
	fake := &fakeSCFClient{
		createErr: context.DeadlineExceeded,
		getResults: []fakeSCFGetResult{
			{err: errors.New("ResourceNotFound.FunctionName")},
			{info: &tencentscf.FunctionInfo{
				Status:      "Active",
				Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": reservationID},
			}},
		},
	}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{
		CloudAccountId:      "account-a",
		Region:              "ap-guangzhou",
		PackageId:           "moox-collector_dev",
		CreateReservationId: reservationID,
		Metadata:            metadata,
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
	seedGenericNodeItemPackage(t, catalog)
	reservationID := "deadline-create-reservation"
	fake := &fakeSCFClient{
		respectContext:       true,
		createWaitForContext: true,
		getResults: []fakeSCFGetResult{
			{err: errors.New("ResourceNotFound.FunctionName")},
			{info: &tencentscf.FunctionInfo{
				Status:      "Active",
				Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": reservationID},
			}},
		},
	}
	svc := newNodeItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = svc.executeCreateNodeItem(ctx, "crypto", &pb.NodeCreateItem{
		CloudAccountId:      "account-a",
		Region:              "ap-guangzhou",
		PackageId:           "moox-collector_dev",
		CreateReservationId: reservationID,
		Metadata:            metadata,
	}, 0)

	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	node, getErr := catalog.GetNode(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	require.NotNil(t, node)
}

func TestExecuteDeployNodeItemUpdatesCodeAndCatalogPackage(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedGenericNodeItemPackage(t, catalog)
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
	seedGenericNodeItemPackage(t, catalog)
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
	seedGenericNodeItemPackage(t, catalog)
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
	seedGenericNodeItemPackage(t, catalog)
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
	seedGenericNodeItemPackage(t, catalog)
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
	require.Len(t, fake.configured, 1)
	assert.Equal(t, "moox-collector_dev", fake.configured[0].Environment["MOOX_CODE_PACKAGE_ID"])
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
