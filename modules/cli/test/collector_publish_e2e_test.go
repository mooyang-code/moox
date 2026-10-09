package test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorPublishStatusQueriesRealJobAndItems(t *testing.T) {
	wire := &cloudNodeWire{}
	target := startCloudNodeGateway(t, wire)
	operatorHome, manifestPath := writeKlineOperator(t, target)
	binary := buildMooxCLI(t)
	command := exec.Command(binary,
		"collector", "function", "publish", "status",
		"--control-url", "http://unused.invalid",
		"--file", manifestPath,
		"--job-id", "node-batch-e2e",
	)
	command.Env = append(os.Environ(), "HOME="+operatorHome)
	output, err := command.Output()
	require.NoError(t, err)

	var response struct {
		Job struct {
			JobID  string `json:"job_id"`
			Status string `json:"status"`
		} `json:"job"`
		Items []struct {
			NodeID       string `json:"node_id"`
			ErrorMessage string `json:"error_message"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(output, &response))
	assert.Equal(t, "node-batch-e2e", response.Job.JobID)
	assert.Equal(t, "NODE_BATCH_STATUS_FAILED", response.Job.Status)
	require.Len(t, response.Items, 1)
	assert.Equal(t, "SCF rejected", response.Items[0].ErrorMessage)
	assert.EqualValues(t, 1, wire.statusCalls.Load())
}

func TestCollectorPublishCommandsRejectPositionalArguments(t *testing.T) {
	binary := buildMooxCLI(t)
	cases := [][]string{
		{"collector", "function", "publish", "status", "node-batch-fake", "--control-url", "http://127.0.0.1:1", "--job-id", "node-batch-real"},
		{"collector", "function", "publish", "submit", "collector.zip"},
	}
	for _, args := range cases {
		command := exec.Command(binary, args...)
		output, err := command.CombinedOutput()
		require.Error(t, err, "command unexpectedly accepted positional args: %v\n%s", args, output)
		assert.Contains(t, string(output), "unknown command")
	}
}

func buildMooxCLI(t *testing.T) string {
	t.Helper()
	if binary := prebuiltE2EBinary(t, "MOOX_CLI_E2E_BINARY"); binary != "" {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "moox-cli")
	command := exec.Command("go", "build", "-o", binary, "../cmd/moox-cli")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "build moox-cli: %s", output)
	return binary
}

// Cross-platform validation executes locally built pure-Go artifacts instead
// of invoking a compiler on the Linux execution host.
func prebuiltE2EBinary(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if path != "" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0)
	}
	return path
}

// The actual CLI and its library clients share the production native router.
type cloudNodeWire struct {
	cloudnodepb.UnimplementedCloudNodeMgr
	statusCalls atomic.Int32
	uploadURL   string
}

func (w *cloudNodeWire) GetNodeBatchChange(_ context.Context, req *cloudnodepb.GetNodeBatchChangeReq) (*cloudnodepb.GetNodeBatchChangeRsp, error) {
	w.statusCalls.Add(1)
	return &cloudnodepb.GetNodeBatchChangeRsp{RetInfo: &cloudnodepb.RetInfo{}, Job: &cloudnodepb.NodeBatchSummary{JobId: req.GetJobId(), Status: cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED, TotalCount: 1, FailedCount: 1}, Items: []*cloudnodepb.NodeBatchItemResult{{ItemId: "item-1", NodeId: "node-1", Status: cloudnodepb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_FAILED, ErrorMessage: "SCF rejected"}}}, nil
}
func (w *cloudNodeWire) GetNodeList(ctx context.Context, req *cloudnodepb.GetNodeListReq) (*cloudnodepb.GetNodeListRsp, error) {
	if string(codec.Message(ctx).ServerMetaData()["X-Space-Id"]) != "crypto" {
		return nil, fmt.Errorf("missing trusted space")
	}
	if req.GetRegion() != "ap-guangzhou" {
		return nil, fmt.Errorf("region filter changed")
	}
	return &cloudnodepb.GetNodeListRsp{RetInfo: &cloudnodepb.RetInfo{}, Items: []*cloudnodepb.CloudNode{{NodeId: "node-1"}}}, nil
}
func (w *cloudNodeWire) InvokeFunction(ctx context.Context, req *cloudnodepb.InvokeFunctionReq) (*cloudnodepb.InvokeFunctionRsp, error) {
	if req.GetNodeId() != "node-1" {
		return nil, fmt.Errorf("node changed")
	}
	// Longer than the former hardcoded five-second gateway budget.
	select {
	case <-time.After(5100 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &cloudnodepb.InvokeFunctionRsp{RetInfo: &cloudnodepb.RetInfo{}, Scf: &cloudnodepb.ScfInvokeResult{RequestId: "invoke-1"}}, nil
}
func (w *cloudNodeWire) ListCloudAccounts(_ context.Context, req *cloudnodepb.ListCloudAccountsReq) (*cloudnodepb.ListCloudAccountsRsp, error) {
	if req.GetProvider() != "tencent" {
		return nil, fmt.Errorf("provider changed")
	}
	return &cloudnodepb.ListCloudAccountsRsp{RetInfo: &cloudnodepb.RetInfo{}, Accounts: []*cloudnodepb.CloudAccountSummary{{AccountId: "account-1", Provider: "tencent"}}}, nil
}
func (w *cloudNodeWire) CreateCloudAccount(_ context.Context, req *cloudnodepb.CreateCloudAccountReq) (*cloudnodepb.CreateCloudAccountRsp, error) {
	return &cloudnodepb.CreateCloudAccountRsp{RetInfo: &cloudnodepb.RetInfo{}, Account: &cloudnodepb.CloudAccountSummary{AccountId: req.GetAccount().GetAccountId()}}, nil
}
func (w *cloudNodeWire) InitPackageUpload(_ context.Context, req *cloudnodepb.InitPackageUploadReq) (*cloudnodepb.InitPackageUploadRsp, error) {
	if req.GetCloudAccountId() != "account-1" {
		return nil, fmt.Errorf("upload account changed")
	}
	return &cloudnodepb.InitPackageUploadRsp{RetInfo: &cloudnodepb.RetInfo{}, PackageId: "package-1", UploadUrl: w.uploadURL}, nil
}
func (w *cloudNodeWire) CompletePackageUpload(_ context.Context, req *cloudnodepb.CompletePackageUploadReq) (*cloudnodepb.CompletePackageUploadRsp, error) {
	if req.GetPackageId() != "package-1" || req.GetFileSize() != 7 || req.GetFileMd5() == "" {
		return nil, fmt.Errorf("upload transaction changed")
	}
	return &cloudnodepb.CompletePackageUploadRsp{RetInfo: &cloudnodepb.RetInfo{}}, nil
}
func batchResponse(operation cloudnodepb.NodeBatchOperation) *cloudnodepb.SubmitNodeBatchRsp {
	return &cloudnodepb.SubmitNodeBatchRsp{RetInfo: &cloudnodepb.RetInfo{}, JobId: "job-1", Operation: operation, TotalCount: 1}
}
func (w *cloudNodeWire) SubmitCreateNodes(_ context.Context, req *cloudnodepb.BatchCreateNodesReq) (*cloudnodepb.SubmitNodeBatchRsp, error) {
	if len(req.GetNodes()) != 1 || req.GetNodes()[0].GetCollectorPublishFencingToken() != 17 {
		return nil, fmt.Errorf("create fence changed")
	}
	return batchResponse(cloudnodepb.NodeBatchOperation_NODE_BATCH_OPERATION_CREATE_NODES), nil
}
func (w *cloudNodeWire) SubmitDeployNodes(_ context.Context, req *cloudnodepb.BatchDeployNodesReq) (*cloudnodepb.SubmitNodeBatchRsp, error) {
	if len(req.GetDeployments()) != 1 || req.GetDeployments()[0].GetCollectorPublishFencingToken() != 17 {
		return nil, fmt.Errorf("deploy fence changed")
	}
	return batchResponse(cloudnodepb.NodeBatchOperation_NODE_BATCH_OPERATION_DEPLOY_NODES), nil
}
func (w *cloudNodeWire) SubmitDeleteNodes(_ context.Context, req *cloudnodepb.BatchDeleteNodesReq) (*cloudnodepb.SubmitNodeBatchRsp, error) {
	if req.GetCollectorPublishFencingToken() != 17 || len(req.GetNodeIds()) != 1 {
		return nil, fmt.Errorf("delete fence changed")
	}
	return batchResponse(cloudnodepb.NodeBatchOperation_NODE_BATCH_OPERATION_DELETE_NODES), nil
}
func (w *cloudNodeWire) SubmitUpdateNodeRuntimeConfigs(_ context.Context, req *cloudnodepb.BatchUpdateNodeRuntimeConfigsReq) (*cloudnodepb.SubmitNodeBatchRsp, error) {
	if len(req.GetNodes()) != 1 {
		return nil, fmt.Errorf("runtime patch changed")
	}
	return batchResponse(cloudnodepb.NodeBatchOperation_NODE_BATCH_OPERATION_UPDATE_RUNTIME_CONFIGS), nil
}
func startCloudNodeGateway(t *testing.T, wire *cloudNodeWire) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTimeout(time.Minute))
	cloudnodepb.RegisterCloudNodeMgrService(service, wire)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	temp := t.TempDir()
	ready := filepath.Join(temp, "ready")
	process := startGatewayHelperProcess(t, buildGatewayE2EHelper(t), "--mode", "cloudnode-native", "--node-id", klineGatewayNode, "--upstream-addr", listener.Addr().String(), "--ready-file", ready, "--nonce-dir", filepath.Join(temp, "nonces"), "--key-id", klineGatewayKeyID)
	t.Cleanup(func() {
		if process.stop(5 * time.Second) {
			t.Errorf("CloudNode gateway required kill: %s", process.logs.String())
		}
	})
	target, err := process.waitForReady(ready, 30*time.Second)
	require.NoError(t, err)
	return target
}
func TestCloudNodeCommandsUseSSHNativeGatewayAndKeepUploadAndFences(t *testing.T) {
	var uploaded atomic.Int32
	cos := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "expected COS PUT", http.StatusMethodNotAllowed)
			return
		}
		uploaded.Add(1)
	}))
	defer cos.Close()
	wire := &cloudNodeWire{uploadURL: cos.URL}
	target := startCloudNodeGateway(t, wire)
	home, manifest := writeKlineOperator(t, target)
	t.Setenv("HOME", home)
	snapshot, err := setupconfig.Load(manifest, filepath.Dir(manifest))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gateway, err := gatewayio.Open(ctx, snapshot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client := &adminclient.Client{Gateway: gateway, SpaceID: "crypto"}
	accounts, err := client.ListCloudAccounts(ctx, "tencent")
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	account, err := client.CreateCloudAccount(ctx, adminclient.CloudAccountInput{AccountID: "account-1"})
	require.NoError(t, err)
	require.Equal(t, "account-1", account.AccountID)
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{Region: "ap-guangzhou"})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	result, err := client.InvokeFunction(ctx, "node-1", map[string]any{"action": "probe"})
	require.NoError(t, err)
	require.Equal(t, "invoke-1", result["_cloudnode_request_id"])
	upload, err := client.UploadPackage(ctx, adminclient.UploadPackageRequest{CloudAccountID: "account-1"}, []byte("package"))
	require.NoError(t, err)
	require.Equal(t, "package-1", upload.PackageID)
	require.EqualValues(t, 1, uploaded.Load())
	client.SetCollectorPublishLease(&adminclient.CollectorPublishLease{SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 17})
	_, err = client.SubmitCreateNodes(ctx, []adminclient.NodeCreateItem{{CloudAccountID: "account-1", PackageID: "package-1"}})
	require.NoError(t, err)
	_, err = client.SubmitDeployNodes(ctx, []adminclient.NodeDeployItem{{NodeID: "node-1", PackageID: "package-1"}})
	require.NoError(t, err)
	_, err = client.SubmitDeleteNodes(ctx, []string{"node-1"})
	require.NoError(t, err)
	var runtime map[string]any
	err = client.CallGatewayJSON(ctx, "trpc.moox.cloudnode.CloudNodeMgr", "SubmitUpdateNodeRuntimeConfigs", map[string]any{"nodes": []map[string]any{{"node_id": "node-1"}}}, &runtime)
	require.NoError(t, err)
	status, err := client.GetNodeBatchChange(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, "job-1", status.Job.JobID)
	require.NotContains(t, fmt.Sprint(runtime), klineGatewaySecret)
}
