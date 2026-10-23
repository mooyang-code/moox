package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func seedGenericInstanceItemPackage(t *testing.T, catalog *store.CatalogRepository) {
	t.Helper()
	seedSCFAccountAndPackage(t, catalog)
	pkg, err := catalog.GetPackage(context.Background(), "crypto", "moox-collector_dev")
	require.NoError(t, err)
	// Provider lifecycle tests do not model the market-fetch runtime contract.
	pkg.WorkloadType = "collect.kline"
	require.NoError(t, catalog.UpsertPackage(context.Background(), *pkg))
}

func TestExecuteCreateInstanceItemCreatesSCFAndCatalogInstance(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{info: &tencentscf.FunctionInfo{Status: "Active"}},
	}}
	svc := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.NoError(t, err)
	assert.Equal(t, "created function collector-000", summary)
	instance, err := catalog.GetInstance(context.Background(), "crypto", "collector-000")
	require.NoError(t, err)
	require.NotNil(t, instance)
	assert.Equal(t, "moox-collector_dev", instance.PackageID)
	require.Len(t, fake.created, 1)
	assert.Equal(t, "moox-collector_dev", fake.created[0].Environment["MOOX_CODE_PACKAGE_ID"])
}

func TestExecuteCreateInstanceItemDoesNotPersistWhenPostCreateStatusFails(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{err: errors.New("ResourceNotFound.FunctionName")},
		{err: errors.New("permission denied")},
	}}
	svc := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.ErrorContains(t, err, "get scf function collector-000 status")
	instance, getErr := catalog.GetInstance(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	require.NotNil(t, instance, "the failed SCF create must leave a discoverable cleanup reservation")
	assert.False(t, metadataBool(parseJSONMap(instance.Metadata), "deployment_ready"))
}

func TestExecuteCreateInstanceItemReconcilesFunctionCreatedBeforeRestart(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	reservationID := "restart-create-reservation"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": reservationID},
		},
	}}}
	svc := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId:      "account-a",
		Region:              "ap-guangzhou",
		PackageId:           "moox-collector_dev",
		CreateReservationId: reservationID,
		Metadata:            metadata,
	}, 0)

	require.NoError(t, err)
	assert.Equal(t, "created function collector-000", summary)
	assert.Empty(t, fake.created)
	instance, err := catalog.GetInstance(context.Background(), "crypto", "collector-000")
	require.NoError(t, err)
	require.NotNil(t, instance)
	assert.Equal(t, "moox-collector_dev", instance.PackageID)
}

func TestExecuteCreateRejectsReservationOwnedByCompetingNonce(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "collector-000", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		FunctionType: "scf-event", Provider: "tencent-scf", Region: "ap-guangzhou", FunctionName: "collector-000",
		LifecycleID: "reservation-first", Metadata: `{"deployment_ready":false,"create_reservation_id":"reservation-first"}`,
	}))
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}}}
	service := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = service.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-guangzhou", PackageId: "moox-collector_dev",
		CreateReservationId: "reservation-second", Metadata: metadata,
	}, 0)

	require.ErrorIs(t, err, store.ErrInstanceLifecycleMismatch)
	require.Empty(t, fake.created, "a competing create must not reach SCF after losing the atomic reservation")
	instance, err := catalog.GetInstance(context.Background(), "crypto", "collector-000")
	require.NoError(t, err)
	require.NotNil(t, instance)
	require.Equal(t, "reservation-first", instance.LifecycleID)
	require.Equal(t, "reservation-first", metadataString(parseJSONMap(instance.Metadata), "create_reservation_id"))
}

func TestExecuteCreateInstanceItemRejectsUnownedExistingFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"},
		},
	}}}
	svc := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a",
		Region:         "ap-guangzhou",
		PackageId:      "moox-collector_dev",
		Metadata:       metadata,
	}, 0)

	require.ErrorContains(t, err, `already exists with code package "old-package"`)
	instance, getErr := catalog.GetInstance(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	require.NotNil(t, instance, "the rejected pre-existing function must leave a reservation for bounded cleanup")
	assert.False(t, metadataBool(parseJSONMap(instance.Metadata), "deployment_ready"))
	assert.Empty(t, fake.created)
}

func TestExecuteCreateRejectsExistingSamePackageWithoutMatchingReservationIdentity(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
		},
	}}}
	service := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-same-package"})
	require.NoError(t, err)
	item := &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", Region: "ap-guangzhou", PackageId: "moox-collector_dev",
		CreateReservationId: "attempt-reservation-1", Metadata: metadata,
	}

	_, err = service.executeCreateInstanceItem(context.Background(), "crypto", item, 0)
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
			catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
				SpaceID: "crypto", InstanceID: "collector-000", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
				FunctionType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", Namespace: "collector", FunctionName: "collector-000",
				LifecycleID: "reservation-1",
				Metadata:    `{"collector_publish_fenced":true,"workload_type":"market_fetcher","deployment_ready":false}`,
			}))
			fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: test.readback, err: test.readErr}}}
			service := newInstanceItemTestService(catalog, fake)

			_, err := service.executeDeleteInstanceItem(context.Background(), "crypto", instanceDeleteItemForTest(t, catalog, "crypto", "collector-000"))
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
			instance, lookupErr := catalog.GetInstanceIncludingDeleted(context.Background(), "crypto", "collector-000")
			require.NoError(t, lookupErr)
			require.NotNil(t, instance)
			assert.Equal(t, test.wantCatalog, instance.IsDeleted, "only verified ownership or explicit mismatch should remove the catalog reservation")
		})
	}
}

func TestOldDeleteCannotDeleteRecreatedInstanceGeneration(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "aba-instance", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		FunctionType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", FunctionName: "aba-instance",
	}))
	oldItem := instanceDeleteItemForTest(t, catalog, "crypto", "aba-instance")
	fake := &fakeSCFClient{}
	service := newInstanceItemTestService(catalog, fake)

	_, err := service.executeDeleteInstanceItem(context.Background(), "crypto", oldItem)
	require.NoError(t, err)
	oldLifecycleID := oldItem.GetLifecycleId()
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "aba-instance", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		FunctionType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", FunctionName: "aba-instance",
	}))
	current, err := catalog.GetInstance(context.Background(), "crypto", "aba-instance")
	require.NoError(t, err)
	require.NotNil(t, current)
	require.NotEqual(t, oldLifecycleID, current.LifecycleID)

	_, err = service.executeDeleteInstanceItem(context.Background(), "crypto", oldItem)
	require.ErrorIs(t, err, store.ErrInstanceLifecycleMismatch)
	require.Len(t, fake.deletedFunctions, 1, "a duplicate old delete must not reach the recreated SCF function")
	current, err = catalog.GetInstance(context.Background(), "crypto", "aba-instance")
	require.NoError(t, err)
	require.NotNil(t, current)
	require.False(t, current.IsDeleted)
}

func TestDeleteLegacyInstanceWithoutReadinessMarkerKeepsNormalDeleteSemantics(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "legacy-instance", CloudAccountID: "account-a", PackageID: "old-package",
		FunctionType: "scf-event", Provider: "tencent-scf", Region: "ap-singapore", FunctionName: "legacy-instance",
	}))
	fake := &fakeSCFClient{}
	service := newInstanceItemTestService(catalog, fake)

	_, err := service.executeDeleteInstanceItem(context.Background(), "crypto", instanceDeleteItemForTest(t, catalog, "crypto", "legacy-instance"))
	require.NoError(t, err)
	require.Equal(t, 1, fake.getCalls, "legacy instances skip reservation ownership reads but deletion is verified before catalog cleanup")
	require.Len(t, fake.deletedFunctions, 1)
}

func instanceDeleteItemForTest(t *testing.T, catalog *store.CatalogRepository, spaceID, instanceID string) *pb.FunctionInstanceDeleteItem {
	t.Helper()
	instance, err := catalog.GetInstance(context.Background(), spaceID, instanceID)
	require.NoError(t, err)
	require.NotNil(t, instance)
	require.NotEmpty(t, instance.LifecycleID)
	return &pb.FunctionInstanceDeleteItem{InstanceId: instanceID, LifecycleId: instance.LifecycleID, OperationId: "test-delete-" + instanceID}
}

func TestExecuteCreateInstanceItemRejectsFailedExistingFunction(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	reservationID := "failed-create-reservation"
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{
		info: &tencentscf.FunctionInfo{
			Status:      "Failed",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev", "MOOX_CREATE_RESERVATION_ID": reservationID},
		},
	}}}
	svc := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	_, err = svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId:      "account-a",
		Region:              "ap-guangzhou",
		PackageId:           "moox-collector_dev",
		CreateReservationId: reservationID,
		Metadata:            metadata,
	}, 0)

	require.ErrorContains(t, err, `entered status "Failed"`)
}

func TestExecuteCreateInstanceItemReconcilesAcceptedCreateTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
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
	svc := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)

	summary, err := svc.executeCreateInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceCreateItem{
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
	instance, getErr := catalog.GetInstance(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	require.NotNil(t, instance)
	assert.Equal(t, "moox-collector_dev", instance.PackageID)
}

func TestExecuteCreateInstanceItemReconcilesAfterItemDeadline(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
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
	svc := newInstanceItemTestService(catalog, fake)
	metadata, err := structpb.NewStruct(map[string]any{"function_name": "collector-000"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = svc.executeCreateInstanceItem(ctx, "crypto", &pb.FunctionInstanceCreateItem{
		CloudAccountId:      "account-a",
		Region:              "ap-guangzhou",
		PackageId:           "moox-collector_dev",
		CreateReservationId: reservationID,
		Metadata:            metadata,
	}, 0)

	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	instance, getErr := catalog.GetInstance(context.Background(), "crypto", "collector-000")
	require.NoError(t, getErr)
	require.NotNil(t, instance)
}

func TestExecuteDeployInstanceItemUpdatesCodeAndCatalogPackage(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	seedInstanceForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{info: &tencentscf.FunctionInfo{Status: "Active", Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"}}},
		{info: &tencentscf.FunctionInfo{Status: "Active"}},
	}}
	svc := newInstanceItemTestService(catalog, fake)

	summary, err := svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "instance-a", PackageId: "moox-collector_dev",
	})

	require.NoError(t, err)
	assert.Equal(t, "deployed package moox-collector_dev to instance-a", summary)
	instance, err := catalog.GetInstance(context.Background(), "crypto", "instance-a")
	require.NoError(t, err)
	require.NotNil(t, instance)
	assert.Equal(t, "moox-collector_dev", instance.PackageID)
}

func TestExecuteDeployInstanceItemRejectsFailedFunctionWithMatchingMarker(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	seedInstanceForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{
		Status:      "Failed",
		Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
	}}}}
	svc := newInstanceItemTestService(catalog, fake)

	_, err := svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "instance-a", PackageId: "moox-collector_dev",
	})

	require.ErrorContains(t, err, `entered status "Failed"`)
}

func TestExecuteDeployInstanceItemRejectsFailedStatusAfterConfigurationUpdate(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	seedInstanceForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
		{info: &tencentscf.FunctionInfo{
			Status:      "Active",
			Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"},
		}},
		{info: &tencentscf.FunctionInfo{Status: "Active"}},
		{info: &tencentscf.FunctionInfo{Status: "Failed"}},
	}}
	svc := newInstanceItemTestService(catalog, fake)

	_, err := svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "instance-a", PackageId: "moox-collector_dev",
	})

	require.ErrorContains(t, err, `entered status "Failed"`)
	instance, getErr := catalog.GetInstance(context.Background(), "crypto", "instance-a")
	require.NoError(t, getErr)
	require.NotNil(t, instance)
	assert.Equal(t, "old-package", instance.PackageID)
}

func TestExecuteDeployInstanceItemRejectsMissingInstance(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	svc := newInstanceItemTestService(catalog, &fakeSCFClient{})

	_, err := svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "missing", PackageId: "pkg",
	})

	require.ErrorContains(t, err, "instance not found")
}

func TestExecuteDeployInstanceItemRejectsUnavailablePackage(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	seedInstanceForDeploy(t, catalog)
	pkg, err := catalog.GetPackage(context.Background(), "crypto", "moox-collector_dev")
	require.NoError(t, err)
	require.NotNil(t, pkg)
	pkg.Status = "uploading"
	require.NoError(t, catalog.UpsertPackage(context.Background(), *pkg))
	svc := newInstanceItemTestService(catalog, &fakeSCFClient{})

	_, err = svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "instance-a", PackageId: "moox-collector_dev",
	})

	require.ErrorContains(t, err, "not available")
}

func TestExecuteDeployInstanceItemReconcilesAcceptedTencentTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	seedGenericInstanceItemPackage(t, catalog)
	seedInstanceForDeploy(t, catalog)
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{
		Status:      "Updating",
		Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "moox-collector_dev"},
	}}}}
	svc := newInstanceItemTestService(catalog, fake)

	_, err := svc.executeDeployInstanceItem(context.Background(), "crypto", &pb.FunctionInstanceDeployItem{
		InstanceId: "instance-a", PackageId: "moox-collector_dev",
	})

	require.NoError(t, err)
	assert.Empty(t, fake.updated)
	require.Len(t, fake.configured, 1)
	assert.Equal(t, "moox-collector_dev", fake.configured[0].Environment["MOOX_CODE_PACKAGE_ID"])
}

func newInstanceItemTestService(catalog *store.CatalogRepository, fake scfProvisioner) *Service {
	return &Service{
		catalog: catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{
			SecretID: "secret-id", SecretKey: "secret-key",
		}},
		scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
}

func seedInstanceForDeploy(t *testing.T, catalog *store.CatalogRepository) {
	t.Helper()
	require.NoError(t, catalog.UpsertInstance(context.Background(), store.FunctionInstance{
		SpaceID: "crypto", InstanceID: "instance-a", CloudAccountID: "account-a",
		PackageID: "old-package", FunctionType: "scf-event", Provider: "tencent-scf",
		Region: "ap-guangzhou", Namespace: "collector", FunctionName: "collector-0",
		Metadata: `{"handler":"main"}`,
	}))
}
