package rpc

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
)

func TestMarketFetcherPackageWorkloadIdentityCannotBypassTimerContract(t *testing.T) {
	for _, workload := range []string{" MARKET_FETCHER ", " Market_Fetcher"} {
		for _, operation := range []string{"create", "deploy"} {
			t.Run(operation+workload, func(t *testing.T) {
				catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
				seedSCFAccountAndPackage(t, catalog)
				pkg, err := catalog.GetPackage(context.Background(), "crypto", "moox-collector_dev")
				require.NoError(t, err)
				pkg.WorkloadType = workload
				require.NoError(t, catalog.UpsertPackage(context.Background(), *pkg))
				env := completeTimerEnvironment()
				env["MOOX_FETCH_TIMEOUT_SECONDS"] = "15"
				fake := &fakeSCFClient{currentEnvironment: env, currentTimeout: 60}
				svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
				if operation == "create" {
					_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{CloudAccountId: "account-a", Region: "ap-singapore", PackageId: pkg.PackageID, TriggerType: "timer", Config: map[string]string{"timeout": "15"}, Environment: env}, 0)
				} else {
					require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "timer", CloudAccountID: "account-a", TriggerType: "timer", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "timer"}))
					_, err = svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "timer", PackageId: pkg.PackageID, Config: map[string]string{"timeout": "15"}})
				}
				require.ErrorContains(t, err, "Timer runtime timeout")
				require.Empty(t, fake.created)
				require.Empty(t, fake.updated)
				require.Empty(t, fake.configured)
			})
		}
	}
}

func completeInvokeEnvironment(timeout string) map[string]string {
	env := completeTimerEnvironment()
	env["MOOX_FETCH_TIMEOUT_SECONDS"] = timeout
	return env
}

func TestMarketFetcherInvokeCreateRejectsIncompleteBasicEnvironmentBeforeCloudAPI(t *testing.T) {
	for _, key := range []string{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_CALLER"} {
		t.Run(key, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			calls := 0
			svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { calls++; return &fakeSCFClient{} }}
			env := completeInvokeEnvironment("90")
			delete(env, key)
			_, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", TriggerType: "invoke", Config: map[string]string{"timeout": "90"}, Environment: env}, 0)
			require.ErrorContains(t, err, key)
			require.Zero(t, calls)
		})
	}
}

func TestMarketFetcherInvokeDeployValidatesBasicEnvironmentWithoutTimerRules(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		wantError        bool
	}{
		{"missing appkeys", "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "", true},
		{"missing EventBus", "MOOX_EVENTBUS_NATS_PASSWORD", "", true},
		{"missing caller", "MOOX_CALLER", "", true},
		{"wrong caller", "MOOX_CALLER", "strategy", true},
		{"valid Invoke needs no Timer timeout", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "invoke", CloudAccountID: "account-a", TriggerType: "invoke", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "invoke"}))
			env := completeTimerEnvironment()
			env["MOOX_FETCH_TIMEOUT_SECONDS"] = "90"
			if test.key != "" {
				env[test.key] = test.value
			}
			fake := &fakeSCFClient{currentEnvironment: env, currentTimeout: 90}
			svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
			_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "invoke", PackageId: "moox-collector_dev", Config: map[string]string{"timeout": "90"}})
			if test.wantError {
				require.Error(t, err)
				require.Empty(t, fake.updated)
				require.Empty(t, fake.configured)
			} else {
				require.NoError(t, err)
				require.Len(t, fake.updated, 1)
				require.Equal(t, int64(90), fake.configured[0].Timeout)
			}
		})
	}
}

func TestMarketFetcherInvokeTimeoutContractBeforeCloudMutation(t *testing.T) {
	for _, test := range []struct {
		name, outer, primary, alias string
		wantError                   bool
	}{
		{"mismatch", "90", "60", "unset", true},
		{"matching 90", "90", "90", "unset", false},
		{"alias fallback", "90", "unset", "90", false},
		{"default fallback", "60", "unset", "unset", false},
		{"primary precedence", "90", "90", "60", false},
		{"invalid primary falls back to alias", "90", "invalid", "90", false},
		{"zero primary falls back to alias", "90", "0", "90", false},
		{"empty primary falls back to alias", "90", "", "90", false},
		{"negative primary falls back to alias", "90", "-1", "90", false},
		{"invalid alias falls back to default", "60", "unset", "invalid", false},
		{"empty alias falls back to default", "60", "unset", "", false},
		{"zero alias falls back to default", "60", "unset", "0", false},
		{"negative alias falls back to default", "60", "unset", "-1", false},
		{"invalid primary falls back to default", "60", "invalid", "unset", false},
		{"empty primary falls back to default", "60", "", "unset", false},
		{"zero primary falls back to default", "60", "0", "unset", false},
		{"negative primary falls back to default", "60", "-1", "unset", false},
		{"both invalid fall back to default", "60", "invalid", "invalid", false},
		{"invalid ignored alias", "90", "90", "invalid", false},
		{"empty ignored alias", "90", "90", "", false},
		{"fallback budget mismatch", "90", "invalid", "invalid", true},
		{"invalid outer", "invalid", "90", "unset", true},
		{"zero outer", "0", "90", "unset", true},
	} {
		for _, operation := range []string{"create", "deploy"} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
				seedSCFAccountAndPackage(t, catalog)
				env := completeInvokeEnvironment("90")
				delete(env, "MOOX_FETCH_TIMEOUT_SECONDS")
				if test.primary != "unset" {
					env["MOOX_FETCH_TIMEOUT_SECONDS"] = test.primary
				}
				if test.alias != "unset" {
					env["MOOX_MARKET_FETCH_TIMEOUT_SECONDS"] = test.alias
				}
				outer, _ := strconv.ParseInt(test.outer, 10, 64)
				fake := &fakeSCFClient{currentEnvironment: env, currentTimeout: outer}
				factoryCalls := 0
				svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { factoryCalls++; return fake }}
				var err error
				if operation == "create" {
					env["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
					fake.getResults = []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}}
					_, err = svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", TriggerType: "invoke", Config: map[string]string{"memory_size": "64", "timeout": test.outer}, Environment: env}, 0)
				} else {
					require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "invoke", CloudAccountID: "account-a", TriggerType: "invoke", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "invoke"}))
					_, err = svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "invoke", PackageId: "moox-collector_dev", Config: map[string]string{"timeout": test.outer}})
				}
				if test.wantError {
					require.ErrorContains(t, err, "Invoke runtime timeout")
					require.Empty(t, fake.created)
					require.Empty(t, fake.updated)
					require.Empty(t, fake.configured)
					if operation == "create" {
						require.Zero(t, factoryCalls)
					}
				} else {
					require.NoError(t, err)
					if operation == "create" {
						require.Len(t, fake.created, 1)
					} else {
						require.Len(t, fake.updated, 1)
					}
				}
			})
		}
	}
}

func TestMarketFetcherInvokeReadbackAcceptsHandlerFallback(t *testing.T) {
	for _, test := range []struct {
		name, primary, alias string
		outer                int64
	}{
		{"invalid primary uses alias", "invalid", "90", 90},
		{"empty primary uses alias", "", "90", 90},
		{"zero primary uses alias", "0", "90", 90},
		{"negative primary uses alias", "-1", "90", 90},
		{"invalid alias uses default", "unset", "invalid", 60},
		{"empty alias uses default", "unset", "", 60},
		{"zero alias uses default", "unset", "0", 60},
		{"negative alias uses default", "unset", "-1", 60},
		{"both invalid use default", "invalid", "invalid", 60},
	} {
		for _, operation := range []string{"create", "deploy"} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
				seedSCFAccountAndPackage(t, catalog)
				requestEnv := completeInvokeEnvironment("60")
				requestEnv["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
				reservationID := "invoke-create-reservation"
				requestEnv["MOOX_CREATE_RESERVATION_ID"] = reservationID
				delete(requestEnv, "MOOX_FETCH_TIMEOUT_SECONDS")
				if test.outer == 90 {
					requestEnv["MOOX_MARKET_FETCH_TIMEOUT_SECONDS"] = "90"
				}
				remoteEnv := copyStringMap(requestEnv)
				if test.primary != "unset" {
					remoteEnv["MOOX_FETCH_TIMEOUT_SECONDS"] = test.primary
				}
				remoteEnv["MOOX_MARKET_FETCH_TIMEOUT_SECONDS"] = test.alias
				fake := &fakeSCFClient{currentEnvironment: remoteEnv, currentTimeout: test.outer}
				svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
				var err error
				if operation == "create" {
					item := &pb.NodeCreateItem{CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", TriggerType: "invoke", Config: map[string]string{"timeout": strconv.FormatInt(test.outer, 10)}, Environment: requestEnv, CreateReservationId: reservationID}
					_, err = svc.executeCreateNodeItem(context.Background(), "crypto", item, 0)
					require.NoError(t, err)
					node, err := catalog.GetNode(context.Background(), "crypto", cloudNodeFromCreateItem("crypto", item, 0).NodeID)
					require.NoError(t, err)
					require.True(t, parseJSONMap(node.Metadata)["deployment_ready"].(bool))
				} else {
					require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "invoke", CloudAccountID: "account-a", PackageID: "old-package", TriggerType: "invoke", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "invoke"}))
					initialEnv := copyStringMap(requestEnv)
					initialEnv["MOOX_CODE_PACKAGE_ID"] = "old-package"
					active := &tencentscf.FunctionInfo{Status: "Active", Environment: initialEnv, Timeout: test.outer}
					fake.getResults = []fakeSCFGetResult{{info: active}, {info: active}, {info: active}, {info: &tencentscf.FunctionInfo{Status: "Active", Environment: remoteEnv, Timeout: test.outer}}}
					_, err = svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "invoke", PackageId: "moox-collector_dev"})
					require.NoError(t, err)
					node, err := catalog.GetNode(context.Background(), "crypto", "invoke")
					require.NoError(t, err)
					require.Equal(t, "moox-collector_dev", node.PackageID)
				}
			})
		}
	}
}

func TestMarketFetcherInvokeCreateValidatesReadbackTimeoutBeforeReady(t *testing.T) {
	for _, path := range []string{"existing", "created", "ambiguous create"} {
		t.Run(path, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			requestEnv := completeInvokeEnvironment("60")
			requestEnv["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
			delete(requestEnv, "MOOX_FETCH_TIMEOUT_SECONDS")
			remoteEnv := copyStringMap(requestEnv)
			remoteEnv["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
			reservationID := "ambiguous-create-reservation"
			if path == "existing" || path == "ambiguous create" {
				remoteEnv["MOOX_CREATE_RESERVATION_ID"] = reservationID
			}
			remoteEnv["MOOX_FETCH_TIMEOUT_SECONDS"] = "90"
			fake := &fakeSCFClient{currentEnvironment: remoteEnv, currentTimeout: 60}
			if path != "existing" {
				fake.getResults = []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}}
			}
			if path == "ambiguous create" {
				fake.createErr = context.DeadlineExceeded
			}
			svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
			item := &pb.NodeCreateItem{CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", TriggerType: "invoke", Config: map[string]string{"timeout": "60"}, Environment: requestEnv}
			if path == "existing" || path == "ambiguous create" {
				item.CreateReservationId = reservationID
			}
			_, err := svc.executeCreateNodeItem(context.Background(), "crypto", item, 0)
			require.ErrorContains(t, err, "Invoke runtime timeout")
			node, err := catalog.GetNode(context.Background(), "crypto", cloudNodeFromCreateItem("crypto", item, 0).NodeID)
			require.NoError(t, err)
			require.NotNil(t, node, "mismatched readback must leave a discoverable cleanup reservation")
			require.False(t, metadataBool(parseJSONMap(node.Metadata), "deployment_ready"), "mismatched readback cannot persist deployment_ready")
			require.Empty(t, fake.configured)
			if path == "existing" {
				require.Empty(t, fake.created)
			}
		})
	}
}

func TestMarketFetcherInvokeDeployValidatesVerifiedTimeoutWithoutDesiredTimeout(t *testing.T) {
	for _, drift := range []string{"outer", "environment"} {
		t.Run(drift, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "invoke", CloudAccountID: "account-a", PackageID: "old-package", TriggerType: "invoke", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "invoke"}))
			env := completeInvokeEnvironment("90")
			verifiedEnv := copyStringMap(env)
			verifiedEnv["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
			verifiedTimeout := int64(90)
			if drift == "outer" {
				verifiedTimeout = 60
			} else {
				verifiedEnv["MOOX_FETCH_TIMEOUT_SECONDS"] = "60"
			}
			active := &tencentscf.FunctionInfo{Status: "Active", Environment: env, Timeout: 90}
			fake := &fakeSCFClient{getResults: []fakeSCFGetResult{
				{info: active}, // initial read
				{info: active}, // code update becomes active
				{info: active}, // configuration update becomes active
				{info: &tencentscf.FunctionInfo{Status: "Active", Environment: verifiedEnv, Timeout: verifiedTimeout}},
			}}
			svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
			_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "invoke", PackageId: "moox-collector_dev"})
			require.ErrorContains(t, err, "Invoke runtime timeout")
			require.Len(t, fake.updated, 1)
			require.Len(t, fake.configured, 1)
			node, err := catalog.GetNode(context.Background(), "crypto", "invoke")
			require.NoError(t, err)
			require.Equal(t, "old-package", node.PackageID, "failed verification cannot persist the new package")
		})
	}
}
