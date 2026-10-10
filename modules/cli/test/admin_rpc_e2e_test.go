package test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

type adminRPCWire struct {
	monitorpb.UnimplementedMonitorMgr
	adminpb.UnimplementedSecretMgr
	adminpb.UnimplementedCollectorPublishLease
	adminpb.UnimplementedSetup
	adminpb.UnimplementedSysDeploy
	adminpb.UnimplementedSpaceMgr
}

func startAdminGateway(t *testing.T, wire *adminRPCWire) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTimeout(time.Minute))
	adminpb.RegisterSecretMgrService(service, wire)
	monitorpb.RegisterMonitorMgrService(service, wire)
	adminpb.RegisterCollectorPublishLeaseService(service, wire)
	adminpb.RegisterSetupService(service, wire)
	adminpb.RegisterSysDeployService(service, wire)
	adminpb.RegisterSpaceMgrService(service, wire)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	temp := t.TempDir()
	ready := filepath.Join(temp, "ready")
	process := startGatewayHelperProcess(t, buildGatewayE2EHelper(t), "--mode", "doctor-native", "--node-id", klineGatewayNode, "--upstream-addr", listener.Addr().String(), "--ready-file", ready, "--nonce-dir", filepath.Join(temp, "nonces"), "--key-id", klineGatewayKeyID)
	t.Cleanup(func() {
		if process.stop(5 * time.Second) {
			t.Errorf("Admin gateway required kill: %s", process.logs.String())
		}
	})
	target, err := process.waitForReady(ready, 30*time.Second)
	require.NoError(t, err)
	return target
}
func TestAdminClientsUseSharedSSHNativeGateway(t *testing.T) {
	target := startAdminGateway(t, &adminRPCWire{})
	home, manifest := writeKlineOperator(t, target)
	t.Setenv("HOME", home)
	snapshot, err := setupconfig.Load(manifest, filepath.Dir(manifest))
	require.NoError(t, err)
	gateway, err := gatewayio.Open(t.Context(), snapshot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client := &adminclient.Client{Gateway: gateway, SpaceID: "crypto"}
	secret, err := client.GetSecretValue(t.Context(), "secret-1")
	require.NoError(t, err)
	require.Equal(t, "fixture-secret", secret.SecretValue)
	lease, err := client.AcquireCollectorPublishLease(t.Context(), "crypto", "holder-1")
	require.NoError(t, err)
	require.EqualValues(t, 17, lease.FencingToken)
	lease, err = client.RenewCollectorPublishLease(t.Context(), lease)
	require.NoError(t, err)
	require.NoError(t, client.ReleaseCollectorPublishLease(t.Context(), lease))
	setup := setupclient.New(gateway)
	status, err := setup.Status(t.Context(), snapshot)
	require.NoError(t, err)
	require.Equal(t, "ready", status.State)
	applied, err := setup.Apply(t.Context(), snapshot)
	require.NoError(t, err)
	require.Equal(t, "created", applied.Action)
	require.NoError(t, setup.SyncHostPlacements(t.Context(), snapshot, snapshot.Manifest.ControlHost().Name))
	doctor := doctorcli.New(gateway)
	doctorContext, err := doctor.GetDoctorContext(t.Context(), &monitorpb.GetDoctorContextReq{NodeId: "control"})
	require.NoError(t, err)
	require.Equal(t, "fixture-doctor-checksum", doctorContext.GetManifestChecksum())
	rows, err := doctor.ListPlacements(t.Context(), "control")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var space adminpb.CreateSpaceRsp
	require.NoError(t, gateway.Invoke(t.Context(), "trpc.moox.admin.SpaceMgr", "CreateSpace", &adminpb.CreateSpaceReq{Space: &adminpb.Space{SpaceId: "fixture"}}, &space))
	require.Equal(t, "fixture", space.GetSpace().GetSpaceId())
	// Machine-only lease validation remains unavailable to the operator.
	var forbidden adminpb.ValidateCollectorPublishLeaseRsp
	require.Error(t, gateway.Invoke(t.Context(), "trpc.moox.admin.CollectorPublishLease", "ValidateCollectorPublishLease", &adminpb.ValidateCollectorPublishLeaseReq{}, &forbidden))
	require.NoError(t, gateway.Close())
	_, err = client.GetSecretValue(t.Context(), "secret-1")
	require.Error(t, err)
}

func (w *adminRPCWire) GetSecretValue(_ context.Context, req *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
	return &adminpb.GetSecretValueRsp{RetInfo: &adminpb.RetInfo{}, Secret: &adminpb.SecretMaterial{SecretId: req.GetSecretId(), Category: "cloud", Provider: "tencent", Status: "active", KeyId: "fixture-id", SecretValue: "fixture-secret"}}, nil
}

func (w *adminRPCWire) AcquireCollectorPublishLease(_ context.Context, req *adminpb.AcquireCollectorPublishLeaseReq) (*adminpb.CollectorPublishLeaseRsp, error) {
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: req.GetSpaceId(), LeaseId: "lease-1", FencingToken: 17, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, nil
}

func (w *adminRPCWire) RenewCollectorPublishLease(_ context.Context, req *adminpb.RenewCollectorPublishLeaseReq) (*adminpb.CollectorPublishLeaseRsp, error) {
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: req.GetSpaceId(), LeaseId: req.GetLeaseId(), FencingToken: req.GetFencingToken(), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, nil
}

func (w *adminRPCWire) ReleaseCollectorPublishLease(_ context.Context, req *adminpb.ReleaseCollectorPublishLeaseReq) (*adminpb.ReleaseCollectorPublishLeaseRsp, error) {
	return &adminpb.ReleaseCollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, Released: true}, nil
}

func (w *adminRPCWire) GetSetupStatus(_ context.Context, req *adminpb.GetSetupStatusReq) (*adminpb.GetSetupStatusRsp, error) {
	return &adminpb.GetSetupStatusRsp{RetInfo: &adminpb.RetInfo{}, State: "ready"}, nil
}

func (w *adminRPCWire) ApplySetup(_ context.Context, req *adminpb.ApplySetupReq) (*adminpb.ApplySetupRsp, error) {
	return &adminpb.ApplySetupRsp{RetInfo: &adminpb.RetInfo{}, Action: "created"}, nil
}

func (w *adminRPCWire) ListHosts(_ context.Context, req *adminpb.ListDeploymentHostsReq) (*adminpb.ListDeploymentHostsRsp, error) {
	return &adminpb.ListDeploymentHostsRsp{RetInfo: &adminpb.RetInfo{}, Hosts: []*adminpb.DeploymentHost{{HostId: "control", Address: "127.0.0.1", Status: "enabled"}}}, nil
}
func (w *adminRPCWire) ListPlacements(_ context.Context, req *adminpb.ListPlacementsReq) (*adminpb.ListPlacementsRsp, error) {
	return &adminpb.ListPlacementsRsp{RetInfo: &adminpb.RetInfo{}, Placements: []*adminpb.ComponentPlacement{{HostId: "control", ComponentId: "admin", Status: "enabled"}}}, nil
}

func (w *adminRPCWire) CreateSpace(_ context.Context, req *adminpb.CreateSpaceReq) (*adminpb.CreateSpaceRsp, error) {
	return &adminpb.CreateSpaceRsp{RetInfo: &adminpb.RetInfo{}, Space: req.GetSpace()}, nil
}

func (*adminRPCWire) GetDoctorContext(_ context.Context, req *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	if req.GetNodeId() != "control" {
		return &monitorpb.GetDoctorContextRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM}}, nil
	}
	return &monitorpb.GetDoctorContextRsp{RetInfo: &commonpb.RetInfo{}, ManifestChecksum: "fixture-doctor-checksum", GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func (w *adminRPCWire) SyncHostPlacements(_ context.Context, req *adminpb.SyncHostPlacementsReq) (*adminpb.SyncHostPlacementsRsp, error) {
	if req.GetHostId() == "" || len(req.GetComponentIds()) == 0 {
		return &adminpb.SyncHostPlacementsRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INVALID_PARAM}}, nil
	}
	return &adminpb.SyncHostPlacementsRsp{RetInfo: &adminpb.RetInfo{}}, nil
}
