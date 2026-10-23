package test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"

	"github.com/stretchr/testify/require"
)

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
type cloudFunctionWire struct {
	cloudfunctionpb.UnimplementedCloudFunctionMgr
	statusCalls atomic.Int32
	uploadURL   string
}

func (w *cloudFunctionWire) GetFunctionJob(_ context.Context, req *cloudfunctionpb.GetFunctionJobReq) (*cloudfunctionpb.GetFunctionJobRsp, error) {
	w.statusCalls.Add(1)
	return &cloudfunctionpb.GetFunctionJobRsp{RetInfo: &cloudfunctionpb.RetInfo{}, Job: &cloudfunctionpb.FunctionJobSummary{JobId: req.GetJobId(), Status: cloudfunctionpb.FunctionJobStatus_FUNCTION_JOB_STATUS_FAILED, TotalCount: 1, FailedCount: 1}, Items: []*cloudfunctionpb.FunctionJobItemResult{{ItemId: "item-1", InstanceId: "node-1", Status: cloudfunctionpb.FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_FAILED, ErrorMessage: "SCF rejected"}}}, nil
}
func (w *cloudFunctionWire) ListFunctionInstances(ctx context.Context, req *cloudfunctionpb.ListFunctionInstancesReq) (*cloudfunctionpb.ListFunctionInstancesRsp, error) {
	if string(codec.Message(ctx).ServerMetaData()["X-Space-Id"]) != "crypto" {
		return nil, fmt.Errorf("missing trusted space")
	}
	if req.GetRegion() != "ap-guangzhou" {
		return nil, fmt.Errorf("region filter changed")
	}
	return &cloudfunctionpb.ListFunctionInstancesRsp{RetInfo: &cloudfunctionpb.RetInfo{}, Items: []*cloudfunctionpb.FunctionInstance{{InstanceId: "node-1"}}}, nil
}
func (w *cloudFunctionWire) InvokeFunction(ctx context.Context, req *cloudfunctionpb.InvokeFunctionReq) (*cloudfunctionpb.InvokeFunctionRsp, error) {
	if req.GetInstanceId() != "node-1" {
		return nil, fmt.Errorf("node changed")
	}
	// Longer than the former hardcoded five-second gateway budget.
	select {
	case <-time.After(5100 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &cloudfunctionpb.InvokeFunctionRsp{RetInfo: &cloudfunctionpb.RetInfo{}, Scf: &cloudfunctionpb.ScfInvokeResult{RequestId: "invoke-1"}}, nil
}
func (w *cloudFunctionWire) ListCloudAccounts(_ context.Context, req *cloudfunctionpb.ListCloudAccountsReq) (*cloudfunctionpb.ListCloudAccountsRsp, error) {
	if req.GetProvider() != "tencent" {
		return nil, fmt.Errorf("provider changed")
	}
	return &cloudfunctionpb.ListCloudAccountsRsp{RetInfo: &cloudfunctionpb.RetInfo{}, Accounts: []*cloudfunctionpb.CloudAccountSummary{{AccountId: "account-1", Provider: "tencent"}}}, nil
}
func (w *cloudFunctionWire) CreateCloudAccount(_ context.Context, req *cloudfunctionpb.CreateCloudAccountReq) (*cloudfunctionpb.CreateCloudAccountRsp, error) {
	return &cloudfunctionpb.CreateCloudAccountRsp{RetInfo: &cloudfunctionpb.RetInfo{}, Account: &cloudfunctionpb.CloudAccountSummary{AccountId: req.GetAccount().GetAccountId()}}, nil
}
func (w *cloudFunctionWire) InitPackageUpload(_ context.Context, req *cloudfunctionpb.InitPackageUploadReq) (*cloudfunctionpb.InitPackageUploadRsp, error) {
	if req.GetCloudAccountId() != "account-1" {
		return nil, fmt.Errorf("upload account changed")
	}
	return &cloudfunctionpb.InitPackageUploadRsp{RetInfo: &cloudfunctionpb.RetInfo{}, PackageId: "package-1", UploadUrl: w.uploadURL}, nil
}
func (w *cloudFunctionWire) CompletePackageUpload(_ context.Context, req *cloudfunctionpb.CompletePackageUploadReq) (*cloudfunctionpb.CompletePackageUploadRsp, error) {
	if req.GetPackageId() != "package-1" || req.GetFileSize() != 7 || req.GetFileMd5() == "" {
		return nil, fmt.Errorf("upload transaction changed")
	}
	return &cloudfunctionpb.CompletePackageUploadRsp{RetInfo: &cloudfunctionpb.RetInfo{}}, nil
}
func batchResponse(operation cloudfunctionpb.FunctionJobOperation) *cloudfunctionpb.SubmitFunctionJobRsp {
	return &cloudfunctionpb.SubmitFunctionJobRsp{RetInfo: &cloudfunctionpb.RetInfo{}, JobId: "job-1", Operation: operation, TotalCount: 1}
}
func (w *cloudFunctionWire) SubmitCreateFunctionInstances(_ context.Context, req *cloudfunctionpb.SubmitCreateFunctionInstancesReq) (*cloudfunctionpb.SubmitFunctionJobRsp, error) {
	if len(req.GetInstances()) != 1 || req.GetInstances()[0].GetCollectorPublishFencingToken() != 17 {
		return nil, fmt.Errorf("create fence changed")
	}
	return batchResponse(cloudfunctionpb.FunctionJobOperation_FUNCTION_JOB_OPERATION_CREATE_INSTANCES), nil
}
func (w *cloudFunctionWire) SubmitDeployFunctionInstances(_ context.Context, req *cloudfunctionpb.SubmitDeployFunctionInstancesReq) (*cloudfunctionpb.SubmitFunctionJobRsp, error) {
	if len(req.GetDeployments()) != 1 || req.GetDeployments()[0].GetCollectorPublishFencingToken() != 17 {
		return nil, fmt.Errorf("deploy fence changed")
	}
	return batchResponse(cloudfunctionpb.FunctionJobOperation_FUNCTION_JOB_OPERATION_DEPLOY_INSTANCES), nil
}
func (w *cloudFunctionWire) SubmitDeleteFunctionInstances(_ context.Context, req *cloudfunctionpb.SubmitDeleteFunctionInstancesReq) (*cloudfunctionpb.SubmitFunctionJobRsp, error) {
	if req.GetCollectorPublishFencingToken() != 17 || len(req.GetInstanceIds()) != 1 {
		return nil, fmt.Errorf("delete fence changed")
	}
	return batchResponse(cloudfunctionpb.FunctionJobOperation_FUNCTION_JOB_OPERATION_DELETE_INSTANCES), nil
}
func (w *cloudFunctionWire) SubmitUpdateFunctionInstanceRuntimeConfigs(_ context.Context, req *cloudfunctionpb.SubmitUpdateFunctionInstanceRuntimeConfigsReq) (*cloudfunctionpb.SubmitFunctionJobRsp, error) {
	if len(req.GetInstances()) != 1 {
		return nil, fmt.Errorf("runtime patch changed")
	}
	return batchResponse(cloudfunctionpb.FunctionJobOperation_FUNCTION_JOB_OPERATION_UPDATE_RUNTIME_CONFIGS), nil
}
func startCloudFunctionGateway(t *testing.T, wire *cloudFunctionWire) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTimeout(time.Minute))
	cloudfunctionpb.RegisterCloudFunctionMgrService(service, wire)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	temp := t.TempDir()
	ready := filepath.Join(temp, "ready")
	process := startGatewayHelperProcess(t, buildGatewayE2EHelper(t), "--mode", "cloudfunction-native", "--node-id", klineGatewayNode, "--upstream-addr", listener.Addr().String(), "--ready-file", ready, "--nonce-dir", filepath.Join(temp, "nonces"), "--key-id", klineGatewayKeyID)
	t.Cleanup(func() {
		if process.stop(5 * time.Second) {
			t.Errorf("FunctionInstance gateway required kill: %s", process.logs.String())
		}
	})
	target, err := process.waitForReady(ready, 30*time.Second)
	require.NoError(t, err)
	return target
}
func TestCloudFunctionCommandsUseSSHNativeGatewayAndKeepUploadAndFences(t *testing.T) {
	var uploaded atomic.Int32
	cos := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "expected COS PUT", http.StatusMethodNotAllowed)
			return
		}
		uploaded.Add(1)
	}))
	defer cos.Close()
	wire := &cloudFunctionWire{uploadURL: cos.URL}
	target := startCloudFunctionGateway(t, wire)
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
	nodes, err := client.ListFunctionInstances(ctx, adminclient.FunctionInstanceListFilter{Region: "ap-guangzhou"})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	result, err := client.InvokeFunction(ctx, "node-1", map[string]any{"action": "probe"})
	require.NoError(t, err)
	require.Equal(t, "invoke-1", result["_cloudfunction_request_id"])
	upload, err := client.UploadPackage(ctx, adminclient.UploadPackageRequest{CloudAccountID: "account-1"}, []byte("package"))
	require.NoError(t, err)
	require.Equal(t, "package-1", upload.PackageID)
	require.EqualValues(t, 1, uploaded.Load())
	client.SetCollectorPublishLease(&adminclient.CollectorPublishLease{SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 17})
	_, err = client.SubmitCreateFunctionInstances(ctx, []adminclient.FunctionInstanceCreateItem{{CloudAccountID: "account-1", PackageID: "package-1"}})
	require.NoError(t, err)
	_, err = client.SubmitDeployFunctionInstances(ctx, []adminclient.FunctionInstanceDeployItem{{InstanceID: "node-1", PackageID: "package-1"}})
	require.NoError(t, err)
	_, err = client.SubmitDeleteFunctionInstances(ctx, []string{"node-1"})
	require.NoError(t, err)
	var runtime map[string]any
	err = client.CallGatewayJSON(ctx, "trpc.moox.cloudfunction.CloudFunctionMgr", "SubmitUpdateFunctionInstanceRuntimeConfigs", map[string]any{"instances": []map[string]any{{"instance_id": "node-1"}}}, &runtime)
	require.NoError(t, err)
	status, err := client.GetFunctionJob(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, "job-1", status.Job.JobID)
	require.NotContains(t, fmt.Sprint(runtime), klineGatewaySecret)
}
