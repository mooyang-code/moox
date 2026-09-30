package rpc

import (
	"context"
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
