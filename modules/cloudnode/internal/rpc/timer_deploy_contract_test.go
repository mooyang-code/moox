package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func completeTimerEnvironment() map[string]string {
	return map[string]string{
		"MOOX_GATEWAY_CALLER":                     "collector",
		"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON": `{"moox-collector":"` + strings.Repeat("a", 64) + `"}`,
		"MOOX_SPACE_ID":                           "crypto", "MOOX_CODE_PACKAGE_ID": "old-package", "MOOX_FETCH_TIMEOUT_SECONDS": "60",
		"MOOX_STORAGE_RPC_GATEWAY_TARGET": "ip://storage.example:11003", "MOOX_GATEWAY_NODE_ID": "storage-node", "MOOX_GATEWAY_TARGET_NODE": "storage-node",
		"MOOX_GATEWAY_SERVICE_KEY_ID": "collector", "MOOX_GATEWAY_SERVICE_SECRET_KEY": "private-test-secret",
		"MOOX_COLLECTOR_RPC_GATEWAY_TARGET": "ip://collector.example:11004", "MOOX_COLLECTOR_GATEWAY_TARGET_NODE": "collector-node",
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_CLS_TOPIC_ID": "topic",
		"MOOX_CLS_TIMEOUT_MS": "3000", "MOOX_CLS_SECRET_ID": "cls-id", "MOOX_CLS_SECRET_KEY": "cls-secret",
		"MOOX_EVENTBUS_NATS_URL": "tls://eventbus.example:4222", "MOOX_EVENTBUS_NATS_USERNAME": "collector", "MOOX_EVENTBUS_NATS_PASSWORD": "bus-secret",
		"MOOX_EVENTBUS_NATS_TLS_CA_FILE": "certs/eventbus-ca.pem",
	}
}

func TestMarketFetcherPackageTimerCreateCannotBypassTimeoutContract(t *testing.T) {
	for _, test := range []struct{ name, outer, runtime, metadata, missing string }{
		{"short matching", "15", "15", `{"biz_type":"market_fetcher","function_mode":"kline"}`, ""},
		{"missing mode and runtime", "15", "", `{}`, ""},
		{"missing biz_type", "15", "60", `{"function_mode":"kline"}`, ""},
		{"missing Storage app keys", "60", "60", `{}`, "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			calls := 0
			svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { calls++; return &fakeSCFClient{} }}
			metadata := &structpb.Struct{}
			require.NoError(t, metadata.UnmarshalJSON([]byte(test.metadata)))
			env := completeTimerEnvironment()
			env["MOOX_FETCH_TIMEOUT_SECONDS"] = test.runtime
			delete(env, test.missing)
			_, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", TriggerType: "timer", Config: map[string]string{"timeout": test.outer}, Environment: env, Metadata: metadata}, 0)
			if test.missing != "" {
				require.ErrorContains(t, err, test.missing)
			} else {
				require.ErrorContains(t, err, "Timer")
			}
			require.Zero(t, calls)
		})
	}
}

func TestMarketFetcherPackageTimerCreateUsesSafeDefaultsWithoutOptionalMetadata(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	env := completeTimerEnvironment()
	env["MOOX_CODE_PACKAGE_ID"] = "moox-collector_dev"
	delete(env, "MOOX_FETCH_TIMEOUT_SECONDS")
	fake := &fakeSCFClient{getResults: []fakeSCFGetResult{{err: errors.New("ResourceNotFound.FunctionName")}, {info: &tencentscf.FunctionInfo{Status: "Active", MemorySize: 64, Timeout: 60, Environment: env}}}}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
	_, err := svc.executeCreateNodeItem(context.Background(), "crypto", &pb.NodeCreateItem{CloudAccountId: "account-a", Region: "ap-singapore", PackageId: "moox-collector_dev", TriggerType: "timer", Config: map[string]string{"memory_size": "64"}, Environment: env}, 0)
	require.NoError(t, err)
	require.Len(t, fake.created, 1)
	require.Equal(t, int64(60), fake.created[0].Timeout)
}

func TestMarketFetcherLegacyInstrumentTimerDeployRetainsSeparateTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "legacy", CloudAccountID: "account-a", PackageID: "old-package", TriggerType: "timer", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "legacy", Metadata: `{"function_mode":"instrument_snapshot"}`}))
	fake := &fakeSCFClient{currentEnvironment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"}, currentTimeout: 15}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "legacy", PackageId: "moox-collector_dev", Config: map[string]string{"timeout": "15"}})
	require.NoError(t, err)
	require.Len(t, fake.configured, 1)
	require.Equal(t, int64(15), fake.configured[0].Timeout)
}

func TestMarketFetcherTimerDeployValidatesFinalContractBeforeCodeUpdate(t *testing.T) {
	for _, test := range []struct {
		name, missing string
		downgrade     bool
		wantSuccess   bool
	}{
		{"timeout downgrade", "", true, false}, {"Storage route", "MOOX_STORAGE_RPC_GATEWAY_TARGET", false, false},
		{"Collector route", "MOOX_COLLECTOR_RPC_GATEWAY_TARGET", false, false}, {"Gateway identity", "MOOX_GATEWAY_SERVICE_SECRET_KEY", false, false},
		{"CLS credential", "MOOX_CLS_SECRET_KEY", false, false}, {"EventBus credential", "MOOX_EVENTBUS_NATS_PASSWORD", false, false},
		{"complete remote patch", "", false, true},
		{"Storage app keys", "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedSCFAccountAndPackage(t, catalog)
			require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "timer", CloudAccountID: "account-a", PackageID: "old-package", TriggerType: "timer", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "timer", CreateTime: time.Now(), Metadata: `{}`}))
			env := completeTimerEnvironment()
			delete(env, test.missing)
			fake := &fakeSCFClient{currentEnvironment: env, currentTimeout: 60}
			svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
			config := map[string]string{}
			patch := map[string]string{}
			if test.downgrade {
				config["timeout"] = "15"
				patch["MOOX_FETCH_TIMEOUT_SECONDS"] = "15"
			}
			_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "timer", PackageId: "moox-collector_dev", Config: config, Environment: patch})
			if test.wantSuccess {
				require.NoError(t, err)
				require.Len(t, fake.updated, 1)
				require.Len(t, fake.configured, 1)
				for key, value := range env {
					if key != "MOOX_CODE_PACKAGE_ID" {
						require.Equal(t, value, fake.configured[0].Environment[key])
					}
				}
				return
			}
			require.Error(t, err)
			require.Empty(t, fake.updated)
			require.Empty(t, fake.configured)
		})
	}
}

func TestMarketFetcherTimerDeployRemovesLegacyEmbeddedCA(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{SpaceID: "crypto", NodeID: "timer", CloudAccountID: "account-a", PackageID: "old-package", TriggerType: "timer", NodeType: "scf-event", Region: "ap-singapore", FunctionName: "timer", CreateTime: time.Now(), Metadata: `{}`}))
	env := completeTimerEnvironment()
	env["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"] = "cGVt"
	fake := &fakeSCFClient{currentEnvironment: env, currentTimeout: 60}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}

	_, err := svc.executeDeployNodeItem(context.Background(), "crypto", &pb.NodeDeployItem{NodeId: "timer", PackageId: "moox-collector_dev", Environment: map[string]string{"MOOX_SPACE_ID": "crypto"}})
	require.NoError(t, err)
	require.Len(t, fake.configured, 1)
	require.NotContains(t, fake.configured[0].Environment, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
}
