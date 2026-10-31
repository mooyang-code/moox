package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const leaseResponse = `{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"7","expires_at":"2099-01-01T00:00:00Z"}`

func cloudWriteCommand(t *testing.T, use string) cloudFunctionWrite {
	t.Helper()
	for _, write := range cloudFunctionWrites {
		if write.Use == use {
			return write
		}
	}
	t.Fatalf("没有 cloud function %s", use)
	return cloudFunctionWrite{}
}

func runCloudWrite(t *testing.T, use string, args ...string) (string, string, error) {
	t.Helper()
	cmd := cloudWriteCommand(t, use).command()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestCloudFunctionDeployPreviewsThenHoldsLeaseAndFillsFence(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"AcquireCollectorPublishLease":  leaseResponse,
		"ReleaseCollectorPublishLease":  `{"ret_info":{"code":0},"released":true}`,
		"SubmitDeployFunctionInstances": `{"ret_info":{"code":0},"job_id":"job-1","operation":"FUNCTION_JOB_OPERATION_DEPLOY_INSTANCES","total_count":1}`,
		"GetFunctionJob":                `{"ret_info":{"code":0},"job":{"job_id":"job-1","status":"FUNCTION_JOB_STATUS_SUCCESS","total_count":1,"success_count":1}}`,
	}}
	useRPCGateway(t, gateway)
	data := `{"deployments":[{"instance_id":"i-1","package_id":"p-1"}]}`

	out, _, err := runCloudWrite(t, "deploy", "--data", data)
	require.NoError(t, err)
	assert.Zero(t, gateway.calls, "没有 --yes 只预览：不能获取租约也不能提交")
	assert.Equal(t, "SubmitDeployFunctionInstances", parsePreview(t, out)["method"])

	out, _, err = runCloudWrite(t, "deploy", "--data", data, "--yes")
	require.NoError(t, err)
	assert.Equal(t, []string{"AcquireCollectorPublishLease", "SubmitDeployFunctionInstances", "GetFunctionJob", "ReleaseCollectorPublishLease"}, gateway.methods, "先拿租约，提交，等作业，最后释放")
	var request struct {
		Deployments []map[string]any `json:"deployments"`
	}
	require.NoError(t, json.Unmarshal(gateway.bodies["SubmitDeployFunctionInstances"], &request))
	require.Len(t, request.Deployments, 1)
	assert.Equal(t, "lease-1", request.Deployments[0]["collector_publish_lease_id"])
	assert.EqualValues(t, 7, request.Deployments[0]["collector_publish_fencing_token"])
	assert.Equal(t, "i-1", request.Deployments[0]["instance_id"], "原有字段保留")
	assert.Contains(t, out, "FUNCTION_JOB_STATUS_SUCCESS")
}

func TestCloudFunctionDeleteUsesTopLevelFenceAndReportsFailedJob(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"AcquireCollectorPublishLease":  leaseResponse,
		"ReleaseCollectorPublishLease":  `{"ret_info":{"code":0},"released":true}`,
		"SubmitDeleteFunctionInstances": `{"ret_info":{"code":0},"job_id":"job-2","total_count":2}`,
		"GetFunctionJob":                `{"ret_info":{"code":0},"job":{"job_id":"job-2","status":"FUNCTION_JOB_STATUS_PARTIAL","total_count":2,"success_count":1,"failed_count":1}}`,
	}}
	useRPCGateway(t, gateway)
	_, _, err := runCloudWrite(t, "delete", "--instance-id", "a,b", "--yes")
	require.ErrorContains(t, err, "没有全部成功")
	var request map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["SubmitDeleteFunctionInstances"], &request))
	assert.Equal(t, []any{"a", "b"}, request["instance_ids"])
	assert.Equal(t, "lease-1", request["collector_publish_lease_id"])
	assert.EqualValues(t, 7, request["collector_publish_fencing_token"])
	assert.Equal(t, "ReleaseCollectorPublishLease", gateway.methods[len(gateway.methods)-1], "失败也要释放租约")

	_, _, err = runCloudWrite(t, "delete", "--yes")
	require.ErrorContains(t, err, "缺少 --instance-id")
}

func TestCloudFunctionCreateWithoutWaitReturnsSubmitResponse(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"AcquireCollectorPublishLease":  leaseResponse,
		"ReleaseCollectorPublishLease":  `{"ret_info":{"code":0},"released":true}`,
		"SubmitCreateFunctionInstances": `{"ret_info":{"code":0},"job_id":"job-3","total_count":1}`,
	}}
	useRPCGateway(t, gateway)
	out, _, err := runCloudWrite(t, "create", "--data", `{"instances":[{"cloud_account_id":"a","function_type":"scf-event","region":"ap-guangzhou","package_id":"p"}]}`, "--wait=false")
	require.NoError(t, err)
	assert.Equal(t, []string{"AcquireCollectorPublishLease", "SubmitCreateFunctionInstances", "ReleaseCollectorPublishLease"}, gateway.methods)
	assert.Contains(t, out, "job-3")
	_, _, err = runCloudWrite(t, "create")
	require.ErrorContains(t, err, "缺少请求内容")
}

func TestInjectPublishFenceFillsEveryItem(t *testing.T) {
	request := map[string]any{"instances": []any{map[string]any{"instance_id": "a"}, map[string]any{"instance_id": "b"}}}
	injectPublishFence(request, cloudWriteCommand(t, "update-config"), fenceFor("lease-9", 3))
	for _, item := range request["instances"].([]any) {
		assert.Equal(t, "lease-9", item.(map[string]any)["collector_publish_lease_id"])
		assert.EqualValues(t, 3, item.(map[string]any)["collector_publish_fencing_token"])
	}
}

func fenceFor(lease string, token int64) *adminclient.CollectorPublishFence {
	return &adminclient.CollectorPublishFence{LeaseID: lease, FencingToken: token}
}
