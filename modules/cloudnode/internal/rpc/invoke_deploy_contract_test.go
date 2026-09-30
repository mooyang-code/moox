package rpc

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
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
	delete(env, "MOOX_COLLECTOR_RPC_GATEWAY_TARGET")
	delete(env, "MOOX_COLLECTOR_GATEWAY_TARGET_NODE")
	env["MOOX_FETCH_TIMEOUT_SECONDS"] = timeout
	return env
}

func TestMarketFetcherInvokeCreateRejectsIncompleteBasicEnvironmentBeforeCloudAPI(t *testing.T) {
	for _, key := range []string{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_GATEWAY_CALLER"} {
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
		{"missing caller", "MOOX_GATEWAY_CALLER", "", true},
		{"wrong caller", "MOOX_GATEWAY_CALLER", "strategy", true},
		{"valid Invoke needs no Claim route or Timer timeout", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "invoke", CloudAccountID: "account-a", TriggerType: "invoke", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "invoke"}))
			env := completeTimerEnvironment()
			delete(env, "MOOX_COLLECTOR_RPC_GATEWAY_TARGET")
			delete(env, "MOOX_COLLECTOR_GATEWAY_TARGET_NODE")
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
		{"invalid primary", "90", "invalid", "90", true},
		{"zero primary", "90", "0", "90", true},
		{"empty primary", "90", "", "90", true},
		{"invalid alias", "60", "unset", "invalid", true},
		{"invalid ignored alias", "90", "90", "invalid", true},
		{"negative primary", "90", "-1", "unset", true},
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
