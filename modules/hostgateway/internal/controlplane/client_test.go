package controlplane

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/server"
)

type fakeGatewayControl struct {
	adminpb.UnimplementedGatewayControl
	mu      sync.Mutex
	callers []string
	reports []*adminpb.ReportStatusReq
	hash    string
}

func (f *fakeGatewayControl) PullSnapshot(ctx context.Context, req *adminpb.PullSnapshotReq) (*adminpb.PullSnapshotRsp, error) {
	f.mu.Lock()
	f.callers = append(f.callers, string(trpc.GetMetaData(ctx, gatewayroute.MetadataVerifiedCaller)))
	f.mu.Unlock()
	if req.GetCurrentHash() == f.hash {
		return &adminpb.PullSnapshotRsp{RetInfo: &adminpb.RetInfo{}}, nil
	}
	return &adminpb.PullSnapshotRsp{RetInfo: &adminpb.RetInfo{}, Changed: true, Snapshot: &adminpb.HostSnapshot{HostId: req.GetHostId(), Hash: f.hash}}, nil
}

func (f *fakeGatewayControl) ReportStatus(_ context.Context, req *adminpb.ReportStatusReq) (*adminpb.ReportStatusRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, req)
	return &adminpb.ReportStatusRsp{RetInfo: &adminpb.RetInfo{}}, nil
}

func startFakeControl(t *testing.T) (*fakeGatewayControl, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	fake := &fakeGatewayControl{hash: "h1"}
	svc := server.New(server.WithAddress(address), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.admin.GatewayControl"))
	adminpb.RegisterGatewayControlService(svc, fake)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	time.Sleep(150 * time.Millisecond)
	return fake, address
}

func controlConfig(target string) config.Config {
	var cfg config.Config
	cfg.Host.ID = "control"
	cfg.Control.Target = target
	cfg.Control.Caller = "host-gateway@control"
	return cfg
}

func TestDirectClientWritesItsOwnCaller(t *testing.T) {
	fake, address := startFakeControl(t)
	client, err := New(Options{Config: controlConfig(address), InstanceID: "inst-1", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, changed, err := client.Pull(context.Background(), "")
	if err != nil || !changed || snapshot.GetHash() != "h1" {
		t.Fatalf("Pull = %v %v %v", snapshot, changed, err)
	}
	if _, changed, err := client.Pull(context.Background(), "h1"); err != nil || changed {
		t.Fatalf("同一哈希应当没有变化: %v %v", changed, err)
	}
	notAfter := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := client.Report(context.Background(), Status{AppliedHash: "h1", RouteCount: 3, LastError: "x", CertificateNotAfter: notAfter}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.callers) != 2 || fake.callers[0] != "host-gateway@control" {
		t.Fatalf("直连时应当写入本机主机网关身份: %v", fake.callers)
	}
	report := fake.reports[0]
	if report.GetInstanceId() != "inst-1" || report.GetVersion() != "v1" || report.GetRouteCount() != 3 || report.GetCertificateNotAfter() != "2031-01-01T00:00:00Z" {
		t.Fatalf("report = %+v", report)
	}
}

func TestRemoteClientRequiresMatchingKey(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "caller-host-gateway.key")
	raw, err := gatewayauth.MarshalCallerKey(gatewayauth.CallerKey{Caller: "host-gateway@compute-1", KeyID: "host-gateway@compute-1-1", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.Host.ID = "storage"
	cfg.Control.Target = "106.53.107.122:11003"
	cfg.Control.Caller = "host-gateway@storage"
	cfg.Control.KeyFile = keyFile
	cfg.TLS.CAFile = filepath.Join(dir, "ca.crt")
	if _, err := New(Options{Config: cfg, InstanceID: "i"}); err == nil || !strings.Contains(err.Error(), "host-gateway@compute-1") {
		t.Fatalf("密钥属于其他主机时应当报错: %v", err)
	}
	if _, err := New(Options{Config: controlConfig("127.0.0.1:11112")}); err == nil {
		t.Fatal("缺少实例 ID 应当报错")
	}
}
