package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestRegisterServiceDeploymentCreatesFactorEngineOnRemoteNode(t *testing.T) {
	rows := map[string]*pb.ServiceDeployment{}
	nodes := map[string]*pb.GatewayNode{}
	c := New(factorEngineRegistryForwarder(t, rows, nodes))
	require.NoError(t, c.RegisterServiceDeployment(context.Background(), "factor-1", "factor-engine", "192.168.0.102"))
	created := rows["factor-1/moox_factor_engine"]
	require.NotNil(t, created)
	require.Equal(t, "factor-1", created.GetNodeId())
	require.Equal(t, "moox_factor_engine", created.GetServiceName())
	require.Equal(t, "factor-engine", created.GetServiceKind())
	require.Equal(t, "192.168.0.102", created.GetHost())
	require.Equal(t, int32(11415), created.GetPort())
	require.Equal(t, "http", created.GetProtocol())
	require.Equal(t, "internal", created.GetScope())
	require.Equal(t, "active", created.GetStatus())
	require.False(t, created.GetGatewayEnabled())
	require.JSONEq(t, `{"monitor_enabled":false,"managed_by":"moox-cli"}`, created.GetExtraConfig())
	node := nodes["factor-1"]
	require.NotNil(t, node)
	require.Equal(t, "https://192.168.0.102:11415", node.GetPublicAddress())
	require.Equal(t, "disabled", node.GetStatus())
}

func TestRegisterFactorEngineKeepsExistingGatewayAddress(t *testing.T) {
	rows := map[string]*pb.ServiceDeployment{}
	nodes := map[string]*pb.GatewayNode{
		"factor-1": {NodeId: "factor-1", Name: "factor-1", PublicAddress: "https://192.168.0.102:11001", Status: "enabled"},
	}
	c := New(factorEngineRegistryForwarder(t, rows, nodes))
	require.NoError(t, c.RegisterServiceDeployment(context.Background(), "factor-1", "factor-engine", "192.168.0.102"))
	require.Equal(t, "https://192.168.0.102:11001", nodes["factor-1"].GetPublicAddress())
	require.Equal(t, "enabled", nodes["factor-1"].GetStatus())
	require.NotNil(t, rows["factor-1/moox_factor_engine"])
}

func factorEngineRegistryForwarder(t *testing.T, rows map[string]*pb.ServiceDeployment, nodes map[string]*pb.GatewayNode) *fakeForwarder {
	t.Helper()
	return &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var response proto.Message
		switch r.URL.Path {
		case "/trpc.moox.ops.SysDeploy/ListGatewayNodes":
			var req pb.ListGatewayNodesReq
			require.NoError(t, protojson.Unmarshal(raw, &req))
			rsp := &pb.ListGatewayNodesRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}
			if node := nodes[req.GetNodeId()]; node != nil {
				rsp.Nodes = []*pb.GatewayNode{node}
			}
			response = rsp
		case "/trpc.moox.ops.SysDeploy/CreateGatewayNode":
			var req pb.CreateGatewayNodeReq
			require.NoError(t, protojson.Unmarshal(raw, &req))
			nodes[req.GetNode().GetNodeId()] = req.GetNode()
			response = &pb.CreateGatewayNodeRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Node: req.GetNode()}
		case "/trpc.moox.ops.SysDeploy/UpdateGatewayNode":
			var req pb.UpdateGatewayNodeReq
			require.NoError(t, protojson.Unmarshal(raw, &req))
			nodes[req.GetNodeId()] = req.GetNode()
			response = &pb.UpdateGatewayNodeRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Node: req.GetNode()}
		case "/trpc.moox.ops.SysDeploy/GetServiceDeployment":
			var req pb.GetServiceDeploymentReq
			require.NoError(t, protojson.Unmarshal(raw, &req))
			row := rows[req.GetNodeId()+"/"+req.GetServiceName()]
			code := pb.ErrorCode_SUCCESS
			if row == nil {
				code = pb.ErrorCode_NOT_FOUND
			}
			response = &pb.GetServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: code}, Deployment: row}
		case "/trpc.moox.ops.SysDeploy/CreateServiceDeployment":
			var req pb.CreateServiceDeploymentReq
			require.NoError(t, protojson.Unmarshal(raw, &req))
			row := req.GetDeployment()
			if nodes[row.GetNodeId()] == nil {
				response = &pb.CreateServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NOT_FOUND, Msg: "gateway node not found"}}
				break
			}
			if row.GetPort() <= 0 {
				response = &pb.CreateServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "port must be between 1 and 65535"}}
				break
			}
			rows[row.GetNodeId()+"/"+row.GetServiceName()] = row
			response = &pb.CreateServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Deployment: row}
		case "/trpc.moox.ops.SysDeploy/UpdateServiceDeployment":
			var req pb.UpdateServiceDeploymentReq
			require.NoError(t, protojson.Unmarshal(raw, &req))
			rows[req.GetNodeId()+"/"+req.GetServiceName()] = req.GetDeployment()
			response = &pb.UpdateServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Deployment: req.GetDeployment()}
		default:
			http.NotFound(w, r)
			return
		}
		encoded, err := protojson.Marshal(response)
		require.NoError(t, err)
		_, _ = w.Write(encoded)
	})}
}

func TestFactorEngineRegistrationDisablesUnreachableHealthProbe(t *testing.T) {
	canonical, spec := lookupServiceDeployment("factor-engine")
	require.Equal(t, "moox_factor_engine", canonical)
	require.Equal(t, "http", spec.protocol)
	require.Equal(t, int32(11415), spec.port)
	require.Equal(t, int32(11415), spec.healthPort)

	aliases := []string{"factor-engine", "moox-factor-engine", "moox_factor_engine"}
	for _, name := range aliases {
		got, _ := lookupServiceDeployment(name)
		require.Equal(t, "moox_factor_engine", got, name)
	}

	remote := registrationExtra("factor-1", "192.168.0.102", spec)
	var extra map[string]any
	require.NoError(t, json.Unmarshal([]byte(remote), &extra))
	require.Equal(t, false, extra["monitor_enabled"])
	_, hasHealthURL := extra["health_url"]
	require.False(t, hasHealthURL)
}

func TestRegisterFactorEngineUpdateDisablesMonitoring(t *testing.T) {
	rows := map[string]*pb.ServiceDeployment{
		"factor-1/moox_factor_engine": {
			NodeId: "factor-1", ServiceName: "moox_factor_engine", ExtraConfig: `{"monitor_enabled":true}`,
		},
	}
	nodes := map[string]*pb.GatewayNode{
		"factor-1": {NodeId: "factor-1", Name: "factor-1", PublicAddress: "https://192.168.0.102:11001", Status: "enabled"},
	}
	c := New(factorEngineRegistryForwarder(t, rows, nodes))
	require.NoError(t, c.RegisterServiceDeployment(context.Background(), "factor-1", "factor-engine", "192.168.0.102"))
	require.JSONEq(t, `{"monitor_enabled":false,"managed_by":"moox-cli"}`, rows["factor-1/moox_factor_engine"].GetExtraConfig())
	require.Equal(t, "https://192.168.0.102:11001", nodes["factor-1"].GetPublicAddress())
}
