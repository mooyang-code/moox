package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestCollectorSCFCanaryRejectsUnverifiedSuccess(t *testing.T) {
	for _, result := range []map[string]any{
		{"success": true},
		{"success": true, "data": map[string]any{"status": "succeeded"}},
		{"success": true, "period_status": "complete", "primary_row_present": true, "view_row_present": true},
	} {
		t.Run(string(mustCanaryJSON(t, result)), func(t *testing.T) {
			client := canaryResponseClient(t, result)
			err := runCollectorSCFCanary(context.Background(), client, collectorPublishOptions{}, "canary-node")
			require.ErrorContains(t, err, "verification incomplete")
			require.ErrorContains(t, err, "ownership-validated task")
			require.ErrorContains(t, err, "View proof")
		})
	}
}

func TestCollectorSCFCanaryRejectsBusinessFailure(t *testing.T) {
	for _, result := range []map[string]any{{"success": false}, {"success": "true"}, {}} {
		proof, _, _ := collectorCanaryTestProof()
		err := runCollectorSCFCanary(context.Background(), canaryResponseClient(t, result), collectorPublishOptions{canaryProof: proof}, "canary-node")
		require.ErrorContains(t, err, "unsuccessful response")
	}
}

func TestCollectorSCFCanaryRequiresStorageProofAfterTransportSuccess(t *testing.T) {
	proof, primary, _ := collectorCanaryTestProof()
	primary.status = "degraded"
	err := runCollectorSCFCanary(context.Background(), canaryResponseClient(t, map[string]any{"success": true}), collectorPublishOptions{canaryProof: proof}, "canary-node")
	require.ErrorContains(t, err, "Storage proof failed")
	require.ErrorContains(t, err, "degraded")
}

func TestCollectorSCFCanarySucceedsOnlyAfterExactStorageProof(t *testing.T) {
	proof, primary, view := collectorCanaryTestProof()
	primary.primaryRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.viewRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.complete = true
	err := runCollectorSCFCanary(context.Background(), canaryResponseClient(t, map[string]any{"success": true}), collectorPublishOptions{canaryProof: proof}, "canary-node")
	require.NoError(t, err)
}

func TestCollectorSCFCanaryEnsuresPeriodBeforeInvoke(t *testing.T) {
	proof, primary, view := collectorCanaryTestProof()
	primary.ensureStatus = "waiting"
	primary.status = "complete"
	primary.primaryRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.viewRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.complete = true

	var ensureCallsAtInvoke atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ensureCallsAtInvoke.Store(primary.ensureCalls.Load())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"scf":{"code":0,"result":{"success":true}}}`))
	}))
	defer server.Close()

	err := runCollectorSCFCanary(context.Background(), adminclient.New(server.URL), collectorPublishOptions{canaryProof: proof}, "canary-node")
	require.NoError(t, err)
	require.EqualValues(t, 1, ensureCallsAtInvoke.Load(), "Storage must have persisted the period contract before SCF writes it")
	require.NotNil(t, primary.ensuredExpectation)
	require.Equal(t, proof.entry.GetSpaceId(), primary.ensuredExpectation.GetSpaceId())
	require.Equal(t, proof.entry.GetDatasetId(), primary.ensuredExpectation.GetDatasetId())
	require.Equal(t, proof.period.UTC().Unix(), primary.ensuredExpectation.GetPeriodTime())
	require.GreaterOrEqual(t, primary.ensuredExpectation.GetDeadlineAt(), time.Now().UTC().Add(collectorCanaryProofTimeout).Unix())
	require.Equal(t, proof.entry.GetSeriesHash(), primary.ensuredExpectation.GetSeriesHash())
	require.Equal(t, proof.entry.GetExpectedCount(), primary.ensuredExpectation.GetExpectedCount())
	require.Len(t, primary.ensuredExpectation.GetSeriesSnapshot(), 1)
}

func TestCollectorSCFCanaryDoesNotInvokeWhenEnsureFails(t *testing.T) {
	proof, primary, view := collectorCanaryTestProof()
	primary.ensureRetInfo = &commonpb.RetInfo{Code: storagepb.ErrorCode_CONFLICT, Msg: "period already occupied"}
	primary.status = "complete"
	primary.primaryRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.viewRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.complete = true
	var invokeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		invokeCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"scf":{"code":0,"result":{"success":true}}}`))
	}))
	defer server.Close()

	err := runCollectorSCFCanary(context.Background(), adminclient.New(server.URL), collectorPublishOptions{canaryProof: proof}, "canary-node")
	require.ErrorContains(t, err, "ensure Storage period")
	require.Zero(t, invokeCalls.Load())
}

func TestPublishCollectorFunctionRejectsUnverifiedCanaryBeforeControlPlaneAccess(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	collectorRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "collector"))
	require.NoError(t, err)
	activeManifest := `[admin]
username = "admin"
password = "test-password"
[tencent_cloud]
secret_id = "test-id"
secret_key = "test-key"
[eventbus]
host = "192.0.2.10"
tls_enabled = true
[hosts."192.0.2.10"]
username = "ubuntu"
password = "test-password"
[hosts."192.0.2.20"]
username = "ubuntu"
password = "test-password"
[control_host]
host = "192.0.2.10"
[scf_fetcher]
enabled = true
[scf_fetcher.cloud_account]
account_id = "account-a"
account_name = "test"
credential_secret_id = "cls-secret"
app_id = "1234567890"
cos_region = "ap-guangzhou"
cos_bucket = "test-bucket"
[[scf_fetcher.spaces]]
space_id = "crypto"
entrypoint = "market_data"
market_id = "crypto"
instrument_type = "spot"
provider_id = "binance"
source_id = "spot_http"
package_config_dir = "scf/market_data"
namespace = "moox-crypto"
function_prefix = "moox-fetcher-crypto"
package_name = "moox-collector-crypto"
public_net_status = "ENABLE"
timer_function_count = 1
memory_size = 64
timeout_seconds = 60
invoke_timeout_seconds = 60
realtime_batch_size = 10
max_inflight_requests = 10
request_timeout_ms = 1000
http_max_attempts = 4
storage_max_attempts = 1
storage_timeout_ms = 5000
max_retry_attempts = 3
collector_rpc_gateway_target = "ip://192.0.2.10:11003"
collector_gateway_target_node = "control"
storage_gateway_node_id = "control"
storage_gateway_host = "192.0.2.20"
[[scf_fetcher.spaces.regions]]
region = "ap-guangzhou"
enabled = false
function_count = 0
[[scf_fetcher.spaces.regions]]
region = "ap-singapore"
enabled = true
function_count = 1
`
	manifestPath := filepath.Join(t.TempDir(), "moox.toml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(activeManifest), 0o600))
	activeSpace, _, err := loadCollectorSCFFetcherConfigSnapshot(manifestPath, "crypto")
	require.NoError(t, err)
	require.NotNil(t, activeSpace)
	marketConfig, err := os.ReadFile(filepath.Join(collectorRoot, "configs/scf/market_data/sources/market/binance.yaml"))
	require.NoError(t, err)
	eventBusCA, err := os.ReadFile(filepath.Join(filepath.Dir(credentialFile), "eventbus-ca.pem"))
	require.NoError(t, err)
	zipPath := filepath.Join(t.TempDir(), "collector.zip")
	writeMinimalSCFZip(t, zipPath, map[string]string{
		"main":                        "binary",
		"sources/market/binance.yaml": string(marketConfig),
		"certs/eventbus-ca.pem":       string(eventBusCA),
	})

	previousCLS := newCollectorCLSAPI
	newCollectorCLSAPI = func(string, string, string) (tencent.CLSAPI, error) {
		return collectorCLSAPI{}, nil
	}
	t.Cleanup(func() { newCollectorCLSAPI = previousCLS })

	for _, tc := range []struct {
		name, file, region, trigger string
		zipPath                     string
	}{
		{name: "ad hoc invoke", region: "ap-guangzhou", trigger: "invoke", zipPath: zipPath},
		{name: "ad hoc timer", region: "ap-guangzhou", trigger: "timer", zipPath: zipPath},
		{name: "active manifest selects disabled region", file: manifestPath, region: "ap-guangzhou", trigger: "invoke"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads, uploads, mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/admin/cloudnode/ListCloudAccounts", "/api/service/cloudnode/ListCloudAccounts":
					reads.Add(1)
					_, _ = w.Write([]byte(`{"ret_info":{"code":0},"accounts":[{"account_id":"account-a","provider":"tencent","credential_secret_id":"cls-secret"}]}`))
				case "/api/admin/secret/GetSecretValue", "/api/service/secret/GetSecretValue":
					reads.Add(1)
					_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"secret_id":"cls-secret","category":"cloud","provider":"tencent","status":"active","key_id":"cls-id","secret_value":"cls-key"}}`))
				case "/api/admin/cloudnode/GetNodeList", "/api/service/cloudnode/GetNodeList":
					reads.Add(1)
					_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
				case "/api/admin/cloudnode/InitPackageUpload":
					uploads.Add(1)
					http.Error(w, "package upload must be blocked", http.StatusInternalServerError)
				case "/api/admin/cloudnode/CreateCloudAccount", "/api/admin/cloudnode/SubmitCreateNodes", "/api/admin/cloudnode/SubmitDeployNodes", "/api/admin/cloudnode/SubmitUpdateNodeRuntimeConfigs":
					mutations.Add(1)
					http.Error(w, "node or account mutation must be blocked", http.StatusInternalServerError)
				default:
					t.Errorf("unexpected control-plane request: %s", strings.TrimSpace(r.URL.Path))
					http.Error(w, "unexpected request", http.StatusInternalServerError)
				}
			}))
			defer server.Close()

			_, err := publishCollectorFunction(context.Background(), collectorPublishOptions{
				collectorPackageOptions: collectorPackageOptions{CollectorRoot: collectorRoot, SpaceID: "crypto", PackageConfigDir: "scf/market_data"},
				ControlURL:              server.URL, File: tc.file, AccessToken: "test-token", ServiceAccessKey: "cli-test", ServiceSecretKey: "cli-secret", SpaceID: "crypto", ZipPath: tc.zipPath,
				CloudAccountID: "account-a", Region: tc.region, NodeCount: 1, TriggerType: tc.trigger, EventBusCredentialFile: credentialFile,
			})
			require.ErrorContains(t, err, "canary verification contract is unavailable")
			if tc.file == "" {
				require.Greater(t, reads.Load(), int32(0), "ad-hoc mode may inspect its existing fleet before failing closed")
			} else {
				require.Zero(t, reads.Load(), "manifest canary gate must precede external route discovery")
			}
			require.Zero(t, uploads.Load(), "canary proof preflight must precede package upload")
			require.Zero(t, mutations.Load(), "canary proof preflight must precede account and node mutations")
		})
	}
}

func TestCollectorSCFCanaryProofPreflightRejectsEmptyPlans(t *testing.T) {
	require.ErrorContains(t, validateCollectorSCFCanaryProofPreflight(), "ownership-validated disabled task")
}

func TestCollectorSCFReleaseCanaryOptionsUseIsolatedInvokeSlot(t *testing.T) {
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "crypto", Namespace: "moox-crypto", FunctionPrefix: "moox-fetcher-crypto"}
	region := setupconfig.SCFFetcherRegion{Region: "ap-nanjing", Enabled: true, FunctionCount: 3}
	limits := setupconfig.TencentSCFLimits{RegionLimits: map[string]setupconfig.TencentSCFRegionLimit{
		"ap-nanjing": {MaxNamespacesPerRegion: 3, MaxFunctionsPerNamespace: 2},
	}}
	base := collectorPublishOptions{FunctionNamePrefix: fetcher.FunctionPrefix, TriggerType: "timer", NodeCount: 3, StorageRPCGatewayTarget: "ip://storage:11003"}

	got, err := collectorSCFReleaseCanaryOptions(base, fetcher, region, limits)
	require.NoError(t, err)
	assert.Equal(t, "moox-crypto-ns2", got.Namespace)
	assert.Equal(t, "ap-nanjing", got.Region)
	assert.Equal(t, "invoke", got.TriggerType)
	assert.Equal(t, 1, got.NodeCount)
	assert.Zero(t, got.IndexOffset)
	assert.Equal(t, "moox-fetcher-crypto-release-canary", got.FunctionNamePrefix)
	assert.Equal(t, base.StorageRPCGatewayTarget, got.StorageRPCGatewayTarget)
}

func TestCollectorSCFReleaseCanaryOptionsSupportStockCN(t *testing.T) {
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "stockcn", Namespace: "moox-stockcn", FunctionPrefix: "moox-fetcher-stockcn"}
	region := setupconfig.SCFFetcherRegion{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}
	limits := setupconfig.TencentSCFLimits{RegionLimits: map[string]setupconfig.TencentSCFRegionLimit{
		"ap-guangzhou": {MaxNamespacesPerRegion: 2, MaxFunctionsPerNamespace: 2},
	}}
	base := collectorPublishOptions{FunctionNamePrefix: fetcher.FunctionPrefix}

	got, err := collectorSCFReleaseCanaryOptions(base, fetcher, region, limits)
	require.NoError(t, err)
	assert.Equal(t, "moox-stockcn-ns2", got.Namespace)
	assert.Equal(t, "invoke", got.TriggerType)
	assert.Equal(t, 1, got.NodeCount)
}

func TestParseCollectorRegionPackageIDsRequiresExactEnabledRegionSet(t *testing.T) {
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "stockcn", Regions: []setupconfig.SCFFetcherRegion{
		{Region: "ap-guangzhou", Enabled: true, FunctionCount: 2},
		{Region: "ap-shanghai", Enabled: true, FunctionCount: 1},
		{Region: "ap-beijing", Enabled: false, FunctionCount: 1},
	}}

	got, err := parseCollectorRegionPackageIDs([]string{"AP-GUANGZHOU=package-a", "ap-shanghai=package-b"}, fetcher)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ap-guangzhou": "package-a", "ap-shanghai": "package-b"}, got)
	_, err = parseCollectorRegionPackageIDs([]string{"ap-guangzhou=package-a"}, fetcher)
	assert.ErrorContains(t, err, "ap-shanghai")
	_, err = parseCollectorRegionPackageIDs([]string{"ap-guangzhou=package-a", "AP-GUANGZHOU=package-b", "ap-shanghai=package-c"}, fetcher)
	assert.ErrorContains(t, err, "repeats")
	_, err = parseCollectorRegionPackageIDs([]string{"ap-guangzhou=package-a", "ap-shanghai=package-b", "ap-beijing=package-c"}, fetcher)
	assert.ErrorContains(t, err, "inactive or unknown")
}

func TestValidateStockCNPackageIdentityRejectsDifferentArtifactWithSameVersion(t *testing.T) {
	packageIDs := map[string]string{"ap-guangzhou": "stockcn_2026.10.03_artifact-a"}
	node := adminclient.CloudNode{NodeID: "invoke-0", PackageID: "stockcn_2026.10.03_artifact-a"}
	require.NoError(t, validateStockCNPackageIdentity(node, "ap-guangzhou", "2026.10.03", packageIDs))

	node.PackageID = "stockcn_2026.10.03_artifact-b"
	require.ErrorContains(t, validateStockCNPackageIdentity(node, "ap-guangzhou", "2026.10.03", packageIDs), "expected exact package")
}

func TestRequireCollectorFleetPackageIDRejectsConcurrentPackageReplacement(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "invoke-0", PackageID: "candidate-package"},
		{NodeID: "invoke-1", PackageID: "candidate-package"},
	}
	require.NoError(t, requireCollectorFleetPackageID(nodes, "candidate-package"))
	nodes[1].PackageID = "other-package"
	require.ErrorContains(t, requireCollectorFleetPackageID(nodes, "candidate-package"), "other-package")
}

func TestCollectorSCFReleaseCanaryOptionsSeparatesConcurrentReservationNames(t *testing.T) {
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "crypto", FunctionPrefix: "moox-fetcher-crypto"}
	region := setupconfig.SCFFetcherRegion{Region: "ap-southeast-1", Enabled: true, FunctionCount: 3}
	base := collectorPublishOptions{FunctionNamePrefix: fetcher.FunctionPrefix}
	reservationA := "scf-release-0123456789abcdef0123456789abcdef"
	reservationB := "scf-release-fedcba98765432100123456789abcdef"
	base.canaryProof = &collectorSCFCanaryProof{reservationID: reservationA}
	got, err := collectorSCFReleaseCanaryOptions(base, fetcher, region, setupconfig.TencentSCFLimits{})
	require.NoError(t, err)
	require.Regexp(t, `^[A-Za-z][A-Za-z0-9_-]*$`, got.FunctionNamePrefix)
	require.NotEqual(t, base.FunctionNamePrefix, got.FunctionNamePrefix)
	functionName := got.FunctionNamePrefix + "-" + region.Region + "-0"
	require.LessOrEqual(t, len(functionName), 60, "CloudNode appends region and index to the function prefix")

	base.canaryProof = &collectorSCFCanaryProof{reservationID: reservationB}
	other, err := collectorSCFReleaseCanaryOptions(base, fetcher, region, setupconfig.TencentSCFLimits{})
	require.NoError(t, err)
	require.NotEqual(t, got.FunctionNamePrefix, other.FunctionNamePrefix, "reservations sharing the old eight-character suffix must remain isolated")
	otherFunctionName := other.FunctionNamePrefix + "-" + region.Region + "-0"
	require.LessOrEqual(t, len(otherFunctionName), 60)

	_, err = uniqueCollectorCanaryFunctionPrefix(reservationA, strings.Repeat("r", 30))
	require.ErrorContains(t, err, "60-character limit")
}

func TestCollectorSCFReleaseCanaryOptionsSeparatesConcurrentStockCNReservations(t *testing.T) {
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "stockcn", Namespace: "moox-stockcn", FunctionPrefix: "moox-fetcher-stockcn"}
	region := setupconfig.SCFFetcherRegion{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}
	base := collectorPublishOptions{FunctionNamePrefix: fetcher.FunctionPrefix}
	reservationA := "scf-release-0123456789abcdef0123456789abcdef"
	reservationB := "scf-release-fedcba98765432100123456789abcdef"
	base.canaryProof = &collectorSCFCanaryProof{reservationID: reservationA}
	first, err := collectorSCFReleaseCanaryOptions(base, fetcher, region, setupconfig.TencentSCFLimits{})
	require.NoError(t, err)
	base.canaryProof = &collectorSCFCanaryProof{reservationID: reservationB}
	second, err := collectorSCFReleaseCanaryOptions(base, fetcher, region, setupconfig.TencentSCFLimits{})
	require.NoError(t, err)
	assert.NotEqual(t, first.FunctionNamePrefix, second.FunctionNamePrefix)
	assert.LessOrEqual(t, len(first.FunctionNamePrefix+"-"+region.Region+"-0"), 60)
	assert.LessOrEqual(t, len(second.FunctionNamePrefix+"-"+region.Region+"-0"), 60)
}

func TestPublishCollectorSCFReleaseCanaryFleetCleansTemporaryFunctions(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		invokeResult         map[string]any
		interruptAfterDeploy bool
		wantError            bool
	}{
		{name: "success", invokeResult: map[string]any{"success": true}},
		{name: "business proof failure", invokeResult: map[string]any{"success": false}, wantError: true},
		{name: "interrupted after deployment", invokeResult: map[string]any{"success": true}, interruptAfterDeploy: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
			proof, primary, view := collectorCanaryTestProof()
			proof.reservationID = "scf-release-0123456789abcdef0123456789abcdef"
			primary.primaryRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
			view.viewRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
			view.complete = true

			const prefix = "release-canary-0123456789abcdef0123456789abcdef"
			var cancel context.CancelFunc
			ctx, cancelCtx := context.WithCancel(context.Background())
			cancel = cancelCtx
			defer cancelCtx()

			var nodes []adminclient.CloudNode
			var deleteIDs []string
			var sawCleanupWithLiveContext atomic.Bool
			var cancelOnce atomic.Bool
			var inventoryReads atomic.Int32
			var createJobTerminal atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/admin/cloudnode/GetNodeList":
					read := inventoryReads.Add(1)
					if tc.interruptAfterDeploy && createJobTerminal.Load() && read > 1 && cancelOnce.CompareAndSwap(false, true) {
						cancel()
					}
					if tc.interruptAfterDeploy && cancelOnce.Load() && read > 2 && r.Context().Err() == nil {
						sawCleanupWithLiveContext.Store(true)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "items": nodes, "page": map[string]any{"has_more": false}})
				case "/api/admin/cloudnode/SubmitCreateNodes":
					var request struct {
						Nodes []adminclient.NodeCreateItem `json:"nodes"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					require.Len(t, request.Nodes, 1)
					item := request.Nodes[0]
					nodes = append(nodes, adminclient.CloudNode{
						NodeID: "canary-node", CloudAccountID: item.CloudAccountID, PackageID: item.PackageID,
						Region: item.Region, Namespace: item.Namespace, NodeType: item.NodeType,
						TriggerType: item.TriggerType, BizType: "market_fetcher", FunctionName: prefix + "-ap-nanjing-0", Metadata: item.Metadata,
					})
					_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"create-job","operation":"NODE_BATCH_OPERATION_CREATE_NODES","total_count":1}`))
				case "/api/admin/cloudnode/GetNodeBatchChange":
					var request struct {
						JobID string `json:"job_id"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					jobID := request.JobID
					if jobID == "create-job" {
						createJobTerminal.Store(true)
						_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"create-job","status":"NODE_BATCH_STATUS_SUCCESS"},"items":[]}`))
						return
					}
					_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"delete-job","status":"NODE_BATCH_STATUS_SUCCESS"},"items":[]}`))
				case "/api/admin/cloudnode/InvokeFunction":
					response, err := json.Marshal(map[string]any{"ret_info": map[string]any{"code": 0}, "scf": map[string]any{"code": 0, "result": tc.invokeResult}})
					require.NoError(t, err)
					_, _ = w.Write(response)
				case "/api/admin/cloudnode/SubmitDeleteNodes":
					var request struct {
						NodeIDs []string `json:"node_ids"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					deleteIDs = append(deleteIDs, request.NodeIDs...)
					deleted := make(map[string]struct{}, len(request.NodeIDs))
					for _, nodeID := range request.NodeIDs {
						deleted[nodeID] = struct{}{}
					}
					remaining := nodes[:0]
					for _, node := range nodes {
						if _, ok := deleted[node.NodeID]; !ok {
							remaining = append(remaining, node)
						}
					}
					nodes = remaining
					_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"delete-job","operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":1}`))
				default:
					t.Errorf("unexpected canary control-plane request: %s", r.URL.Path)
					http.Error(w, "unexpected request", http.StatusInternalServerError)
				}
			}))
			defer server.Close()

			opts := collectorPublishOptions{
				CloudAccountID: "account-a", SpaceID: "crypto", Region: "ap-nanjing", Namespace: "canary-ns",
				NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke", NodeCount: 1,
				FunctionNamePrefix: prefix, StorageRPCGatewayTarget: "ip://192.0.2.20:11003",
				EventBusCredentialFile: credentialFile, StorageAppKeysJSON: collectorTestStorageAppKeysJSON,
				canaryProof: proof,
			}
			summary, canaryNode, err := publishCollectorSCFReleaseCanaryFleet(ctx, adminclient.New(server.URL), opts, "candidate-package")
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotEmpty(t, summary.JobID)
			require.Empty(t, canaryNode.NodeID, "a successfully cleaned temporary node must not be returned as active")
			require.Equal(t, []string{"canary-node"}, deleteIDs)
			require.Empty(t, nodes, "success, business failure, and cancellation must all leave no canary function behind")
			if tc.interruptAfterDeploy {
				require.True(t, sawCleanupWithLiveContext.Load(), "cleanup inventory after cancellation must use a detached request context")
			}
		})
	}
}

func TestCleanupCollectorSCFReleaseCanaryFleetNeverDeletesUnrelatedNodes(t *testing.T) {
	const prefix = "release-canary-0123456789abcdef0123456789abcdef"
	nodes := []adminclient.CloudNode{
		{NodeID: "owned", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher", Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
		{NodeID: "other-prefix", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher", Metadata: map[string]any{"function_name_prefix": "some-other-prefix", "index": 0}},
		{NodeID: "other-account", CloudAccountID: "account-b", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher", Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
		{NodeID: "other-region", CloudAccountID: "account-a", Region: "ap-guangzhou", Namespace: "canary-ns", NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher", Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
		{NodeID: "other-namespace", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "production", NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher", Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
		{NodeID: "other-trigger", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", TriggerType: "timer", BizType: "market_fetcher", Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
		{NodeID: "other-type", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "container", TriggerType: "invoke", BizType: "market_fetcher", Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
		{NodeID: "other-biz", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", TriggerType: "invoke", BizType: "other", Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
		{NodeID: "deleted", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher", IsDeleted: true, Metadata: map[string]any{"function_name_prefix": prefix, "index": 0}},
	}
	var deletedIDs []string
	var statusReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "items": nodes, "page": map[string]any{"has_more": false}})
		case "/api/admin/cloudnode/SubmitDeleteNodes":
			var request struct {
				NodeIDs []string `json:"node_ids"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			deletedIDs = append(deletedIDs, request.NodeIDs...)
			for index := range nodes {
				if slices.Contains(request.NodeIDs, nodes[index].NodeID) {
					nodes[index].IsDeleted = true
				}
			}
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"delete-job","operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":1}`))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			statusReads.Add(1)
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"delete-job","status":"NODE_BATCH_STATUS_SUCCESS"},"items":[]}`))
		default:
			t.Errorf("unexpected cleanup request: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := cleanupCollectorSCFReleaseCanaryFleet(ctx, adminclient.New(server.URL), collectorPublishOptions{
		CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke", FunctionNamePrefix: prefix,
	}, "", true, false)
	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown, "without the caller context, an accepted async delete cannot be reported as complete")
	require.Equal(t, []string{"owned"}, deletedIDs)
	require.Zero(t, statusReads.Load(), "do not detach the terminal wait after caller cancellation")
}

func TestCleanupWaitsForKnownCanaryDeploymentBatchBeforeInventory(t *testing.T) {
	var statusReads atomic.Int32
	var inventoryAfterTerminal atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeBatchChange":
			read := statusReads.Add(1)
			status := "NODE_BATCH_STATUS_RUNNING"
			if read > 1 {
				status = "NODE_BATCH_STATUS_SUCCESS"
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":"create-job","status":%q},"items":[]}`, status)
		case "/api/admin/cloudnode/GetNodeList":
			inventoryAfterTerminal.Store(statusReads.Load() > 1)
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
		default:
			t.Errorf("unexpected cleanup request: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	err := cleanupCollectorSCFReleaseCanaryFleet(context.Background(), adminclient.New(server.URL), collectorPublishOptions{
		CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke", NodeCount: 1,
		FunctionNamePrefix: "r0123456789abcdef0123456789abcdef",
	}, "create-job", false, false)
	require.NoError(t, err)
	require.GreaterOrEqual(t, statusReads.Load(), int32(2))
	require.True(t, inventoryAfterTerminal.Load(), "prefix inventory must wait until every known job is terminal")
}

func TestCleanupUnknownCanaryWaitsForPrefixThenFailsClosedAfterBestEffortDelete(t *testing.T) {
	const prefix = "release-canary-0123456789abcdef0123456789abcdef"
	var inventoryReads atomic.Int32
	var statusReads atomic.Int32
	var deletedIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			read := inventoryReads.Add(1)
			items := []adminclient.CloudNode(nil)
			if read == 2 {
				items = []adminclient.CloudNode{{
					NodeID: "late-canary", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns",
					NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher",
					Metadata: map[string]any{"function_name_prefix": prefix, "index": 0},
				}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "items": items, "page": map[string]any{"has_more": false}})
		case "/api/admin/cloudnode/SubmitDeleteNodes":
			var request struct {
				NodeIDs []string `json:"node_ids"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			deletedIDs = append(deletedIDs, request.NodeIDs...)
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"delete-job","operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":1}`))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			statusReads.Add(1)
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"delete-job","status":"NODE_BATCH_STATUS_SUCCESS"},"items":[]}`))
		default:
			t.Errorf("unexpected cleanup request: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	err := cleanupCollectorSCFReleaseCanaryFleet(context.Background(), adminclient.New(server.URL), collectorPublishOptions{
		CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke", NodeCount: 1,
		FunctionNamePrefix: prefix,
	}, "", false, true)
	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown, "a terminal delete cannot prove the unknown create worker will not create another late function")
	require.Equal(t, []string{"late-canary"}, deletedIDs)
	require.EqualValues(t, 1, statusReads.Load())
	require.GreaterOrEqual(t, inventoryReads.Load(), int32(3), "wait for the prefix to appear, then inventory again after best-effort deletion")
}

func TestCleanupCanceledKnownCanaryBatchFailsClosedBeforeInventory(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Errorf("canceled cleanup must not issue follow-up requests, got %s", r.URL.Path)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := cleanupCollectorSCFReleaseCanaryFleet(ctx, adminclient.New(server.URL), collectorPublishOptions{
		CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke", NodeCount: 1,
		FunctionNamePrefix: "r0123456789abcdef0123456789abcdef",
	}, "create-job", false, false)
	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown)
	require.Zero(t, calls.Load(), "do not inventory or delete while the known create job has not reached terminal status")
}

func TestCleanupCanceledKnownCanaryDeleteBatchFailsClosed(t *testing.T) {
	const prefix = "release-canary-0123456789abcdef0123456789abcdef"
	var statusReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info": map[string]any{"code": 0},
				"items": []adminclient.CloudNode{{
					NodeID: "owned", CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns",
					NodeType: "scf-event", TriggerType: "invoke", BizType: "market_fetcher",
					Metadata: map[string]any{"function_name_prefix": prefix, "index": 0},
				}},
				"page": map[string]any{"has_more": false},
			})
		case "/api/admin/cloudnode/SubmitDeleteNodes":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"delete-job","operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":1}`))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			statusReads.Add(1)
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"delete-job","status":"NODE_BATCH_STATUS_SUCCESS"},"items":[]}`))
		default:
			t.Errorf("unexpected cleanup request: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := cleanupCollectorSCFReleaseCanaryFleet(ctx, adminclient.New(server.URL), collectorPublishOptions{
		CloudAccountID: "account-a", Region: "ap-nanjing", Namespace: "canary-ns", NodeType: "scf-event", BizType: "market_fetcher", TriggerType: "invoke", NodeCount: 1,
		FunctionNamePrefix: prefix,
	}, "", true, false)
	require.ErrorIs(t, err, errCollectorBatchOutcomeUnknown)
	require.Zero(t, statusReads.Load(), "caller cancellation must not turn an unconfirmed asynchronous deletion into successful cleanup")
}

func TestCollectorSCFCanaryTaskSelectionRequiresOwnedDisabledSingleSeries(t *testing.T) {
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "crypto", CanaryTaskID: "task-canary", MarketID: "crypto", ProviderID: "binance", SourceID: "spot_http", InstrumentType: "spot"}
	entry := collectorCanaryTestEntry()

	selected, err := selectCollectorCanaryTask([]*collectorpb.TaskResultInventoryEntry{entry}, fetcher)
	require.NoError(t, err)
	require.Same(t, entry, selected)

	tests := []struct {
		name   string
		mutate func(*collectorpb.TaskResultInventoryEntry)
	}{
		{name: "enabled", mutate: func(entry *collectorpb.TaskResultInventoryEntry) { entry.Enabled = true }},
		{name: "not ownership verified", mutate: func(entry *collectorpb.TaskResultInventoryEntry) { entry.OwnershipVerified = false }},
		{name: "not single series", mutate: func(entry *collectorpb.TaskResultInventoryEntry) { entry.ExpectedCount = 2 }},
		{name: "candidate unavailable", mutate: func(entry *collectorpb.TaskResultInventoryEntry) { entry.CanaryCandidateAvailable = false }},
		{name: "other space", mutate: func(entry *collectorpb.TaskResultInventoryEntry) { entry.SpaceId = "other" }},
		{name: "wrong provider", mutate: func(entry *collectorpb.TaskResultInventoryEntry) { entry.Provider = "okx" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := collectorCanaryTestEntry()
			tc.mutate(candidate)
			_, err := selectCollectorCanaryTask([]*collectorpb.TaskResultInventoryEntry{candidate}, fetcher)
			require.Error(t, err)
		})
	}
}

func TestLoadCollectorCanaryInventoryUsesOneCompleteBoundedSnapshot(t *testing.T) {
	entries := make([]*collectorpb.TaskResultInventoryEntry, 101)
	for index := range entries {
		entry := collectorCanaryTestEntry()
		entry.TaskId = fmt.Sprintf("task-%03d", index)
		entries[index] = entry
	}
	entries[100].TaskId = "task-canary"
	var spaces []string
	var metadatas []codec.MetaData
	ctx, msg := codec.WithNewMessage(context.Background())
	defer codec.PutBackMessage(msg)
	msg.WithClientMetaData(codec.MetaData{"trace-id": []byte("keep-me")})
	loaded, err := loadCollectorCanaryInventory(ctx, collectorCanaryTestInventory{entries: entries, requestSpaces: &spaces, requestMetadata: &metadatas}, "crypto")
	require.NoError(t, err)
	require.Len(t, loaded, 101)
	require.Equal(t, []string{"crypto", "crypto"}, spaces)
	require.Len(t, metadatas, 2)
	for _, metadata := range metadatas {
		require.Equal(t, []byte("crypto"), metadata["space_id"])
		require.Equal(t, []byte("keep-me"), metadata["trace-id"])
	}
	selected, err := selectCollectorCanaryTask(loaded, &setupconfig.SCFFetcherSpace{SpaceID: "crypto", CanaryTaskID: "task-canary"})
	require.NoError(t, err)
	require.Equal(t, "task-canary", selected.GetTaskId())

	_, err = loadCollectorCanaryInventory(context.Background(), collectorCanaryTestInventory{entries: entries, snapshotIDs: []string{"snapshot-1", "snapshot-changed"}}, "crypto")
	require.ErrorContains(t, err, "does not belong to the first-page snapshot")
}

func TestPrepareCollectorSCFCanaryProofReservesOnlyUnusedOwnedPeriod(t *testing.T) {
	entry := collectorCanaryTestEntry()
	primary := &collectorCanaryTestPrimary{status: "not_found"}
	view := &collectorCanaryTestView{complete: true}
	now := time.Date(2026, 10, 3, 12, 0, 10, 0, time.UTC)
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "crypto", CanaryTaskID: entry.GetTaskId(), MarketID: "crypto", ProviderID: "binance", SourceID: "spot_http", InstrumentType: "spot"}
	access := collectorCanaryAccess{
		inventory: collectorCanaryTestInventory{entries: []*collectorpb.TaskResultInventoryEntry{entry}}, primary: primary, view: view,
		periodAuth: &commonpb.AuthInfo{AppId: "collector"}, primaryAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
		viewAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
	}
	proof, err := prepareCollectorSCFCanaryProofWithClock(context.Background(), fetcher, access, now, func() time.Time { return now })
	require.NoError(t, err)
	require.Equal(t, entry.GetTaskId(), proof.entry.GetTaskId())
	require.False(t, proof.period.IsZero())
	require.Equal(t, "1m", proof.entry.GetFrequency())
	require.EqualValues(t, 1, primary.ensureCalls.Load(), "preflight must atomically reserve the candidate before returning")
	require.NotEmpty(t, proof.reservationID)
	require.NotNil(t, primary.ensuredExpectation)
	require.Equal(t, proof.reservationID, primary.ensuredExpectation.GetReservationId())
	require.GreaterOrEqual(t, primary.ensuredExpectation.GetDeadlineAt(), time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC).Unix())
}

func TestPrepareCollectorSCFCanaryProofAcceptsAViewThatDoesNotCoverThePeriodYet(t *testing.T) {
	entry := collectorCanaryTestEntry()
	primary := &collectorCanaryTestPrimary{status: "not_found"}
	view := &collectorCanaryTestView{complete: false}
	now := time.Date(2026, 10, 3, 12, 0, 10, 0, time.UTC)
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "crypto", CanaryTaskID: entry.GetTaskId(), MarketID: "crypto", ProviderID: "binance", SourceID: "spot_http", InstrumentType: "spot"}
	access := collectorCanaryAccess{
		inventory: collectorCanaryTestInventory{entries: []*collectorpb.TaskResultInventoryEntry{entry}}, primary: primary, view: view,
		periodAuth: &commonpb.AuthInfo{AppId: "collector"}, primaryAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
		viewAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
	}
	proof, err := prepareCollectorSCFCanaryProofWithClock(context.Background(), fetcher, access, now, func() time.Time { return now })
	require.NoError(t, err, "a disabled canary task's empty View cannot cover recent periods; Primary absence is authoritative")
	require.EqualValues(t, 1, primary.ensureCalls.Load())
	require.Equal(t, proof.reservationID, primary.ensuredExpectation.GetReservationId())
}

func TestCollectorStockCNCanaryPeriodsRespectProviderHistoryWindow(t *testing.T) {
	entry := &collectorpb.TaskResultInventoryEntry{
		MarketId: "stockcn", CalendarId: "cn_stock", Timezone: "Asia/Shanghai",
		Sessions: []string{"09:30-11:30", "13:00-15:00"}, Provider: "tdx", Frequency: "1m",
	}
	now := time.Date(2026, 10, 3, 4, 0, 10, 0, time.UTC)

	periods, err := collectorSCFCanaryPeriods(entry, now, collectorCanaryCandidatePeriods)
	require.NoError(t, err, "TDX can serve the last pre-holiday period within its declared history window")
	require.NotEmpty(t, periods)
	require.Equal(t, time.Date(2026, 9, 30, 6, 59, 0, 0, time.UTC), periods[0])

	entry.Provider = "eastmoney"
	_, err = collectorSCFCanaryPeriods(entry, now, collectorCanaryCandidatePeriods)
	require.ErrorContains(t, err, "exceeds provider history window")
	require.ErrorContains(t, err, "eastmoney")
}

func TestCollectorStockCNCanaryPeriodsRejectUnknownProviderHistory(t *testing.T) {
	entry := &collectorpb.TaskResultInventoryEntry{
		MarketId: "stockcn", CalendarId: "cn_stock", Timezone: "Asia/Shanghai",
		Sessions: []string{"09:30-11:30", "13:00-15:00"}, Provider: "unknown", Frequency: "1m",
	}
	_, err := collectorSCFCanaryPeriods(entry, time.Date(2026, 10, 3, 4, 0, 10, 0, time.UTC), collectorCanaryCandidatePeriods)
	require.ErrorContains(t, err, "does not declare a supported history window")
}

func TestPrepareCollectorSCFCanaryProofRetriesWhenReservationRaces(t *testing.T) {
	entry := collectorCanaryTestEntry()
	primary := &collectorCanaryTestPrimary{status: "not_found", ensureConflictOnCall: 1}
	view := &collectorCanaryTestView{complete: true}
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "crypto", CanaryTaskID: entry.GetTaskId(), MarketID: "crypto", ProviderID: "binance", SourceID: "spot_http", InstrumentType: "spot"}
	access := collectorCanaryAccess{
		inventory: collectorCanaryTestInventory{entries: []*collectorpb.TaskResultInventoryEntry{entry}}, primary: primary, view: view,
		periodAuth: &commonpb.AuthInfo{AppId: "collector"}, primaryAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
		viewAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
	}
	now := time.Date(2026, 10, 3, 12, 0, 10, 0, time.UTC)
	candidates, err := collectorSCFCanaryPeriods(entry, now, collectorCanaryCandidatePeriods)
	require.NoError(t, err)
	proof, err := prepareCollectorSCFCanaryProofWithClock(context.Background(), fetcher, access, now, func() time.Time { return now })
	require.NoError(t, err)
	require.EqualValues(t, 2, primary.ensureCalls.Load())
	require.NotEqual(t, candidates[0], proof.period, "a period that lost the atomic reservation race cannot be shared")
	require.Equal(t, proof.reservationID, primary.ensuredExpectation.GetReservationId())
}

func TestCollectorSCFCanaryEventBindsInventoryTaskPeriodAndView(t *testing.T) {
	proof := &collectorSCFCanaryProof{entry: collectorCanaryTestEntry(), period: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), interval: time.Minute}
	proof.entry.OutputFields = []string{"close", "volume"}
	proof.entry.SeriesHash = "series-hash"
	opts := collectorPublishOptions{SpaceID: "crypto", Region: "ap-singapore", StorageRPCGatewayTarget: "ip://storage:11003"}
	event := collectorSCFCanaryEventForProof(opts, "canary-node", "batch-123", proof)
	data, ok := event["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, data["require_period_commit"])
	require.NotContains(t, data, "task_id", "the market_fetch request rejects unknown fields; task ownership travels in targets")
	require.Equal(t, proof.entry.GetDatasetId(), data["dataset_id"])
	require.Equal(t, proof.entry.GetFrequency(), data["frequency"])

	items, ok := data["items"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	require.Equal(t, proof.entry.GetSubjectId(), items[0]["subject_id"])
	require.Equal(t, proof.entry.GetProviderSymbol(), items[0]["symbol"])
	require.Equal(t, proof.entry.GetSeriesIndex(), items[0]["series_index"])
	require.Equal(t, proof.entry.GetSeriesHash(), items[0]["series_hash"])
	require.Equal(t, proof.reservationID, items[0]["period_reservation_id"])
	require.Equal(t, proof.period.Format(time.RFC3339Nano), items[0]["target_data_time"])
	require.Equal(t, true, items[0]["require_period_commit"])

	targets, ok := data["targets"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, targets, 1)
	require.Equal(t, proof.entry.GetTaskId(), targets[0]["task_id"])
	require.Equal(t, proof.entry.GetDatasetId(), targets[0]["dataset_id"])
	require.Equal(t, proof.entry.GetViewId(), targets[0]["view_id"])
	require.Equal(t, proof.entry.GetSeriesHash(), targets[0]["series_hash"])
	require.Equal(t, proof.period.Format(time.RFC3339Nano), targets[0]["target_data_time"])
}

func TestCollectorSCFCanaryProofRequiresCompletePeriodAndExactPrimaryAndViewRows(t *testing.T) {
	proof, primary, view := collectorCanaryTestProof()
	primary.status = "complete"
	primary.primaryRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.viewRows = []*storagepb.TimeSeriesRow{collectorCanaryTestRow(proof)}
	view.complete = true
	require.NoError(t, proof.verify(context.Background()))

	view.viewRows[0].Key.DataTime = proof.period.Add(time.Minute).Format(time.RFC3339Nano)
	err := proof.verify(context.Background())
	require.ErrorContains(t, err, "exact task-owned View row")
}

func TestCollectorStockCNActivationRequiresPackageIdentityBeforeManifestOrTimerMutation(t *testing.T) {
	_, err := activateStockCNCollection(context.Background(), collectorStockCNActivateOptions{
		ControlURL: "https://control.invalid", File: "/missing/moox.toml", Version: "candidate",
	})
	require.ErrorContains(t, err, "--region-package-id")
}

func canaryResponseClient(t *testing.T, result map[string]any) *adminclient.Client {
	t.Helper()
	raw := mustCanaryJSON(t, map[string]any{
		"ret_info": map[string]any{"code": 0},
		"scf":      map[string]any{"code": 0, "result": result},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/cloudnode/InvokeFunction", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(raw)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	return adminclient.New(server.URL)
}

func mustCanaryJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

type collectorCanaryTestInventory struct {
	entries         []*collectorpb.TaskResultInventoryEntry
	snapshotIDs     []string
	requestSpaces   *[]string
	requestMetadata *[]codec.MetaData
}

func (f collectorCanaryTestInventory) GetTaskResultInventory(ctx context.Context, req *collectorpb.GetTaskResultInventoryReq, _ ...client.Option) (*collectorpb.GetTaskResultInventoryRsp, error) {
	if f.requestSpaces != nil {
		*f.requestSpaces = append(*f.requestSpaces, req.GetSpaceId())
	}
	if f.requestMetadata != nil {
		*f.requestMetadata = append(*f.requestMetadata, codec.Message(ctx).ClientMetaData().Clone())
	}
	page, size := req.GetPage().GetPage(), req.GetPage().GetSize()
	start := int(page-1) * int(size)
	end := start + int(size)
	if end > len(f.entries) {
		end = len(f.entries)
	}
	entries := append([]*collectorpb.TaskResultInventoryEntry(nil), f.entries[start:end]...)
	snapshotID := "snapshot-1"
	if len(f.snapshotIDs) > 0 && int(page) <= len(f.snapshotIDs) {
		snapshotID = f.snapshotIDs[page-1]
	}
	return &collectorpb.GetTaskResultInventoryRsp{
		RetInfo: &commonpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}, Entries: entries,
		SnapshotId: snapshotID, ObservedAt: "2026-10-03T00:00:00Z",
		Page: &commonpb.PageResult{Page: page, Size: size, Total: uint32(len(f.entries)), HasMore: end < len(f.entries)},
	}, nil
}

type collectorCanaryTestPrimary struct {
	status               string
	statusErr            error
	primaryRows          []*storagepb.TimeSeriesRow
	readErr              error
	ensureStatus         string
	ensureRetInfo        *commonpb.RetInfo
	ensureErr            error
	ensureCalls          atomic.Int32
	ensureConflictOnCall int32
	ensuredExpectation   *storagepb.DatasetPeriodExpectation
}

func (f *collectorCanaryTestPrimary) EnsureDatasetPeriod(_ context.Context, req *storagepb.PrimaryEnsureDatasetPeriodReq, _ ...client.Option) (*storagepb.PrimaryEnsureDatasetPeriodRsp, error) {
	call := f.ensureCalls.Add(1)
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	if call == f.ensureConflictOnCall {
		return &storagepb.PrimaryEnsureDatasetPeriodRsp{RetInfo: &commonpb.RetInfo{Code: storagepb.ErrorCode_CONFLICT, Msg: "another publisher reserved the period"}}, nil
	}
	if req != nil && req.GetExpectation() != nil {
		f.ensuredExpectation = req.GetExpectation()
	}
	retInfo := f.ensureRetInfo
	if retInfo == nil {
		retInfo = &commonpb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
	}
	status := f.ensureStatus
	if status == "" {
		status = "waiting"
	}
	deadlineAt := int64(0)
	if req != nil && req.GetExpectation() != nil {
		deadlineAt = req.GetExpectation().GetDeadlineAt()
	}
	return &storagepb.PrimaryEnsureDatasetPeriodRsp{RetInfo: retInfo, Status: status, DeadlineAt: deadlineAt}, nil
}

func (f *collectorCanaryTestPrimary) GetDatasetPeriodStatus(_ context.Context, _ *storagepb.PrimaryGetDatasetPeriodStatusReq, _ ...client.Option) (*storagepb.PrimaryGetDatasetPeriodStatusRsp, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return &storagepb.PrimaryGetDatasetPeriodStatusRsp{
		RetInfo: &commonpb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Status: f.status,
		SeriesHash: "series-hash", ExpectedCount: 1,
	}, nil
}

func (f *collectorCanaryTestPrimary) ReadTimeSeriesRows(_ context.Context, _ *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Rows: f.primaryRows}, nil
}

type collectorCanaryTestView struct {
	viewRows []*storagepb.TimeSeriesRow
	complete bool
	queryErr error
}

func (f *collectorCanaryTestView) QueryTimeSeriesRows(_ context.Context, _ *storagepb.QueryTimeSeriesRowsReq, _ ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return &storagepb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Rows: f.viewRows, Complete: f.complete}, nil
}

func collectorCanaryTestEntry() *collectorpb.TaskResultInventoryEntry {
	return &collectorpb.TaskResultInventoryEntry{
		SpaceId: "crypto", TaskId: "task-canary", DatasetId: "dataset_binance_kline_1m", ViewId: "view-canary",
		Frequency: "1m", MarketId: "crypto", Enabled: false, OwnershipVerified: true, CanaryCandidateAvailable: true,
		SubjectId: "BTC-USDT", ProviderSymbol: "BTCUSDT", Provider: "binance", SourceId: "spot_http", MarketType: "spot",
		SeriesTag: "venue:binance|market:spot|source:spot_http", SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1,
		OutputFields: []string{"close"},
	}
}

func collectorCanaryTestProof() (*collectorSCFCanaryProof, *collectorCanaryTestPrimary, *collectorCanaryTestView) {
	entry := collectorCanaryTestEntry()
	primary := &collectorCanaryTestPrimary{status: "complete"}
	view := &collectorCanaryTestView{complete: true}
	proof := &collectorSCFCanaryProof{
		entry: entry, period: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), interval: time.Minute,
		access: collectorCanaryAccess{
			inventory: collectorCanaryTestInventory{entries: []*collectorpb.TaskResultInventoryEntry{entry}}, primary: primary, view: view,
			periodAuth: &commonpb.AuthInfo{AppId: "collector"}, primaryAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
			viewAuth: &commonpb.AuthInfo{AppId: "scf-market-canary"},
		},
	}
	return proof, primary, view
}

func collectorCanaryTestRow(proof *collectorSCFCanaryProof) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{Key: proof.timeSeriesKey(), Fields: []*storagepb.FieldValue{{
		FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 100}},
	}}}
}

func TestCollectorHTTPInventoryReaderUsesTheServiceGatewayRoute(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"snapshot_id":"snap-1","entries":[{"task_id":"canary","enabled":false,"expected_count":1}],"page":{"page":1,"size":100,"total":1}}`))
	}))
	defer server.Close()

	reader := collectorHTTPInventoryReader{control: adminclient.New(server.URL)}
	rsp, err := reader.GetTaskResultInventory(context.Background(), &collectorpb.GetTaskResultInventoryReq{
		SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 100},
	})
	require.NoError(t, err)
	assert.Equal(t, "/api/admin/collectmgr/GetTaskResultInventory", gotPath)
	assert.Equal(t, "crypto", gotBody["space_id"])
	assert.Equal(t, "snap-1", rsp.GetSnapshotId())
	require.Len(t, rsp.GetEntries(), 1)
	assert.Equal(t, "canary", rsp.GetEntries()[0].GetTaskId())
	assert.EqualValues(t, 1, rsp.GetEntries()[0].GetExpectedCount())
	assert.EqualValues(t, 1, rsp.GetPage().GetTotal())
}
