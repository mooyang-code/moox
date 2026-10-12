package command

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"debug/elf"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/errs"
)

func setCollectorCLSTestCredentials(t *testing.T) {
	t.Helper()
	if os.Getenv("MOOX_CLS_SECRET_ID") == "" {
		t.Setenv("MOOX_CLS_SECRET_ID", "test-cls-id")
	}
	if os.Getenv("MOOX_CLS_SECRET_KEY") == "" {
		t.Setenv("MOOX_CLS_SECRET_KEY", "test-cls-key")
	}
}

func setCollectorFleetRuntimeTestEnvironment(t *testing.T) string {
	t.Helper()
	setCollectorCLSTestCredentials(t)
	t.Setenv("MOOX_CLS_ENDPOINT", "ap-guangzhou.cls.tencentyun.com")
	t.Setenv("MOOX_CLS_TOPIC_ID", "topic-test")
	t.Setenv("MOOX_ACCESS_ADDRESS", "storage.example:11004")
	t.Setenv("MOOX_ACCESS_ID", "access@storage")
	t.Setenv("MOOX_CALLER_KEY_ID", "assigned-scf-key-17")
	t.Setenv("MOOX_CALLER_KEY", strings.Repeat("a", 64))

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	gatewayCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	dir := t.TempDir()
	gatewayCAFile := filepath.Join(dir, "gateway-ca.pem")
	require.NoError(t, os.WriteFile(gatewayCAFile, gatewayCA, 0o600))
	t.Setenv("MOOX_GATEWAY_CA_FILE", gatewayCAFile)
	t.Setenv("MOOX_SERVICE_GATEWAY_CA_FILE", gatewayCAFile)

	eventBusCAFile := filepath.Join(dir, "eventbus-ca.pem")
	require.NoError(t, os.WriteFile(eventBusCAFile, mustTestEventBusCAPEM(t), 0o600))
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "test-primary-secret")
	credentialFile := filepath.Join(dir, "eventbus.yaml")
	require.NoError(t, os.WriteFile(credentialFile, []byte(
		"version: 1\nurls: [tls://203.0.113.10:4222]\nusername: worker\npassword: worker-secret\nca_file: eventbus-ca.pem\n",
	), 0o600))
	return credentialFile
}

const collectorTestStorageAppKeysJSON = `{"moox-collector":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

func mustTestEventBusCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "moox-test-eventbus-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCollectorStoragePrimaryAuthSecretUsesNormalizedControlFile(t *testing.T) {
	secret, err := collectorStoragePrimaryAuthSecret([]byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-secret\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"))
	require.NoError(t, err)
	assert.Equal(t, "primary-secret", secret)
	_, err = collectorStoragePrimaryAuthSecret([]byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=old\nMOOX_STORAGE_PRIMARY_AUTH_SECRET=new\nMOOX_STORAGE_VIEW_AUTH_SECRET=view\n"))
	assert.Error(t, err)
}

func TestEgressProbeResponseDataAcceptsStructuredAndRawResponses(t *testing.T) {
	structured, ok := egressProbeResponseData(map[string]any{
		"data": map[string]any{"details": map[string]any{"public_ip": "198.51.100.1"}},
	})
	require.True(t, ok)
	assert.Equal(t, "198.51.100.1", structured["details"].(map[string]any)["public_ip"])

	raw, ok := egressProbeResponseData(map[string]any{
		"raw": `{"success":true,"data":{"details":{"public_ip":"198.51.100.2"}}}`,
	})
	require.True(t, ok)
	assert.Equal(t, "198.51.100.2", raw["details"].(map[string]any)["public_ip"])

	_, ok = egressProbeResponseData(map[string]any{"raw": "not-json"})
	assert.False(t, ok)
}

func TestSelectCollectorProbeNodesKeepsCryptoSemanticsAndStockTimersOnly(t *testing.T) {
	nodes := []adminclient.CloudNode{
		{NodeID: "timer-a", PackageID: "pkg", TriggerType: "timer", Metadata: map[string]any{"deployment_ready": true}},
		{NodeID: "invoke-a", PackageID: "pkg", TriggerType: "invoke", Metadata: map[string]any{"deployment_ready": true}},
	}

	assert.Len(t, selectCollectorProbeNodes(nodes, false), 2)
	stockNodes := selectCollectorProbeNodes(nodes, true)
	require.Len(t, stockNodes, 1)
	assert.Equal(t, "timer-a", stockNodes[0].NodeID)
}

func TestCollectorEgressProbeReportKeepsOnlyDiagnosticCounts(t *testing.T) {
	report := &collectorProbeReport{
		Results: []collectorProbeResult{
			{NodeID: "node-a", OutboundIP: "198.51.100.1"},
			{NodeID: "node-b", OutboundIP: "198.51.100.1"},
		},
		ExpectedCount: 170,
		EligibleCount: 2,
		DistinctCount: 1,
	}
	assert.Equal(t, 170, report.ExpectedCount)
	assert.Equal(t, 2, report.EligibleCount)
	assert.Equal(t, 1, report.DistinctCount)
}

func TestDefaultCryptoCollectorTasksSelectOnlyKlineFields(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	var bundle struct {
		Tasks []struct {
			SpaceID       string `yaml:"space_id"`
			TaskName      string `yaml:"task_name"`
			DataType      string `yaml:"data_type"`
			CollectParams struct {
				Frequency    string   `yaml:"frequency"`
				OutputFields []string `yaml:"output_fields"`
			} `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(content, &bundle))
	wantFields := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	wantTasks := map[string]bool{
		"Binance 现货 K 线 1m": false,
		"Binance 合约 K 线 1m": false,
		"Binance 现货 K 线 1h": false,
		"Binance 合约 K 线 1h": false,
	}
	for _, task := range bundle.Tasks {
		if task.SpaceID != "crypto" || task.DataType != "kline" {
			continue
		}
		if _, ok := wantTasks[task.TaskName]; !ok {
			continue
		}
		wantTasks[task.TaskName] = true
		assert.Equal(t, wantFields, task.CollectParams.OutputFields, task.TaskName)
	}
	for name, found := range wantTasks {
		assert.True(t, found, "missing default task %s", name)
	}
}

func TestDefaultStockCNCollectorTasksRequireExplicitActivation(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	var bundle struct {
		Tasks []struct {
			SpaceID       string         `yaml:"space_id"`
			DataType      string         `yaml:"data_type"`
			TagIDs        []string       `yaml:"tag_ids"`
			Enabled       bool           `yaml:"enabled"`
			CollectParams map[string]any `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(content, &bundle))
	found := false
	for _, task := range bundle.Tasks {
		if task.SpaceID != "stockcn" || task.DataType != "kline" || !slices.Contains(task.TagIDs, "cn_a_share") {
			continue
		}
		if fmt.Sprint(task.CollectParams["frequency"]) != "1m" {
			continue
		}
		found = true
		assert.False(t, task.Enabled)
	}
	assert.True(t, found, "default stockcn 1m task should exist and remain disabled until explicit activation")
}

type collectorCLSAPI struct{}

func (collectorCLSAPI) GetService(context.Context) (bool, error)    { return true, nil }
func (collectorCLSAPI) OpenService(context.Context) (string, error) { return "", nil }
func (collectorCLSAPI) FindLogset(context.Context, string) (tencent.CLSLogset, bool, error) {
	return tencent.CLSLogset{ID: "logset-from-api", Name: "moox"}, true, nil
}
func (collectorCLSAPI) CreateLogset(context.Context, string) (tencent.CLSLogset, string, error) {
	panic("unexpected CreateLogset")
}
func (collectorCLSAPI) FindTopic(context.Context, string, string) (tencent.CLSTopic, bool, error) {
	return tencent.CLSTopic{ID: "topic-from-api", LogsetID: "logset-from-api", Name: "moox-application", IndexEnabled: true}, true, nil
}
func (collectorCLSAPI) CreateTopic(context.Context, tencent.CLSCreateTopicOptions) (tencent.CLSTopic, string, error) {
	panic("unexpected CreateTopic")
}
func (collectorCLSAPI) EnsureIndexFields(context.Context, string, []tencent.CLSIndexField) (bool, string, error) {
	return false, "", nil
}

func (collectorCLSAPI) CreateIndex(context.Context, string) (string, error) {
	panic("unexpected CreateIndex")
}

type fakeCollectorFleetAPI struct {
	nodes       []adminclient.CloudNode
	createCalls [][]adminclient.NodeCreateItem
	deployCalls [][]adminclient.NodeDeployItem
	createErr   error
}

func (f *fakeCollectorFleetAPI) ListCloudNodes(context.Context, adminclient.CloudNodeListFilter) ([]adminclient.CloudNode, error) {
	return append([]adminclient.CloudNode(nil), f.nodes...), nil
}

func (f *fakeCollectorFleetAPI) SubmitCreateNodes(_ context.Context, items []adminclient.NodeCreateItem) (*adminclient.SubmitNodeBatchResponse, error) {
	f.createCalls = append(f.createCalls, append([]adminclient.NodeCreateItem(nil), items...))
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &adminclient.SubmitNodeBatchResponse{
		JobID:      fmt.Sprintf("create-%d", len(f.createCalls)),
		Operation:  "NODE_BATCH_OPERATION_CREATE_NODES",
		TotalCount: len(items),
	}, nil
}

func (f *fakeCollectorFleetAPI) SubmitDeployNodes(_ context.Context, items []adminclient.NodeDeployItem) (*adminclient.SubmitNodeBatchResponse, error) {
	f.deployCalls = append(f.deployCalls, append([]adminclient.NodeDeployItem(nil), items...))
	return &adminclient.SubmitNodeBatchResponse{
		JobID:      fmt.Sprintf("deploy-%d", len(f.deployCalls)),
		Operation:  "NODE_BATCH_OPERATION_DEPLOY_NODES",
		TotalCount: len(items),
	}, nil
}

func TestDeleteCollectorFunctionsFiltersNamespace(t *testing.T) {
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/admin/cloudnode/GetNodeList", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items": []adminclient.CloudNode{
				{NodeID: "legacy-default", Namespace: "default", NodeType: "scf-event", BizType: "market_fetcher"},
				{NodeID: "new-crypto", Namespace: "moox-crypto", NodeType: "scf-event", BizType: "market_fetcher"},
				{NodeID: "deleted-default", Namespace: "default", IsDeleted: true, NodeType: "scf-event", BizType: "market_fetcher"},
			},
		})
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL,
		SpaceID:    "crypto",
		Namespace:  "default",
		DryRun:     true,
	})
	require.NoError(t, err)
	assert.Equal(t, "default", summary.Namespace)
	assert.Equal(t, 1, summary.TotalCount)
	assert.Equal(t, []string{"legacy-default"}, summary.NodeIDs)
}

func TestDeleteCollectorFunctionsRequiresWaitForFencedAsyncBatch(t *testing.T) {
	var calls []string
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"node_id":"fetcher-1","node_type":"scf-event","biz_type":"market_fetcher","namespace":"default"}],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"1","expires_at":"2026-10-03T12:02:00Z"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	_, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "crypto", Confirm: true, Wait: false,
	})
	require.ErrorContains(t, err, "requires --wait")
	assert.Empty(t, calls, "the command must reject non-waiting deletion before reading targets or acquiring a lease")
}

func TestDeleteCollectorFunctionsAcquiresLeaseBeforeTargetSnapshot(t *testing.T) {
	var calls []string
	leaseHeld := false
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			leaseHeld = true
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudnode/GetNodeList":
			assert.True(t, leaseHeld, "target snapshot must be read inside the publish lease")
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			leaseHeld = false
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "crypto", Confirm: true, Wait: true,
	})
	require.NoError(t, err)
	require.Equal(t, "nothing_to_delete", summary.Status)
	assert.Equal(t, []string{
		"/api/admin/publishlease/AcquireCollectorPublishLease",
		"/api/admin/cloudnode/GetNodeList",
		"/api/admin/publishlease/ReleaseCollectorPublishLease",
	}, calls, "even an empty snapshot is protected and released under the lease")
}

func TestDeleteCollectorFunctionsHoldsLeaseUntilNodeBatchCompletes(t *testing.T) {
	statusReads := 0
	var sawFence bool
	var released bool
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"node_id":"fetcher-1","node_type":"scf-event","biz_type":"market_fetcher","namespace":"default"}],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudnode/SubmitDeleteNodes":
			var request struct {
				LeaseID string `json:"collector_publish_lease_id"`
				Token   int64  `json:"collector_publish_fencing_token"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			sawFence = request.LeaseID == "lease-1" && request.Token == 17
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"delete-1","operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":1}`))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			statusReads++
			status := "NODE_BATCH_STATUS_RUNNING"
			if statusReads > 1 {
				status = "NODE_BATCH_STATUS_SUCCESS"
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":"delete-1","operation":"NODE_BATCH_OPERATION_DELETE_NODES","status":%q,"success_count":1,"total_count":1},"items":[]}`, status)
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			released = true
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "crypto", Confirm: true, Wait: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "NODE_BATCH_STATUS_SUCCESS", summary.Status)
	assert.True(t, sawFence)
	assert.True(t, released)
	assert.GreaterOrEqual(t, statusReads, 2)
}

func TestDeleteCollectorFunctionsChunksFleetAndWaitsEveryAcceptedBatch(t *testing.T) {
	nodes := make([]adminclient.CloudNode, 170)
	for index := range nodes {
		nodes[index] = adminclient.CloudNode{NodeID: fmt.Sprintf("fetcher-%03d", index), Namespace: "default", NodeType: "scf-event", BizType: "market_fetcher"}
	}
	var submittedSizes []int
	var statusReads = map[string]int{}
	var released bool
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudnode/GetNodeList":
			_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "items": nodes, "page": map[string]any{"has_more": false}})
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"stockcn","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudnode/SubmitDeleteNodes":
			var request struct {
				NodeIDs []string `json:"node_ids"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			submittedSizes = append(submittedSizes, len(request.NodeIDs))
			jobID := fmt.Sprintf("delete-%d", len(submittedSizes))
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job_id":%q,"operation":"NODE_BATCH_OPERATION_DELETE_NODES","total_count":%d}`, jobID, len(request.NodeIDs))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			var request struct {
				JobID string `json:"job_id"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			statusReads[request.JobID]++
			total := map[string]int{"delete-1": 100, "delete-2": 70}[request.JobID]
			status := "NODE_BATCH_STATUS_SUCCESS"
			failed, success := 0, total
			if request.JobID == "delete-1" {
				status, failed, success = "NODE_BATCH_STATUS_FAILED", 100, 0
			} else if statusReads[request.JobID] == 1 {
				status = "NODE_BATCH_STATUS_RUNNING"
				success = 0
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":%q,"operation":"NODE_BATCH_OPERATION_DELETE_NODES","status":%q,"success_count":%d,"failed_count":%d,"total_count":%d},"items":[]}`, request.JobID, status, success, failed, total)
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			released = true
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		ControlURL: server.URL, SpaceID: "stockcn", Confirm: true, Wait: true,
	})
	require.ErrorContains(t, err, "delete-1 finished with status NODE_BATCH_STATUS_FAILED")
	assert.Equal(t, []int{100, 70}, submittedSizes)
	assert.Equal(t, "delete-1,delete-2", summary.JobID)
	assert.Equal(t, 100, summary.FailedCount)
	assert.Equal(t, 1, statusReads["delete-1"])
	assert.GreaterOrEqual(t, statusReads["delete-2"], 2, "the second job must reach terminal status after the first fails")
	assert.True(t, released, "release only after every accepted batch reaches terminal status")
}

func TestCollectorPublishUnknownSubmissionErrorsAreAmbiguous(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "native network loss", err: errs.NewFrameError(errs.RetClientNetErr, "lost response"), want: true},
		{name: "native server timeout", err: errs.NewFrameError(errs.RetServerTimeout, "timed out"), want: true},
		{name: "native authentication", err: errs.NewFrameError(errs.RetServerAuthFail, "denied"), want: false},
		{name: "gateway 503", err: fmt.Errorf("control returned HTTP 503 Service Unavailable"), want: true},
		{name: "service 500", err: fmt.Errorf("SubmitCreateNodes: code 500: internal error"), want: true},
		{name: "missing job id", err: fmt.Errorf("SubmitCreateNodes: empty job_id"), want: true},
		{name: "malformed operation", err: fmt.Errorf("SubmitCreateNodes: operation: unknown enum"), want: true},
		{name: "authorization rejection", err: fmt.Errorf("control returned HTTP 403 Forbidden"), want: false},
		{name: "business rejection", err: fmt.Errorf("SubmitCreateNodes: code 409: lease stale"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, isAmbiguousCollectorPublishOutcome(test.err))
		})
	}
}

type stockCNHandoffTestState struct {
	acquireCount                          int
	listCount                             int
	releasedLeaseID                       string
	firstLeaseReleasedBeforeSecondAcquire bool
}

func newStockCNHandoffTestServer(t *testing.T, assignmentHashes ...string) (*httptest.Server, *stockCNHandoffTestState) {
	t.Helper()
	state := &stockCNHandoffTestState{}
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			state.acquireCount++
			if state.acquireCount == 2 {
				state.firstLeaseReleasedBeforeSecondAcquire = state.releasedLeaseID == "lease-1"
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"space_id":"stockcn","lease_id":"lease-%d","fencing_token":"%d","expires_at":"2026-10-03T20:02:00Z"}`, state.acquireCount, state.acquireCount)
		case "/api/admin/publishlease/ReleaseCollectorPublishLease":
			var request struct {
				LeaseID string `json:"lease_id"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			state.releasedLeaseID = request.LeaseID
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		case "/api/admin/cloudnode/GetNodeList":
			state.listCount++
			index := min(state.listCount-1, len(assignmentHashes)-1)
			assignmentHash := assignmentHashes[index]
			packageID := "package-7"
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"items":[{"node_id":"timer-1","package_id":%q,"region":"ap-guangzhou","namespace":"default","node_type":"scf-event","biz_type":"market_fetcher","trigger_type":"timer","function_name":"stock-ap-guangzhou-0","metadata":{"index":0,"assignment_hash":%q,"binding_hash":"binding-a","assignment_count":12,"runtime_config_reconciled_at":"2026-10-03T00:00:00Z"}}],"page":{"has_more":false}}`, packageID, assignmentHash)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	return server, state
}

func writeMinimalSCFZip(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
	out, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(out)
	if len(files) == 0 {
		files = map[string]string{"main": "binary"}
	}
	for name, content := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		if name == "main" {
			header.SetMode(0o755)
			if content == "binary" {
				content = string(testfixture.LinuxExecutable(elf.EM_X86_64))
			}
		}
		writer, err := zw.CreateHeader(header)
		require.NoError(t, err)
		_, err = writer.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, out.Close())
}

func TestResolveCollectorRootMissing(t *testing.T) {
	_, err := resolveCollectorRoot("/nonexistent/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collector root not found")
}
