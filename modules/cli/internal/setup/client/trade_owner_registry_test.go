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

func TestRegisterTradeCreatesNodeLocalOwnerRouteIdempotently(t *testing.T) {
	rows := map[string]*pb.ServiceDeployment{
		"control/trade_console": {NodeId: "control", ServiceName: "trade_console", Host: "203.0.113.9", Port: 11200},
	}
	browser := proto.Clone(rows["control/trade_console"])
	c := New(tradeRegistryForwarder(t, rows, ""))
	for range 2 {
		require.NoError(t, c.RegisterServiceDeployment(context.Background(), "trade-node", "trade", "203.0.113.9"))
	}
	require.Len(t, rows, 4)
	require.True(t, proto.Equal(browser, rows["control/trade_console"]))
	owner := rows["trade-node/trade_owner"]
	require.NotNil(t, owner)
	require.Equal(t, "trade-node", owner.GetNodeId())
	require.Equal(t, "127.0.0.1", owner.GetHost())
	require.Equal(t, int32(11200), owner.GetPort())
	require.Equal(t, "http", owner.GetProtocol())
	require.Equal(t, "trpc.moox.trade.TradeConsoleService", owner.GetGatewayPath())
	require.Equal(t, "trade_owner", owner.GetGatewayServiceId())
	require.True(t, owner.GetGatewayEnabled())
	require.Equal(t, "active", owner.GetStatus())
	require.JSONEq(t, `{"gateway_methods":["GetLogicalAccount","ClaimLogicalAccountOwner","ReleaseLogicalAccountOwner","RebindLogicalAccountOwner"],"gateway_callers":["strategy"],"monitor_enabled":false,"managed_by":"moox-cli"}`, owner.GetExtraConfig())
	console := rows["trade-node/trade_console"]
	require.NotNil(t, console)
	require.Equal(t, "trade_console", console.GetGatewayServiceId())
	require.Equal(t, "trpc.moox.trade.TradeConsoleAdminService", console.GetGatewayPath())
	require.True(t, console.GetGatewayEnabled())
	require.Equal(t, "127.0.0.1", console.GetHost())
	require.JSONEq(t, `{"gateway_methods":["CreateTradingAccount","UpdateTradingAccount","GetTradingAccount","ListTradingAccounts","SetLeverage","SyncTradingAccount","CreateLogicalAccount","GetLogicalAccount","ListLogicalAccounts","UpdateLogicalAccount","AddLogicalAccountMember","RemoveLogicalAccountMember","PauseLogicalAccount","ResumeLogicalAccount","FlattenLogicalAccount","PlaceManualOrder","SubmitOrder","CancelOrder","GetOperatorAction","GetLogicalAccountTarget","GetOrder","ListOrders","ListFills","ListPositions","CreatePaperSimulation","ClosePaperSimulation","GetExecutionCapabilities","QueryEquityCurve","ListHoldings"],"gateway_callers":["admin-gateway"],"monitor_enabled":false,"managed_by":"moox-cli"}`, console.GetExtraConfig())

	// Reactivation must not preserve an overbroad ACL or remote upstream on this
	// dedicated machine-owned route. Browser route settings remain untouched.
	owner.ExtraConfig = `{"gateway_methods":["*"],"gateway_callers":["*"]}`
	owner.Host = "203.0.113.8"
	owner.Status = "disabled"
	require.NoError(t, c.RegisterServiceDeployment(context.Background(), "trade-node", "moox_trade", "203.0.113.9"))
	require.Equal(t, "127.0.0.1", rows["trade-node/trade_owner"].GetHost())
	require.NotContains(t, rows["trade-node/trade_owner"].GetExtraConfig(), "*")
	require.NoError(t, c.DisableServiceDeployment(context.Background(), "trade-node", "trade"))
	require.Equal(t, "disabled", rows["trade-node/moox_trade"].GetStatus())
	require.Equal(t, "disabled", rows["trade-node/trade_owner"].GetStatus())
	require.Equal(t, "disabled", rows["trade-node/trade_console"].GetStatus())
}

func TestRegisterTradePropagatesOwnerRouteFailure(t *testing.T) {
	rows := make(map[string]*pb.ServiceDeployment)
	err := New(tradeRegistryForwarder(t, rows, "trade_owner")).RegisterServiceDeployment(context.Background(), "trade-node", "trade", "203.0.113.9")
	require.Error(t, err)
	require.Contains(t, err.Error(), "trade_owner")
}

func TestRemoteTradeRegistrationDisablesUnreachableHealthProbe(t *testing.T) {
	remote := registrationExtra("trade-node", "203.0.113.9", serviceDeploymentCatalogEntry{kind: "trade"})
	var extra map[string]any
	require.NoError(t, json.Unmarshal([]byte(remote), &extra))
	require.Equal(t, false, extra["monitor_enabled"])
	_, hasHealthURL := extra["health_url"]
	require.False(t, hasHealthURL)
	control := registrationExtra("control", "127.0.0.1", serviceDeploymentCatalogEntry{kind: "trade"})
	require.Contains(t, control, "11210/readyz")
}

func TestStrategyRegistrationUsesHTTPServicePort(t *testing.T) {
	canonical, spec := lookupServiceDeployment("strategy")
	require.Equal(t, "moox_strategy", canonical)
	require.Equal(t, "http", spec.protocol)
	require.Equal(t, int32(11433), spec.port)
	require.Equal(t, int32(11431), spec.healthPort)
}

func TestStrategyRegistrationMigratesExistingNativePortToHTTP(t *testing.T) {
	rows := map[string]*pb.ServiceDeployment{
		"control/moox_strategy": {
			NodeId: "control", ServiceName: "moox_strategy", Protocol: "http",
			Host: "127.0.0.1", Port: 11430, GatewayPath: "trpc.moox.strategy.StrategyMgr",
		},
	}
	c := New(tradeRegistryForwarder(t, rows, ""))
	require.NoError(t, c.RegisterServiceDeployment(context.Background(), "control", "strategy", "127.0.0.1"))
	updated := rows["control/moox_strategy"]
	require.Equal(t, int32(11433), updated.GetPort())
	require.Equal(t, "http", updated.GetProtocol())
}

func tradeRegistryForwarder(t *testing.T, rows map[string]*pb.ServiceDeployment, failService string) *fakeForwarder {
	t.Helper()
	nodes := make(map[string]*pb.GatewayNode)
	return &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var response proto.Message
		switch r.URL.Path {
		case "/trpc.moox.ops.SysDeploy/ListGatewayNodes":
			var req pb.ListGatewayNodesReq
			require.NoError(t, protojson.Unmarshal(raw, &req))
			response = &pb.ListGatewayNodesRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}
			if node := nodes[req.GetNodeId()]; node != nil {
				response = &pb.ListGatewayNodesRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Nodes: []*pb.GatewayNode{node}}
			}
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
		case "/trpc.moox.ops.SysDeploy/CreateServiceDeployment", "/trpc.moox.ops.SysDeploy/UpdateServiceDeployment":
			var row *pb.ServiceDeployment
			if r.URL.Path == "/trpc.moox.ops.SysDeploy/CreateServiceDeployment" {
				var req pb.CreateServiceDeploymentReq
				require.NoError(t, protojson.Unmarshal(raw, &req))
				row = req.GetDeployment()
			} else {
				var req pb.UpdateServiceDeploymentReq
				require.NoError(t, protojson.Unmarshal(raw, &req))
				row = req.GetDeployment()
			}
			if row.GetServiceName() == failService {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			rows[row.GetNodeId()+"/"+row.GetServiceName()] = row
			response = &pb.CreateServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Deployment: row}
		default:
			http.NotFound(w, r)
			return
		}
		encoded, err := protojson.Marshal(response)
		require.NoError(t, err)
		_, _ = w.Write(encoded)
	})}
}
