package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// fakeGateway 把 Invoke 转成对 handler 的 POST /<服务名>/<方法>（protojson 编码），沿用各测试的 HTTP 处理函数。
type fakeGateway struct {
	handler http.Handler
	service string
}

func (f *fakeGateway) Invoke(ctx context.Context, servicePath, method string, req, rsp any, _ ...gatewayclient.CallOption) error {
	f.service = servicePath
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(req.(proto.Message))
	if err != nil {
		return err
	}
	request := httptest.NewRequest(http.MethodPost, "/"+servicePath+"/"+method, bytes.NewReader(raw)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return fmt.Errorf("HTTP %d", recorder.Code)
	}
	return protojson.Unmarshal(recorder.Body.Bytes(), rsp.(proto.Message))
}

func clientSnapshot(t *testing.T) *setupconfig.Snapshot {
	snapshot, _ := clientSnapshotWithPath(t)
	return snapshot
}

func clientSnapshotWithPath(t *testing.T) (*setupconfig.Snapshot, string) {
	t.Helper()
	root := t.TempDir()
	body := `[admin]
username = "admin"
password = "recognizable-admin-password"
[tencent_cloud]
secret_id = "recognizable-secret-id"
secret_key = "recognizable-secret-key"
[eventbus]
port = 4222
tls_enabled = true
[hosts.control]
address = "eventbus.example.test"
ssh = { username = "ubuntu", password = "recognizable-control-password" }
[hosts.compute]
address = "192.0.2.11"
ssh = { username = "ubuntu", password = "recognizable-compute-password" }
[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor"]
`
	path := filepath.Join(root, "moox.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	snapshot, err := setupconfig.Load(path, root)
	require.NoError(t, err)
	return snapshot, path
}

func TestApplyCallsSetupService(t *testing.T) {
	var capturedPath string
	var capturedRequest pb.ApplySetupReq
	gateway := &fakeGateway{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created", Users: 1, Secrets: 1, Hosts: 2,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(gateway).Apply(context.Background(), clientSnapshot(t))
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, 2, result.Hosts)
	assert.Equal(t, "trpc.moox.admin.Setup", gateway.service)
	assert.Equal(t, "/trpc.moox.admin.Setup/ApplySetup", capturedPath)
	assert.Equal(t, "recognizable-secret-key", capturedRequest.GetTencentCloud().GetSecretKey())
	assert.Empty(t, capturedRequest.GetSpaces())
}

func TestApplyWithSpacesMapsAdminSpaceContractAndCounts(t *testing.T) {
	var capturedRequest pb.ApplySetupReq
	gateway := &fakeGateway{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created",
			Users: 1, Secrets: 1, Hosts: 2, Spaces: 1, SpacesCreated: 1,
		})
		_, _ = w.Write(response)
	})}
	spaces := []Space{{
		SpaceID: "stockcn", Name: "A股市场", Description: "A股行情",
		Owner: "quant", Market: "CN", Timezone: "Asia/Shanghai",
		Status: "active", AttributesJSON: `{"managed_by":"moox-cli"}`,
	}}

	result, err := New(gateway).ApplyWithSpaces(context.Background(), clientSnapshot(t), spaces)
	require.NoError(t, err)
	require.Len(t, capturedRequest.GetSpaces(), 1)
	assert.Equal(t, "stockcn", capturedRequest.GetSpaces()[0].GetSpaceId())
	assert.Equal(t, "CN", capturedRequest.GetSpaces()[0].GetMarket())
	assert.Equal(t, "Asia/Shanghai", capturedRequest.GetSpaces()[0].GetTimezone())
	assert.Equal(t, `{"managed_by":"moox-cli"}`, capturedRequest.GetSpaces()[0].GetAttributesJson())
	assert.Equal(t, 1, result.Spaces)
	assert.Equal(t, 1, result.SpacesCreated)
	assert.Zero(t, result.SpacesUnchanged)
}

func TestStatusSendsManifestAndReturnsSanitizedState(t *testing.T) {
	var capturedRequest pb.GetSetupStatusReq
	gateway := &fakeGateway{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed", Users: 1, Secrets: 1, Hosts: 2,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(gateway).Status(context.Background(), clientSnapshot(t))
	require.NoError(t, err)
	assert.Equal(t, "completed", result.State)
	assert.Equal(t, "recognizable-admin-password", capturedRequest.GetAdmin().GetPassword())
	assert.Empty(t, capturedRequest.GetSpaces())
}

func TestStatusWithSpacesReturnsSpaceCount(t *testing.T) {
	var capturedRequest pb.GetSetupStatusReq
	gateway := &fakeGateway{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed",
			Users: 1, Secrets: 1, Hosts: 2, Spaces: 1,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(gateway).StatusWithSpaces(context.Background(), clientSnapshot(t), []Space{{
		SpaceID: "crypto", Name: "加密货币市场", Market: "crypto",
		Timezone: "UTC", Status: "active", AttributesJSON: "{}",
	}})
	require.NoError(t, err)
	require.Len(t, capturedRequest.GetSpaces(), 1)
	assert.Equal(t, "crypto", capturedRequest.GetSpaces()[0].GetSpaceId())
	assert.Equal(t, 1, result.Spaces)
}

func TestApplyReturnsStableSecretFreeErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "conflict", body: `{"ret_info":{"code":1,"msg":"setup_conflict"}}`, want: "setup_conflict"},
		{name: "unexpected remote text", body: `recognizable-secret-key`, want: "setup_remote_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &fakeGateway{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			})}
			_, err := New(gateway).Apply(context.Background(), clientSnapshot(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "recognizable-secret-key")
			assert.NotContains(t, err.Error(), "recognizable-admin-password")
		})
	}
}

func TestApplyDetectsManifestMutationBeforeRequest(t *testing.T) {
	snapshot, path := clientSnapshotWithPath(t)
	require.NoError(t, os.WriteFile(path, []byte("changed"), 0o600))
	gateway := &fakeGateway{handler: http.NotFoundHandler()}
	_, err := New(gateway).Apply(context.Background(), snapshot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}
