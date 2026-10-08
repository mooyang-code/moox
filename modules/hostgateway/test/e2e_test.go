// Package test 在一台机器上用不同端口模拟 control 和 storage 两台主机，让真实的主机网关参与端到端验证（执行计划阶段 C）。
// 网关控制用一个按组件目录编译快照的假实现代替：真实实现在管理后台，按模块边界不能在这里引用。
package test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/gatewayroute/proto/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	dataView   = "trpc.moox.storage.DataView"
	metadata   = "trpc.moox.storage.Metadata"
	monitorMgr = "trpc.moox.monitor.MonitorMgr"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

// ---------- 证书 ----------

type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	pemFile string
}

func newCA(t *testing.T, dir, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	file := filepath.Join(dir, name+".crt")
	require.NoError(t, os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return testCA{cert: cert, key: key, pemFile: file}
}

func (ca testCA) issue(t *testing.T, dir, hostID string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: hostID},
		DNSNames: []string{hostID}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certFile, keyFile := filepath.Join(dir, hostID+".crt"), filepath.Join(dir, hostID+".key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
}

// ---------- 假的网关控制：按组件目录编译快照 ----------

type fakeControl struct {
	mu          sync.Mutex
	deployment  servicecatalog.Deployment
	keys        map[string][]gatewayroute.VerificationKey
	addresses   map[string]map[string]string // 主机 → service path → 上游地址
	down        bool
	reports     []*adminpb.ReportStatusReq
	pullCallers []string
}

func (f *fakeControl) PullSnapshot(ctx context.Context, req *adminpb.PullSnapshotReq) (*adminpb.PullSnapshotRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caller := string(trpc.GetMetaData(ctx, gatewayroute.MetadataVerifiedCaller))
	f.pullCallers = append(f.pullCallers, caller)
	if f.down {
		return nil, errs.New(5000, "网关控制暂时不可用")
	}
	if caller != servicecatalog.HostGatewayIdentity(req.GetHostId()) {
		return &adminpb.PullSnapshotRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_NO_PERMISSION, Msg: "身份不符"}}, nil
	}
	snapshot, err := f.build(req.GetHostId())
	if err != nil {
		return &adminpb.PullSnapshotRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	if snapshot.GetHash() == req.GetCurrentHash() {
		return &adminpb.PullSnapshotRsp{RetInfo: &adminpb.RetInfo{}}, nil
	}
	return &adminpb.PullSnapshotRsp{RetInfo: &adminpb.RetInfo{}, Changed: true, Snapshot: snapshot}, nil
}

func (f *fakeControl) ReportStatus(ctx context.Context, req *adminpb.ReportStatusReq) (*adminpb.ReportStatusRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errs.New(5000, "网关控制暂时不可用")
	}
	f.reports = append(f.reports, proto.Clone(req).(*adminpb.ReportStatusReq))
	return &adminpb.ReportStatusRsp{RetInfo: &adminpb.RetInfo{}}, nil
}

func (f *fakeControl) build(hostID string) (*adminpb.HostSnapshot, error) {
	compiled, err := servicecatalog.Default().Compile(f.deployment)
	if err != nil {
		return nil, err
	}
	config, ok := compiled.HostConfig(hostID)
	if !ok {
		return nil, fmt.Errorf("没有主机 %s", hostID)
	}
	routes := append([]gatewayroute.Route(nil), config.Routes...)
	for i := range routes {
		if address, ok := f.addresses[hostID][routes[i].ServicePath]; ok {
			routes[i].Address = address
		}
	}
	normalized, err := gatewayroute.NormalizeAndHashState(hostID, config.Disabled, routes)
	if err != nil {
		return nil, err
	}
	var keys []gatewayroute.VerificationKey
	for _, caller := range config.Callers {
		keys = append(keys, f.keys[caller]...)
	}
	hash, err := gatewayroute.StateHash(normalized.RouteHash, compiled.Directory.Version, keys)
	if err != nil {
		return nil, err
	}
	snapshot := &adminpb.HostSnapshot{HostId: hostID, Hash: hash, Disabled: config.Disabled, Directory: gatewayclient.DirectoryToProto(compiled.Directory)}
	for _, route := range normalized.Routes {
		snapshot.Routes = append(snapshot.Routes, &adminpb.HostRoute{ComponentId: route.ServiceID, ServicePath: route.ServicePath, Address: route.Address,
			TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes, Methods: route.AllowedMethods, Callers: route.AllowedCallers})
	}
	for _, key := range keys {
		snapshot.Keys = append(snapshot.Keys, &adminpb.VerificationKey{KeyId: key.KeyID, Caller: key.Caller, Secret: key.Secret})
	}
	return snapshot, nil
}

func (f *fakeControl) update(change func(*fakeControl)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeControl) instanceIDs(hostID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, report := range f.reports {
		if report.GetHostId() == hostID && !seen[report.GetInstanceId()] {
			seen[report.GetInstanceId()] = true
			out = append(out, report.GetInstanceId())
		}
	}
	return out
}

func setPlacement(d *servicecatalog.Deployment, host, component string, enabled bool) {
	for i := range d.Placements {
		if d.Placements[i].HostID == host && d.Placements[i].ComponentID == component {
			d.Placements[i].Enabled = enabled
			return
		}
	}
	d.Placements = append(d.Placements, servicecatalog.Placement{HostID: host, ComponentID: component, Enabled: enabled})
}

// ---------- 上游服务 ----------

type upstream struct {
	mu    sync.Mutex
	calls []upstreamCall
}

type upstreamCall struct {
	rpcName, caller string
	body            []byte
	serialization   int
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *upstream) last(t *testing.T) upstreamCall {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	require.NotEmpty(t, u.calls)
	return u.calls[len(u.calls)-1]
}

func startUpstream(t *testing.T, name string) (*upstream, string) {
	t.Helper()
	address := freeAddr(t)
	up := &upstream{}
	svc := server.New(server.WithAddress(address), server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName(name))
	desc := &server.ServiceDesc{ServiceName: name, HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
		req := &codec.Body{}
		filters, err := f(req)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, req, func(ctx context.Context, body interface{}) (interface{}, error) {
			msg := codec.Message(ctx)
			data := body.(*codec.Body).Data
			up.mu.Lock()
			up.calls = append(up.calls, upstreamCall{rpcName: msg.ServerRPCName(), caller: string(msg.ServerMetaData()[gatewayroute.MetadataVerifiedCaller]),
				body: append([]byte(nil), data...), serialization: msg.SerializationType()})
			up.mu.Unlock()
			return &codec.Body{Data: data}, nil
		})
	}}}}
	require.NoError(t, svc.Register(desc, struct{}{}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	return up, address
}

// ---------- 两台主机 ----------

type cluster struct {
	t          *testing.T
	dir        string
	ca         testCA
	control    *fakeControl
	controlRPC string
	gateways   map[string]*runningGateway
	configs    map[string]config.Config
	dataView   *upstream
	metadata   *upstream
	monitor    *upstream
	keys       map[string]gatewayauth.Credentials
}

type runningGateway struct {
	gateway *bootstrap.Gateway
	cancel  context.CancelFunc
	done    chan error
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "health-secret")
	dir := t.TempDir()
	c := &cluster{t: t, dir: dir, ca: newCA(t, dir, "moox-ca"), gateways: map[string]*runningGateway{}, configs: map[string]config.Config{},
		keys: map[string]gatewayauth.Credentials{}}
	// control 的主机网关直连网关控制，没有调用方密钥。
	for _, caller := range []string{"strategy", "console", "monitor", "host-gateway@storage"} {
		c.keys[caller] = gatewayauth.Credentials{KeyID: caller + "-1", Caller: caller, Secret: "secret-" + caller}
	}
	var dataViewAddress, metadataAddress, monitorAddress string
	c.dataView, dataViewAddress = startUpstream(t, dataView)
	c.metadata, metadataAddress = startUpstream(t, metadata)
	c.monitor, monitorAddress = startUpstream(t, monitorMgr)
	c.controlRPC = freeAddr(t)
	deployment := servicecatalog.Deployment{
		Hosts: []servicecatalog.Host{
			{ID: "control", Address: "10.0.0.1", Enabled: true},
			{ID: "storage", Address: "10.0.0.2", PrivateAddress: "192.168.0.2", Region: "ap-nanjing", Enabled: true},
		},
	}
	for _, component := range []string{"console-proxy", "web-host", "admin", "monitor", "strategy"} {
		setPlacement(&deployment, "control", component, true)
	}
	for _, component := range []string{"storage-primary", "storage-view"} {
		setPlacement(&deployment, "storage", component, true)
	}
	c.control = &fakeControl{
		deployment: deployment, keys: map[string][]gatewayroute.VerificationKey{},
		addresses: map[string]map[string]string{
			"control": {"trpc.moox.admin.GatewayControl": c.controlRPC, monitorMgr: monitorAddress},
			"storage": {dataView: dataViewAddress, metadata: metadataAddress},
		},
	}
	for _, caller := range []string{"strategy", "console", "host-gateway@storage"} {
		key := c.keys[caller]
		c.control.keys[caller] = []gatewayroute.VerificationKey{{KeyID: key.KeyID, Caller: key.Caller, Secret: key.Secret}}
	}
	svc := server.New(server.WithAddress(c.controlRPC), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.admin.GatewayControl"))
	adminpb.RegisterGatewayControlService(svc, c.control)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	waitListening(t, c.controlRPC)
	return c
}

func waitListening(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s 没有开始监听", address)
}

func (c *cluster) writeKey(hostID string) string {
	key := c.keys[servicecatalog.HostGatewayIdentity(hostID)]
	raw, err := gatewayauth.MarshalCallerKey(gatewayauth.CallerKey{Caller: key.Caller, KeyID: key.KeyID, Secret: key.Secret})
	require.NoError(c.t, err)
	path := filepath.Join(c.dir, hostID+"-caller.key")
	require.NoError(c.t, os.WriteFile(path, raw, 0o600))
	return path
}

func (c *cluster) start(hostID string) {
	c.t.Helper()
	cfg, ok := c.configs[hostID]
	if !ok {
		cfg.Host.ID = hostID
		cfg.Server.RemoteAddr, cfg.Server.LocalAddr, cfg.Server.HealthAddr = freeAddr(c.t), freeAddr(c.t), freeAddr(c.t)
		cfg.TLS.CertFile, cfg.TLS.KeyFile = c.ca.issue(c.t, c.dir, hostID)
		cfg.TLS.CAFile = c.ca.pemFile
		cfg.Control.Caller = servicecatalog.HostGatewayIdentity(hostID)
		cfg.Control.Target = c.controlRPC
		if hostID != servicecatalog.ControlHostID {
			// 两台主机在同一台机器上：storage 经 control 的跨主机入口（127.0.0.1 上的 TLS 端口）访问网关控制。
			cfg.Control.Target = c.configs[servicecatalog.ControlHostID].Server.RemoteAddr
			cfg.Control.KeyFile = c.writeKey(hostID)
		}
		cfg.Store.Path = filepath.Join(c.dir, hostID+"-store")
		require.NoError(c.t, config.Validate(cfg))
		c.configs[hostID] = cfg
	}
	ctx, cancel := context.WithCancel(context.Background())
	gateway, err := bootstrap.NewGateway(ctx, cfg, "e2e")
	require.NoError(c.t, err)
	running := &runningGateway{gateway: gateway, cancel: cancel, done: make(chan error, 1)}
	go func() { running.done <- gateway.Serve(ctx) }()
	c.gateways[hostID] = running
	waitListening(c.t, cfg.Server.LocalAddr)
	waitListening(c.t, cfg.Server.RemoteAddr)
	c.t.Cleanup(func() { c.stop(hostID) })
}

func (c *cluster) stop(hostID string) {
	running, ok := c.gateways[hostID]
	if !ok {
		return
	}
	delete(c.gateways, hostID)
	running.cancel()
	<-running.done
	running.gateway.Close()
}

func (c *cluster) refresh(hosts ...string) {
	c.t.Helper()
	for _, host := range hosts {
		require.NoError(c.t, c.gateways[host].gateway.Runtime.Refresh(context.Background()), host)
	}
}

// client 返回一个运行在 hostID 上、以 caller 身份调用的内部方式客户端。
func (c *cluster) client(hostID, caller, caFile string) *gatewayclient.Client {
	c.t.Helper()
	credentials := c.keys[caller]
	remote := map[string]string{}
	for host, cfg := range c.configs {
		remote[host] = cfg.Server.RemoteAddr
	}
	gc, err := gatewayclient.New(gatewayclient.Options{
		Config: gatewayclient.Config{Mode: gatewayclient.ModeLocal, Caller: caller, CAFile: caFile,
			CacheDir: filepath.Join(c.dir, "client-"+hostID+"-"+caller+"-"+fmt.Sprint(time.Now().UnixNano())), LocalAddress: c.configs[hostID].Server.LocalAddr},
		Credentials: &credentials, RefreshInterval: time.Hour,
		RemoteAddress: func(_, target servicecatalog.DirectoryHost) string { return remote[target.ID] },
	})
	require.NoError(c.t, err)
	c.t.Cleanup(gc.Close)
	return gc
}

// ---------- 测试 ----------

func TestTwoHostGatewaysEndToEnd(t *testing.T) {
	c := newCluster(t)
	c.start("control")
	c.start("storage")
	c.refresh("control")

	strategy := c.client("control", "strategy", c.ca.pemFile)
	ctx := context.Background()

	t.Run("跨主机 TLS 的对象调用", func(t *testing.T) {
		req := &directorypb.GetDirectoryReq{CurrentVersion: "对象调用"}
		rsp := &directorypb.GetDirectoryReq{}
		require.NoError(t, strategy.Invoke(ctx, dataView, "QueryTimeSeriesRows", req, rsp))
		require.Equal(t, "对象调用", rsp.GetCurrentVersion())
		call := c.dataView.last(t)
		want, _ := proto.Marshal(req)
		require.Equal(t, want, call.body)
		require.Equal(t, "strategy", call.caller, "上游看到经网关校验的调用方")
		require.Equal(t, codec.SerializationTypePB, call.serialization)
	})

	t.Run("跨主机的 PB 原始字节转发", func(t *testing.T) {
		raw := []byte{0x0a, 0x03, 'a', 'b', 'c'}
		out, err := strategy.Forward(ctx, metadata, "ListDatasets", codec.SerializationTypePB, raw)
		require.NoError(t, err)
		require.Equal(t, raw, out)
		require.Equal(t, raw, c.metadata.last(t).body)
	})

	t.Run("本机的 JSON 原始字节转发", func(t *testing.T) {
		console := c.client("control", "console", c.ca.pemFile)
		body := []byte(`{"space_id":"s1"}`)
		out, err := console.Forward(ctx, monitorMgr, "GetHealthOverview", codec.SerializationTypeJSON, body)
		require.NoError(t, err)
		require.Equal(t, body, out)
		call := c.monitor.last(t)
		require.Equal(t, body, call.body)
		require.Equal(t, codec.SerializationTypeJSON, call.serialization)
		require.Equal(t, "console", call.caller)
	})

	t.Run("ACL 只放行目录中的调用方", func(t *testing.T) {
		console := c.client("control", "console", c.ca.pemFile)
		_, err := console.Forward(ctx, metadata, "ApplyTagSnapshot", codec.SerializationTypeJSON, []byte(`{}`))
		require.Equal(t, gatewayroute.RetForbidden, int(errs.Code(err)))
	})

	t.Run("不是 MooX 私有 CA 签发的证书被拒绝", func(t *testing.T) {
		other := newCA(t, c.dir, "other-ca")
		untrusting := c.client("control", "strategy", other.pemFile)
		before := c.dataView.count()
		_, err := untrusting.Forward(ctx, dataView, "QueryTimeSeriesRows", codec.SerializationTypeJSON, []byte(`{}`))
		require.Error(t, err)
		require.Equal(t, before, c.dataView.count())
	})

	t.Run("明文连接跨主机入口被拒绝", func(t *testing.T) {
		before := c.dataView.count()
		_, err := plainCall(c.configs["storage"].Server.RemoteAddr, c.keys["strategy"], "storage", dataView, "QueryTimeSeriesRows", []byte(`{}`), nil)
		require.Error(t, err)
		require.Equal(t, before, c.dataView.count())
	})

	t.Run("重放请求被拒绝", func(t *testing.T) {
		headers, err := plainCall(c.configs["control"].Server.LocalAddr, c.keys["console"], "control", monitorMgr, "ListHostAgents", []byte(`{}`), nil)
		require.NoError(t, err)
		_, err = plainCall(c.configs["control"].Server.LocalAddr, c.keys["console"], "control", monitorMgr, "ListHostAgents", []byte(`{}`), headers)
		require.Equal(t, gatewayroute.RetUnauthenticated, int(errs.Code(err)))
	})

	t.Run("新增调用方后下一次快照即可通过校验", func(t *testing.T) {
		monitor := c.client("control", "monitor", c.ca.pemFile)
		_, err := monitor.Forward(ctx, dataView, "QueryTimeSeriesRows", codec.SerializationTypeJSON, []byte(`{}`))
		require.Equal(t, gatewayroute.RetUnauthenticated, int(errs.Code(err)), "还没有 monitor 的校验密钥")
		key := c.keys["monitor"]
		c.control.update(func(f *fakeControl) {
			f.keys["monitor"] = []gatewayroute.VerificationKey{{KeyID: key.KeyID, Caller: key.Caller, Secret: key.Secret}}
		})
		c.refresh("storage")
		_, err = monitor.Forward(ctx, dataView, "QueryTimeSeriesRows", codec.SerializationTypeJSON, []byte(`{}`))
		require.NoError(t, err, "不需要重启网关")
	})

	t.Run("停用部署后调用方收到明确的错误，目录变更后自动切换", func(t *testing.T) {
		c.control.update(func(f *fakeControl) { setPlacement(&f.deployment, "storage", "storage-view", false) })
		c.refresh("storage", "control")
		_, err := strategy.Forward(ctx, dataView, "QueryTimeSeriesRows", codec.SerializationTypeJSON, []byte(`{}`))
		require.Equal(t, gatewayroute.RetServiceNotHere, int(errs.Code(err)))

		// storage-view 改到 control：客户端刷新目录后改走本机。
		_, dataViewOnControl := startUpstream(t, dataView)
		c.control.update(func(f *fakeControl) {
			setPlacement(&f.deployment, "control", "storage-view", true)
			f.addresses["control"][dataView] = dataViewOnControl
		})
		c.refresh("control", "storage")
		_, err = strategy.Forward(ctx, dataView, "QueryTimeSeriesRows", codec.SerializationTypeJSON, []byte(`{"moved":true}`))
		require.NoError(t, err, "只读方法刷新目录后重试到新目标")
		view, err := strategy.Directory(ctx)
		require.NoError(t, err)
		require.Equal(t, []string{"control"}, view.Directory.ServiceHostIDs(dataView))
	})

	t.Run("网关控制不可用时继续使用缓存的路由和密钥", func(t *testing.T) {
		c.control.update(func(f *fakeControl) { f.down = true })
		require.Error(t, c.gateways["storage"].gateway.Runtime.Refresh(ctx))
		_, err := strategy.Forward(ctx, metadata, "ListDatasets", codec.SerializationTypeJSON, []byte(`{}`))
		require.NoError(t, err)
		c.gateways["storage"].gateway.State.SetRouteSyncStaleAfter(time.Second)
		time.Sleep(2100 * time.Millisecond)
		require.False(t, c.gateways["storage"].gateway.State.Ready(), "超过就绪窗口后未就绪")
		c.control.update(func(f *fakeControl) { f.down = false })
		c.refresh("storage")
		require.True(t, c.gateways["storage"].gateway.State.Ready())
	})

	t.Run("重启网关进程是一次实例替换", func(t *testing.T) {
		first := c.control.instanceIDs("storage")
		require.Len(t, first, 1)
		c.stop("storage")
		c.start("storage")
		ids := c.control.instanceIDs("storage")
		require.Len(t, ids, 2, "重启后上报新的实例 ID，由网关控制记为替换")
		sort.Strings(ids)
		require.NotEqual(t, ids[0], ids[1])
		_, err := strategy.Forward(ctx, metadata, "ListDatasets", codec.SerializationTypeJSON, []byte(`{}`))
		require.NoError(t, err)
	})

	t.Run("只有同一主机的主机网关能拉取快照", func(t *testing.T) {
		c.control.mu.Lock()
		callers := append([]string(nil), c.control.pullCallers...)
		c.control.mu.Unlock()
		require.Contains(t, callers, "host-gateway@storage", "storage 经 control 的跨主机入口拉取，身份由网关写入")
		require.Contains(t, callers, "host-gateway@control")
	})
}

// plainCall 用明文 tRPC 直接签名调用网关；replay 不为空时复用给定的签名头。
func plainCall(address string, credentials gatewayauth.Credentials, targetNode, servicePath, method string, body []byte, replay map[string]string) (map[string]string, error) {
	rpcName := "/" + servicePath + "/" + method
	headers := replay
	if headers == nil {
		signed, err := gatewayauth.Sign(credentials, gatewayauth.Request{Method: http.MethodPost, Path: rpcName, TargetNode: targetNode, Callee: servicePath, Func: method, Body: body}, time.Now())
		if err != nil {
			return nil, err
		}
		headers = map[string]string{}
		for key, values := range signed {
			headers[key] = values[0]
		}
	}
	// 单独的连接池：默认连接池不区分明文与 TLS，可能复用其他客户端到同一地址的 TLS 连接。
	options := []client.Option{client.WithTarget("ip://" + address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(servicePath), client.WithCalleeMethod(method), client.WithSerializationType(codec.SerializationTypeJSON),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop), client.WithTimeout(3 * time.Second),
		client.WithPool(connpool.NewConnectionPool())}
	for key, value := range headers {
		options = append(options, client.WithMetaData(key, []byte(value)))
	}
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithClientRPCName(rpcName)
	return headers, client.New().Invoke(ctx, &codec.Body{Data: body}, &codec.Body{}, options...)
}
