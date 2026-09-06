package command

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSyncTradeRegistryRequiresOwnerRouteBeforeSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "failure", false: "success"}[fail], map[bool]string{true: "remote", false: "local"}[remote]}, "/"), func(t *testing.T) {
				ssh := &tradeRegistrySSH{t: t, rows: make(map[string]*pb.ServiceDeployment), failOwner: fail}
				result, err := syncSetupServiceRegistry(context.Background(), ssh, ssh,
					setupconfig.Host{Name: "trade-node", Address: "203.0.113.9"}, "trade", remote,
					setupdeploy.ServiceResult{DeployDir: "/isolated/deployment"})
				ssh.mu.Lock()
				defer ssh.mu.Unlock()
				if fail {
					require.ErrorContains(t, err, "trade_owner_registry_failed")
					require.False(t, result.RegistrySynced)
					require.GreaterOrEqual(t, len(ssh.commands), 1)
					require.Contains(t, strings.Join(ssh.commands, "\n"), "moox-stop-trade-after-registry-failure")
					require.Equal(t, "disabled", ssh.rows["trade-node/moox_trade"].GetStatus())
					return
				}
				require.NoError(t, err)
				require.True(t, result.RegistrySynced)
				require.Equal(t, "active", ssh.rows["trade-node/trade_owner"].GetStatus())
				if remote {
					require.Equal(t, "203.0.113.9", ssh.rows["control/trade_console"].GetHost())
					require.False(t, ssh.rows["control/trade_console"].GetGatewayEnabled())
				} else {
					require.Nil(t, ssh.rows["control/trade_console"])
				}
			})
		}
	}
}

func TestSyncTradeRegistryCleansUpAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ssh := &tradeRegistrySSH{t: t, rows: make(map[string]*pb.ServiceDeployment), cancelOwner: cancel}
	result, err := syncSetupServiceRegistry(ctx, ssh, ssh,
		setupconfig.Host{Name: "trade-node", Address: "203.0.113.9"}, "trade", true,
		setupdeploy.ServiceResult{DeployDir: "/isolated/deployment"})
	require.Error(t, err)
	require.False(t, result.RegistrySynced)
	ssh.mu.Lock()
	defer ssh.mu.Unlock()
	require.GreaterOrEqual(t, len(ssh.commands), 1, "cleanup must run despite caller cancellation")
	require.Contains(t, strings.Join(ssh.commands, "\n"), "moox-stop-trade-after-")
	require.Equal(t, "disabled", ssh.rows["trade-node/moox_trade"].GetStatus())
	require.Equal(t, "disabled", ssh.rows["trade-node/trade_owner"].GetStatus())
	require.True(t, ssh.stopBounded, "cleanup requires its own bounded deadline")
}

func TestSyncTradeRegistryReportsCleanupFailure(t *testing.T) {
	ssh := &tradeRegistrySSH{t: t, rows: make(map[string]*pb.ServiceDeployment), failOwner: true, stopError: errors.New("stop unavailable")}
	_, err := syncSetupServiceRegistry(context.Background(), ssh, ssh,
		setupconfig.Host{Name: "trade-node", Address: "203.0.113.9"}, "trade", false,
		setupdeploy.ServiceResult{DeployDir: "/isolated/deployment"})
	require.ErrorContains(t, err, "trade_owner_registry_failed")
	require.ErrorContains(t, err, "stop unavailable")
}

func TestSyncTradeRegistryReportsDisableFailure(t *testing.T) {
	ssh := &tradeRegistrySSH{t: t, rows: make(map[string]*pb.ServiceDeployment), failOwner: true, failDisable: true}
	result, err := syncSetupServiceRegistry(context.Background(), ssh, ssh,
		setupconfig.Host{Name: "trade-node", Address: "203.0.113.9"}, "trade", false,
		setupdeploy.ServiceResult{DeployDir: "/isolated/deployment"})
	require.False(t, result.RegistrySynced)
	require.ErrorContains(t, err, "trade_owner_registry_failed")
	require.ErrorContains(t, err, "restore Trade registry after failure")
}

type tradeRegistrySSH struct {
	setupssh.Client
	t           *testing.T
	mu          sync.Mutex
	rows        map[string]*pb.ServiceDeployment
	failOwner   bool
	failDisable bool
	commands    []string
	cancelOwner context.CancelFunc
	stopError   error
	stopBounded bool
}

func (s *tradeRegistrySSH) Run(ctx context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	if err := ctx.Err(); err != nil {
		return setupssh.Result{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, strings.Join(argv, " "))
	if strings.Contains(strings.Join(argv, " "), "moox-stop-trade-after-") {
		deadline, ok := ctx.Deadline()
		s.stopBounded = ok && time.Until(deadline) > 0 && time.Until(deadline) <= 30*time.Second
		return setupssh.Result{}, s.stopError
	}
	return setupssh.Result{}, nil
}

func (s *tradeRegistrySSH) ForwardLocal(ctx context.Context, _ string) (net.Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/GetServiceDeployment") {
			var req pb.GetServiceDeploymentReq
			if protojson.Unmarshal(raw, &req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			row := s.rows[req.GetNodeId()+"/"+req.GetServiceName()]
			if row == nil && req.GetServiceName() == "trade_console" {
				row = &pb.ServiceDeployment{NodeId: "control", ServiceName: "trade_console", GatewayEnabled: true}
			}
			code := pb.ErrorCode_SUCCESS
			if row == nil {
				code = pb.ErrorCode_NOT_FOUND
			}
			encoded, _ := protojson.Marshal(&pb.GetServiceDeploymentRsp{RetInfo: &pb.RetInfo{Code: code}, Deployment: row})
			_, _ = w.Write(encoded)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ListGatewayNodes") {
			var req pb.ListGatewayNodesReq
			if protojson.Unmarshal(raw, &req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			encoded, _ := protojson.Marshal(&pb.ListGatewayNodesRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Nodes: []*pb.GatewayNode{{NodeId: req.GetNodeId(), Name: req.GetNodeId(), Status: "enabled", AppliedRouteHash: "route-hash", LastSeenAt: time.Now().UTC().Format(time.RFC3339Nano)}}})
			_, _ = w.Write(encoded)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/CreateGatewayNode") || strings.HasSuffix(r.URL.Path, "/UpdateGatewayNode") {
			if strings.HasSuffix(r.URL.Path, "/CreateGatewayNode") {
				var req pb.CreateGatewayNodeReq
				if protojson.Unmarshal(raw, &req) != nil {
					http.Error(w, "invalid", 400)
					return
				}
				encoded, _ := protojson.Marshal(&pb.CreateGatewayNodeRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Node: req.GetNode()})
				_, _ = w.Write(encoded)
				return
			}
			var req pb.UpdateGatewayNodeReq
			if protojson.Unmarshal(raw, &req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			encoded, _ := protojson.Marshal(&pb.UpdateGatewayNodeRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Node: req.GetNode()})
			_, _ = w.Write(encoded)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/GetGatewayNodeRoutes") {
			var req pb.GetGatewayNodeRoutesReq
			if protojson.Unmarshal(raw, &req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			if req.GetNodeId() == "" {
				http.Error(w, "invalid", 400)
				return
			}
			encoded, _ := protojson.Marshal(&pb.GetGatewayNodeRoutesRsp{
				RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, NodeId: req.GetNodeId(), RouteHash: "route-hash",
				Routes: []*pb.GatewayRoute{
					{ServiceId: "trade_owner", Address: "127.0.0.1:11200", ServicePath: "trpc.moox.trade.TradeConsoleService", AllowedMethods: []string{"GetLogicalAccount", "ClaimLogicalAccountOwner", "ReleaseLogicalAccountOwner", "RebindLogicalAccountOwner"}, AllowedCallers: []string{"strategy"}},
					{ServiceId: "trade_console", Address: "127.0.0.1:11200", ServicePath: "trpc.moox.trade.TradeConsoleService", AllowedMethods: []string{"CreateTradingAccount", "UpdateTradingAccount", "GetTradingAccount", "ListTradingAccounts", "SetLeverage", "SyncTradingAccount", "CreateLogicalAccount", "GetLogicalAccount", "ListLogicalAccounts", "UpdateLogicalAccount", "AddLogicalAccountMember", "RemoveLogicalAccountMember", "PauseLogicalAccount", "ResumeLogicalAccount", "FlattenLogicalAccount", "PlaceManualOrder", "SubmitOrder", "CancelOrder", "GetOperatorAction", "GetLogicalAccountTarget", "GetOrder", "ListOrders", "ListFills", "ListPositions", "CreatePaperSimulation", "ClosePaperSimulation", "GetExecutionCapabilities", "QueryEquityCurve", "ListHoldings"}, AllowedCallers: []string{"admin-gateway"}},
				},
			})
			_, _ = w.Write(encoded)
			return
		}
		var row *pb.ServiceDeployment
		if strings.HasSuffix(r.URL.Path, "/CreateServiceDeployment") {
			var req pb.CreateServiceDeploymentReq
			if protojson.Unmarshal(raw, &req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			row = req.GetDeployment()
		} else if strings.HasSuffix(r.URL.Path, "/UpdateServiceDeployment") {
			var req pb.UpdateServiceDeploymentReq
			if protojson.Unmarshal(raw, &req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			row = req.GetDeployment()
		} else {
			http.NotFound(w, r)
			return
		}
		if s.failOwner && row.GetServiceName() == "trade_owner" || s.failDisable && row.GetStatus() == "disabled" {
			http.Error(w, "unavailable", 503)
			return
		}
		s.rows[row.GetNodeId()+"/"+row.GetServiceName()] = row
		if s.cancelOwner != nil && row.GetServiceName() == "trade_owner" && row.GetStatus() == "active" {
			s.cancelOwner()
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":"SUCCESS"}}`))
	})}
	s.t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	return listener, nil
}
