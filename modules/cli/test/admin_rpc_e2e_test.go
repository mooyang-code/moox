package test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

type adminRPCWire struct {
	monitorpb.UnimplementedMonitorMgr
	adminpb.UnimplementedSecretMgr
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
	cfg, err := topology.Load(manifest)
	require.NoError(t, err)
	// setup 下的客户端还读旧格式的 moox.toml（T12 改到新配置包）：另写一份旧格式的，主机与新格式的一致。
	legacy := t.TempDir()
	host := cfg.ControlHost()
	legacyRaw := fmt.Sprintf("[admin]\nusername='admin'\npassword='admin-test'\n[tencent_cloud]\nsecret_id='AKID-test'\nsecret_key='cloud-test'\n[eventbus]\nport=4222\ntls_enabled=true\n[hosts.%s]\naddress='127.0.0.1'\n[hosts.%s.ssh]\nport=%d\nusername='%s'\nprivate_key_file='%s'\n[deployments]\n%s=['admin','eventbus']\n", host.Name, host.Name, host.Port, host.Username, host.PrivateKeyFile, host.Name)
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "moox.toml"), []byte(legacyRaw), 0o600))
	snapshot, err := setupconfig.Load(filepath.Join(legacy, "moox.toml"), legacy)
	require.NoError(t, err)
	gateway, err := gatewayio.Open(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client := &adminclient.Client{Gateway: gateway, SpaceID: "crypto"}
	secret, err := client.GetSecretValue(t.Context(), "secret-1")
	require.NoError(t, err)
	require.Equal(t, "fixture-secret", secret.SecretValue)
	setup := setupclient.New(gateway)
	status, err := setup.Status(t.Context(), snapshot)
	require.NoError(t, err)
	require.Equal(t, "ready", status.State)
	applied, err := setup.Apply(t.Context(), snapshot)
	require.NoError(t, err)
	require.Equal(t, "created", applied.Action)
	require.NoError(t, setup.SyncHostDeployments(t.Context(), snapshot, snapshot.Manifest.ControlHost().Name))
	doctor := doctorcli.New(gateway)
	doctorContext, err := doctor.GetDoctorContext(t.Context(), &monitorpb.GetDoctorContextReq{NodeId: "control"})
	require.NoError(t, err)
	require.Equal(t, "fixture-doctor-checksum", doctorContext.GetManifestChecksum())
	rows, err := doctor.ListDeployments(t.Context(), "control")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var space adminpb.CreateSpaceRsp
	require.NoError(t, gateway.Invoke(t.Context(), "trpc.moox.admin.SpaceMgr", "CreateSpace", &adminpb.CreateSpaceReq{Space: &adminpb.Space{SpaceId: "fixture"}}, &space))
	require.Equal(t, "fixture", space.GetSpace().GetSpaceId())
	// 只给 Collector 的读云密钥方法，操作员调不到。
	var forbidden adminpb.GetCloudCredentialRsp
	require.Error(t, gateway.Invoke(t.Context(), "trpc.moox.ops.SecretMgr", "GetCloudCredential", &adminpb.GetCloudCredentialReq{SecretId: "secret-1"}, &forbidden))
	require.NoError(t, gateway.Close())
	_, err = client.GetSecretValue(t.Context(), "secret-1")
	require.Error(t, err)
}

func (w *adminRPCWire) GetSecretValue(_ context.Context, req *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
	return &adminpb.GetSecretValueRsp{RetInfo: &adminpb.RetInfo{}, Secret: &adminpb.SecretMaterial{SecretId: req.GetSecretId(), Category: "cloud", Provider: "tencent", Status: "active", KeyId: "fixture-id", SecretValue: "fixture-secret"}}, nil
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
func (w *adminRPCWire) ListDeployments(_ context.Context, req *adminpb.ListDeploymentsReq) (*adminpb.ListDeploymentsRsp, error) {
	return &adminpb.ListDeploymentsRsp{RetInfo: &adminpb.RetInfo{}, Deployments: []*adminpb.ComponentDeployment{{HostId: "control", ComponentId: "admin", Status: "enabled"}}}, nil
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

func (w *adminRPCWire) SyncHostDeployments(_ context.Context, req *adminpb.SyncHostDeploymentsReq) (*adminpb.SyncHostDeploymentsRsp, error) {
	if req.GetHostId() == "" || len(req.GetComponentIds()) == 0 {
		return &adminpb.SyncHostDeploymentsRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INVALID_PARAM}}, nil
	}
	return &adminpb.SyncHostDeploymentsRsp{RetInfo: &adminpb.RetInfo{}}, nil
}
