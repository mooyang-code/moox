package client

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeForwarder struct {
	handler http.Handler
	remote  string
}

func (f *fakeForwarder) ForwardLocal(ctx context.Context, remote string) (net.Listener, error) {
	f.remote = remote
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: f.handler}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	return listener, nil
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
public_address = "eventbus.example.test"
port = 4222
tls_enabled = true
[control_host]
name = "control"
address = "192.0.2.10"
username = "ubuntu"
password = "recognizable-control-password"
[[other_hosts]]
name = "compute"
address = "192.0.2.11"
username = "ubuntu"
password = "recognizable-compute-password"
`
	path := filepath.Join(root, "custom.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	snapshot, err := setupconfig.Load(path, root)
	require.NoError(t, err)
	return snapshot, path
}

func TestApplyUsesForwardedPrivateSetupEndpoint(t *testing.T) {
	var capturedPath string
	var capturedRequest pb.ApplySetupReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created", Users: 1, Secrets: 1, Hosts: 2,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Apply(context.Background(), clientSnapshot(t))
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, 2, result.Hosts)
	assert.Equal(t, "127.0.0.1:11110", forwarder.remote)
	assert.Equal(t, "/trpc.moox.admin.Setup/ApplySetup", capturedPath)
	assert.Equal(t, "recognizable-secret-key", capturedRequest.GetTencentCloud().GetSecretKey())
	assert.Empty(t, capturedRequest.GetSpaces())
}

func TestApplyWithSpacesMapsAdminSpaceContractAndCounts(t *testing.T) {
	var capturedRequest pb.ApplySetupReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created",
			Users: 1, Secrets: 1, Hosts: 2, Spaces: 1, SpacesCreated: 1,
		})
		_, _ = w.Write(response)
	})}
	spaces := []Space{{
		SpaceID: "stock_cn", Name: "A股市场", Description: "A股行情",
		Owner: "quant", Market: "CN", Timezone: "Asia/Shanghai",
		Status: "active", AttributesJSON: `{"managed_by":"moox-cli"}`,
	}}

	result, err := New(forwarder).ApplyWithSpaces(context.Background(), clientSnapshot(t), spaces)
	require.NoError(t, err)
	require.Len(t, capturedRequest.GetSpaces(), 1)
	assert.Equal(t, "stock_cn", capturedRequest.GetSpaces()[0].GetSpaceId())
	assert.Equal(t, "CN", capturedRequest.GetSpaces()[0].GetMarket())
	assert.Equal(t, "Asia/Shanghai", capturedRequest.GetSpaces()[0].GetTimezone())
	assert.Equal(t, `{"managed_by":"moox-cli"}`, capturedRequest.GetSpaces()[0].GetAttributesJson())
	assert.Equal(t, 1, result.Spaces)
	assert.Equal(t, 1, result.SpacesCreated)
	assert.Zero(t, result.SpacesUnchanged)
}

func TestStatusSendsManifestAndReturnsSanitizedState(t *testing.T) {
	var capturedRequest pb.GetSetupStatusReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed", Users: 1, Secrets: 1, Hosts: 2,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Status(context.Background(), clientSnapshot(t))
	require.NoError(t, err)
	assert.Equal(t, "completed", result.State)
	assert.Equal(t, "recognizable-admin-password", capturedRequest.GetAdmin().GetPassword())
	assert.Empty(t, capturedRequest.GetSpaces())
}

func TestStatusWithSpacesReturnsSpaceCount(t *testing.T) {
	var capturedRequest pb.GetSetupStatusReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed",
			Users: 1, Secrets: 1, Hosts: 2, Spaces: 1,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).StatusWithSpaces(context.Background(), clientSnapshot(t), []Space{{
		SpaceID: "crypto", Name: "加密货币市场", Market: "crypto",
		Timezone: "UTC", Status: "active", AttributesJSON: "{}",
	}})
	require.NoError(t, err)
	require.Len(t, capturedRequest.GetSpaces(), 1)
	assert.Equal(t, "crypto", capturedRequest.GetSpaces()[0].GetSpaceId())
	assert.Equal(t, 1, result.Spaces)
}

func TestApplyStoragePlacementCreatesRemoteLoopbackRoutesAndPublishesGatewayEndpoints(t *testing.T) {
	t.Parallel()
	updated := map[string]*pb.ServiceDeployment{}
	created := map[string]*pb.ServiceDeployment{}
	var gatewayNode *pb.GatewayNode
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/trpc.moox.ops.SysDeploy/ListGatewayNodes":
			response, _ := protojson.Marshal(&pb.ListGatewayNodesRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
			_, _ = w.Write(response)
		case "/trpc.moox.ops.SysDeploy/CreateGatewayNode":
			var input pb.CreateGatewayNodeReq
			body, _ := io.ReadAll(request.Body)
			_ = protojson.Unmarshal(body, &input)
			gatewayNode = input.GetNode()
			response, _ := protojson.Marshal(&pb.CreateGatewayNodeRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Node: gatewayNode})
			_, _ = w.Write(response)
		case "/trpc.moox.ops.SysDeploy/GetServiceDeployment":
			var input pb.GetServiceDeploymentReq
			body, _ := io.ReadAll(request.Body)
			_ = protojson.Unmarshal(body, &input)
			if input.GetNodeId() == "compute-1" {
				response, _ := protojson.Marshal(&pb.GetServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NOT_FOUND}})
				_, _ = w.Write(response)
				return
			}
			response, _ := protojson.Marshal(&pb.GetServiceDeploymentRsp{
				RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
				Deployment: &pb.ServiceDeployment{NodeId: "control", ServiceName: input.GetServiceName(), Host: "127.0.0.1", Status: "active", ExtraConfig: `{"health_url":"http://127.0.0.1:20210/readyz","monitor_enabled":true}`},
			})
			_, _ = w.Write(response)
		case "/trpc.moox.ops.SysDeploy/CreateServiceDeployment":
			var input pb.CreateServiceDeploymentReq
			body, _ := io.ReadAll(request.Body)
			_ = protojson.Unmarshal(body, &input)
			created[input.GetDeployment().GetServiceName()] = input.GetDeployment()
			response, _ := protojson.Marshal(&pb.CreateServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Deployment: input.GetDeployment()})
			_, _ = w.Write(response)
		case "/trpc.moox.ops.SysDeploy/UpdateServiceDeployment":
			var input pb.UpdateServiceDeploymentReq
			body, _ := io.ReadAll(request.Body)
			_ = protojson.Unmarshal(body, &input)
			updated[input.GetServiceName()] = input.GetDeployment()
			response, _ := protojson.Marshal(&pb.UpdateServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Deployment: input.GetDeployment()})
			_, _ = w.Write(response)
		default:
			http.NotFound(w, request)
		}
	})}

	result, err := New(forwarder).ApplyStoragePlacement(context.Background(), "compute-1", "203.0.113.9")
	require.NoError(t, err)
	require.Equal(t, len(storageDeploymentNames), result.Deployments)
	require.Equal(t, "127.0.0.1:11109", forwarder.remote)
	require.Equal(t, "compute-1", gatewayNode.GetNodeId())
	require.Equal(t, "https://203.0.113.9:11001", gatewayNode.GetPublicAddress())
	for _, name := range storageDeploymentNames {
		require.Equal(t, "compute-1", created[name].GetNodeId(), name)
		require.Equal(t, "127.0.0.1", created[name].GetHost(), name)
	}
	require.Equal(t, "203.0.113.9", updated["service_gateway_native"].GetHost())
}

func TestApplyReturnsStableSecretFreeErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "conflict", body: `{"ret_info":{"code":1,"msg":"setup_conflict"}}`, want: "setup_conflict"},
		{name: "unexpected remote text", body: `recognizable-secret-key`, want: "setup_response_invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			})}
			_, err := New(forwarder).Apply(context.Background(), clientSnapshot(t))
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
	forwarder := &fakeForwarder{handler: http.NotFoundHandler()}
	_, err := New(forwarder).Apply(context.Background(), snapshot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}
