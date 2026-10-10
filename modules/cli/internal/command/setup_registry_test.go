package command

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type registryGateway struct {
	t         *testing.T
	failSync  bool
	failProbe bool
	calls     int
}

func (g *registryGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	require.Equal(g.t, "trpc.moox.ops.SysDeploy", service)
	require.Equal(g.t, "SyncHostPlacements", method)
	request := req.(*pb.SyncHostPlacementsReq)
	require.Equal(g.t, "compute", request.GetHostId())
	require.Equal(g.t, []string{"access", "trade"}, request.GetComponentIds())
	g.calls++
	if g.failSync {
		return errors.New("fixture transport failed")
	}
	rsp.(*pb.SyncHostPlacementsRsp).RetInfo = &pb.RetInfo{}
	return nil
}
func (g *registryGateway) Forward(_ context.Context, service, method string, _ int, _ []byte) ([]byte, error) {
	require.Equal(g.t, "trpc.moox.trade.TradeConsoleService", service)
	require.Equal(g.t, "GetExecutionCapabilities", method)
	if g.failProbe {
		return nil, errors.New("fixture probe failed")
	}
	status, err := protojson.Marshal(&commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION})
	return []byte(`{"ret_info":` + string(status) + `}`), err
}
func (*registryGateway) Close() error { return nil }

type registrySSH struct {
	localReadSSH
	t     *testing.T
	calls []string
}

func (s *registrySSH) Run(ctx context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	require.NoError(s.t, ctx.Err(), "rollback must use its own cleanup context")
	s.calls = append(s.calls, strings.Join(argv, " "))
	return setupssh.Result{}, nil
}

func TestServiceRegistryUsesFullHostPlacementsAndRollsBackEveryFailure(t *testing.T) {
	for _, stage := range []string{"success", "gateway", "sync", "probe", "placement"} {
		t.Run(stage, func(t *testing.T) {
			snapshot := setupSnapshot(t)
			host := setupconfig.Host{Name: "compute", Address: "compute.example.test", Username: "fixture"}
			testfixture.SetHost(&snapshot.Manifest, host, "trade", "access")
			if stage == "placement" {
				snapshot.Manifest.Placements[host.Name] = []string{"access"}
			}
			ssh := &registrySSH{t: t}
			gateway := &registryGateway{t: t, failSync: stage == "sync", failProbe: stage == "probe"}
			previous := openCommandGateway
			openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) {
				if stage == "gateway" {
					return nil, context.Canceled
				}
				return gateway, nil
			}
			t.Cleanup(func() { openCommandGateway = previous })
			result, err := syncSetupServiceRegistry(t.Context(), snapshot, ssh, host, "trade", true, setupdeploy.ServiceResult{DeployDir: "/isolated/deployment"})
			if stage == "success" {
				require.NoError(t, err)
				require.True(t, result.RegistrySynced)
				require.Empty(t, ssh.calls)
				require.Equal(t, 1, gateway.calls)
			} else {
				require.ErrorContains(t, err, "service_registry_failed")
				require.False(t, result.RegistrySynced)
				require.Len(t, ssh.calls, 2)
				require.Contains(t, ssh.calls[0], "moox-stop-trade-after-")
				require.Contains(t, ssh.calls[1], "moox-rollback-service")
			}
		})
	}
}
