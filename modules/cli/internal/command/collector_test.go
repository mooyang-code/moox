package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	metricsreport "github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/errs"
)

func TestCollectorEgressReportPreservesHostAndProcessIdentities(t *testing.T) {
	for _, tc := range []struct{ name, host, process, wantHost, wantProcess string }{
		{"defaults", "", "", "moox-cli", "moox-cli"},
		{"distinct identities", "operator-host", "probe-process", "operator-host", "probe-process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MOOX_NODE_ID", tc.host)
			t.Setenv("MOOX_INSTANCE_ID", tc.process)
			cfg := collectorEgressReportConfig()
			assert.Equal(t, tc.wantHost, cfg.NodeID)
			assert.Equal(t, tc.wantProcess, cfg.InstanceID)
			_, err := metricsreport.NewHandlerWithRegistry(cfg, prometheus.NewRegistry())
			require.NoError(t, err)
		})
	}
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
	nodes := []adminclient.FunctionInstance{
		{InstanceID: "timer-a", PackageID: "pkg", TriggerType: "timer", Metadata: map[string]any{"deployment_ready": true}},
		{InstanceID: "invoke-a", PackageID: "pkg", TriggerType: "invoke", Metadata: map[string]any{"deployment_ready": true}},
	}

	assert.Len(t, selectCollectorProbeInstances(nodes, false), 2)
	stockInstances := selectCollectorProbeInstances(nodes, true)
	require.Len(t, stockInstances, 1)
	assert.Equal(t, "timer-a", stockInstances[0].InstanceID)
}

func TestCollectorEgressProbeReportKeepsOnlyDiagnosticCounts(t *testing.T) {
	report := &collectorProbeReport{
		Results: []collectorProbeResult{
			{InstanceID: "node-a", OutboundIP: "198.51.100.1"},
			{InstanceID: "node-b", OutboundIP: "198.51.100.1"},
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

func TestDeleteCollectorFunctionsFiltersNamespace(t *testing.T) {
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/admin/cloudfunction/ListFunctionInstances", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items": []adminclient.FunctionInstance{
				{InstanceID: "legacy-default", Namespace: "default", FunctionType: "scf-event", WorkloadType: "market_fetcher"},
				{InstanceID: "new-crypto", Namespace: "moox-crypto", FunctionType: "scf-event", WorkloadType: "market_fetcher"},
				{InstanceID: "deleted-default", Namespace: "default", IsDeleted: true, FunctionType: "scf-event", WorkloadType: "market_fetcher"},
			},
		})
	}))
	defer server.Close()

	summary, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		SpaceID:   "crypto",
		Namespace: "default",
		DryRun:    true,
	})
	require.NoError(t, err)
	assert.Equal(t, "default", summary.Namespace)
	assert.Equal(t, 1, summary.TotalCount)
	assert.Equal(t, []string{"legacy-default"}, summary.InstanceIDs)
}

func TestDeleteCollectorFunctionsRequiresWaitForFencedAsyncBatch(t *testing.T) {
	var calls []string
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudfunction/ListFunctionInstances":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"instance_id":"fetcher-1","function_type":"scf-event","workload_type":"market_fetcher","namespace":"default"}],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"1","expires_at":"2026-10-03T12:02:00Z"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	_, err := deleteCollectorFunctions(context.Background(), collectorDeleteOptions{
		SpaceID: "crypto", Confirm: true, Wait: false,
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
		case "/api/admin/cloudfunction/ListFunctionInstances":
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
		SpaceID: "crypto", Confirm: true, Wait: true,
	})
	require.NoError(t, err)
	require.Equal(t, "nothing_to_delete", summary.Status)
	assert.Equal(t, []string{
		"/api/admin/publishlease/AcquireCollectorPublishLease",
		"/api/admin/cloudfunction/ListFunctionInstances",
		"/api/admin/publishlease/ReleaseCollectorPublishLease",
	}, calls, "even an empty snapshot is protected and released under the lease")
}

func TestDeleteCollectorFunctionsHoldsLeaseUntilFunctionJobCompletes(t *testing.T) {
	statusReads := 0
	var sawFence bool
	var released bool
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudfunction/ListFunctionInstances":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[{"instance_id":"fetcher-1","function_type":"scf-event","workload_type":"market_fetcher","namespace":"default"}],"page":{"has_more":false}}`))
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudfunction/SubmitDeleteFunctionInstances":
			var request struct {
				LeaseID string `json:"collector_publish_lease_id"`
				Token   int64  `json:"collector_publish_fencing_token"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			sawFence = request.LeaseID == "lease-1" && request.Token == 17
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"delete-1","operation":"FUNCTION_JOB_OPERATION_DELETE_INSTANCES","total_count":1}`))
		case "/api/admin/cloudfunction/GetFunctionJob":
			statusReads++
			status := "FUNCTION_JOB_STATUS_RUNNING"
			if statusReads > 1 {
				status = "FUNCTION_JOB_STATUS_SUCCESS"
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":"delete-1","operation":"FUNCTION_JOB_OPERATION_DELETE_INSTANCES","status":%q,"success_count":1,"total_count":1},"items":[]}`, status)
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
		SpaceID: "crypto", Confirm: true, Wait: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "FUNCTION_JOB_STATUS_SUCCESS", summary.Status)
	assert.True(t, sawFence)
	assert.True(t, released)
	assert.GreaterOrEqual(t, statusReads, 2)
}

func TestDeleteCollectorFunctionsChunksFleetAndWaitsEveryAcceptedBatch(t *testing.T) {
	nodes := make([]adminclient.FunctionInstance, 170)
	for index := range nodes {
		nodes[index] = adminclient.FunctionInstance{InstanceID: fmt.Sprintf("fetcher-%03d", index), Namespace: "default", FunctionType: "scf-event", WorkloadType: "market_fetcher"}
	}
	var submittedSizes []int
	var statusReads = map[string]int{}
	var released bool
	server := newControlFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/admin/cloudfunction/ListFunctionInstances":
			_ = json.NewEncoder(w).Encode(map[string]any{"ret_info": map[string]any{"code": 0}, "items": nodes, "page": map[string]any{"has_more": false}})
		case "/api/admin/publishlease/AcquireCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"stockcn","lease_id":"lease-1","fencing_token":"17","expires_at":"2026-10-03T20:02:00Z"}`))
		case "/api/admin/cloudfunction/SubmitDeleteFunctionInstances":
			var request struct {
				InstanceIDs []string `json:"instance_ids"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			submittedSizes = append(submittedSizes, len(request.InstanceIDs))
			jobID := fmt.Sprintf("delete-%d", len(submittedSizes))
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job_id":%q,"operation":"FUNCTION_JOB_OPERATION_DELETE_INSTANCES","total_count":%d}`, jobID, len(request.InstanceIDs))
		case "/api/admin/cloudfunction/GetFunctionJob":
			var request struct {
				JobID string `json:"job_id"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			statusReads[request.JobID]++
			total := map[string]int{"delete-1": 100, "delete-2": 70}[request.JobID]
			status := "FUNCTION_JOB_STATUS_SUCCESS"
			failed, success := 0, total
			if request.JobID == "delete-1" {
				status, failed, success = "FUNCTION_JOB_STATUS_FAILED", 100, 0
			} else if statusReads[request.JobID] == 1 {
				status = "FUNCTION_JOB_STATUS_RUNNING"
				success = 0
			}
			_, _ = fmt.Fprintf(w, `{"ret_info":{"code":0},"job":{"job_id":%q,"operation":"FUNCTION_JOB_OPERATION_DELETE_INSTANCES","status":%q,"success_count":%d,"failed_count":%d,"total_count":%d},"items":[]}`, request.JobID, status, success, failed, total)
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
		SpaceID: "stockcn", Confirm: true, Wait: true,
	})
	require.ErrorContains(t, err, "delete-1 finished with status FUNCTION_JOB_STATUS_FAILED")
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
		{name: "service 500", err: fmt.Errorf("SubmitCreateFunctionInstances: code 500: internal error"), want: true},
		{name: "missing job id", err: fmt.Errorf("SubmitCreateFunctionInstances: empty job_id"), want: true},
		{name: "malformed operation", err: fmt.Errorf("SubmitCreateFunctionInstances: operation: unknown enum"), want: true},
		{name: "authorization rejection", err: fmt.Errorf("control returned HTTP 403 Forbidden"), want: false},
		{name: "business rejection", err: fmt.Errorf("SubmitCreateFunctionInstances: code 409: lease stale"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, isAmbiguousCollectorPublishOutcome(test.err))
		})
	}
}

func TestResolveCollectorRootMissing(t *testing.T) {
	_, err := resolveCollectorRoot("/nonexistent/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collector root not found")
}
