package gatewayclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/gatewayroute/proto/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

var testCredentials = gatewayauth.Credentials{KeyID: "collector-1", Caller: "collector", Secret: "test-secret"}

const (
	metadataPath = "trpc.moox.storage.Metadata"
	// stubService 用 Directory 的生成桩验证 ClientOptions 路径；测试目录把它放在 storage。
	stubService = "trpc.moox.hostgateway.Directory"
)

// received 记录假网关收到的一次调用。
type received struct {
	rpcName       string
	caller        string
	body          []byte
	serialization int
	metadata      map[string]string
}

// fakeGateway 是一个验证签名的假主机网关：本机入口同时提供 Directory 服务。
type fakeGateway struct {
	hostID   string
	address  string
	mu       sync.Mutex
	calls    []received
	respond  func(rpcName string, req []byte) ([]byte, error)
	notHere  atomic.Bool
	dirMu    sync.Mutex
	view     *directorypb.GetDirectoryRsp
	dirCalls atomic.Int32
}

func (g *fakeGateway) lastCall(t *testing.T) received {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.calls) == 0 {
		t.Fatal("假网关没有收到调用")
	}
	return g.calls[len(g.calls)-1]
}

func (g *fakeGateway) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *fakeGateway) handle(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
	request := &codec.Body{}
	filters, err := f(request)
	if err != nil {
		return nil, err
	}
	return filters.Filter(ctx, request, func(ctx context.Context, body interface{}) (interface{}, error) {
		req := body.(*codec.Body)
		msg := codec.Message(ctx)
		rpcName := msg.ServerRPCName()
		servicePath, method, _ := splitRPCName(rpcName)
		header := http.Header{}
		metadata := map[string]string{}
		for key, value := range msg.ServerMetaData() {
			header.Add(key, string(value))
			metadata[key] = string(value)
		}
		claims, err := gatewayauth.Verify(testCredentials, gatewayauth.Request{
			Method: http.MethodPost, Path: rpcName, TargetNode: g.hostID, Callee: servicePath, Func: method, Body: req.Data,
		}, header, time.Now())
		if err != nil {
			return nil, errs.New(gatewayroute.RetUnauthenticated, err.Error())
		}
		g.mu.Lock()
		g.calls = append(g.calls, received{rpcName: rpcName, caller: claims.Caller, body: append([]byte(nil), req.Data...), serialization: msg.SerializationType(), metadata: metadata})
		g.mu.Unlock()
		if g.notHere.Load() {
			return nil, errs.New(gatewayroute.RetServiceNotHere, "服务不在本机")
		}
		if g.respond != nil {
			out, err := g.respond(rpcName, req.Data)
			if err != nil {
				return nil, err
			}
			return &codec.Body{Data: out}, nil
		}
		return &codec.Body{Data: req.Data}, nil
	})
}

func (g *fakeGateway) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	g.dirCalls.Add(1)
	g.dirMu.Lock()
	defer g.dirMu.Unlock()
	if g.view == nil {
		return &directorypb.GetDirectoryRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "没有快照"}}, nil
	}
	if req.GetCurrentVersion() == g.view.GetDirectory().GetVersion() {
		return &directorypb.GetDirectoryRsp{RetInfo: &commonpb.RetInfo{}, HostId: g.hostID}, nil
	}
	return proto.Clone(g.view).(*directorypb.GetDirectoryRsp), nil
}

func (g *fakeGateway) setDirectory(directory servicecatalog.Directory) {
	g.dirMu.Lock()
	defer g.dirMu.Unlock()
	g.view = &directorypb.GetDirectoryRsp{RetInfo: &commonpb.RetInfo{}, Changed: true, HostId: g.hostID, Directory: DirectoryToProto(directory)}
}

// startGateway 启动假网关；tls 不为空时以 TLS 监听（模拟 11003），否则同时注册 Directory（模拟 11002）。
func startGateway(t *testing.T, hostID string, tlsFiles *tlsFiles) *fakeGateway {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	gateway := &fakeGateway{hostID: hostID, address: address}
	options := []server.Option{
		server.WithAddress(address), server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName("trpc.moox.hostgateway.Local"),
	}
	if tlsFiles != nil {
		options = append(options, server.WithTLS(tlsFiles.cert, tlsFiles.key, ""))
	}
	service := server.New(options...)
	proxy := &server.ServiceDesc{ServiceName: "trpc.moox.hostgateway.Local", HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: gateway.handle}}}
	if err := service.Register(proxy, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if tlsFiles == nil {
		if err := service.Register(directorypb.NoopServiceDesc(), directorypb.DirectoryService(gateway)); err != nil {
			t.Fatal(err)
		}
	}
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	waitListening(t, address)
	return gateway
}

func waitListening(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s 没有开始监听", address)
}

type tlsFiles struct{ ca, cert, key string }

// newTLSFiles 生成一个 CA 和签给 hostID 的服务端证书。
func newTLSFiles(t *testing.T, hostID string) tlsFiles {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX 测试 CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: hostID}, DNSNames: []string{hostID},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	files := tlsFiles{ca: filepath.Join(dir, "ca.crt"), cert: filepath.Join(dir, "server.crt"), key: filepath.Join(dir, "server.key")}
	writePEM(t, files.ca, "CERTIFICATE", caDER)
	writePEM(t, files.cert, "CERTIFICATE", serverDER)
	writePEM(t, files.key, "EC PRIVATE KEY", keyDER)
	return files
}

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// twoHosts 模拟 control（本机）和 storage（远端，TLS）两台主机。
type twoHosts struct {
	local, remote *fakeGateway
	tls           tlsFiles
	directory     servicecatalog.Directory
}

func newTwoHosts(t *testing.T) *twoHosts {
	t.Helper()
	files := newTLSFiles(t, "storage")
	hosts := &twoHosts{local: startGateway(t, "control", nil), remote: startGateway(t, "storage", &files), tls: files}
	hosts.directory = servicecatalog.Directory{
		Version: "v1",
		Services: []servicecatalog.ServiceHosts{
			{Path: metadataPath, HostIDs: []string{"storage"}},
			{Path: "trpc.moox.collector.CollectMgr", HostIDs: []string{"control"}},
			{Path: "trpc.moox.hostagent.HostAgentMgr", HostIDs: []string{"control", "storage"}},
			{Path: stubService, HostIDs: []string{"storage"}},
		},
		Hosts: []servicecatalog.DirectoryHost{
			{ID: "control", Address: "127.0.0.1"},
			{ID: "storage", Address: "127.0.0.2"},
		},
	}
	hosts.local.setDirectory(hosts.directory)
	return hosts
}

func (h *twoHosts) client(t *testing.T, caFile string) *Client {
	t.Helper()
	credentials := testCredentials
	remote := h.remote.address
	c, err := New(Options{
		Config:      Config{Mode: ModeLocal, Caller: "collector", CAFile: caFile, CacheDir: t.TempDir(), LocalAddress: h.local.address},
		Credentials: &credentials, RefreshInterval: time.Hour,
		RemoteAddress: func(_, target servicecatalog.DirectoryHost) string {
			if target.ID == "storage" {
				return remote
			}
			return "127.0.0.1:1"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestInvokeSignsTheBytesItSends(t *testing.T) {
	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	req := &directorypb.GetDirectoryReq{CurrentVersion: "版本-1"}
	rsp := &directorypb.GetDirectoryReq{}
	if err := c.Invoke(context.Background(), "trpc.moox.collector.CollectMgr", "GetTaskList", req, rsp); err != nil {
		t.Fatal(err)
	}
	call := hosts.local.lastCall(t)
	want, _ := proto.Marshal(req)
	if string(call.body) != string(want) || call.serialization != codec.SerializationTypePB {
		t.Fatalf("网关收到的字节或序列化类型不对: %x %d", call.body, call.serialization)
	}
	if call.caller != "collector" || call.rpcName != "/trpc.moox.collector.CollectMgr/GetTaskList" {
		t.Fatalf("调用信息不对: %+v", call)
	}
	if rsp.GetCurrentVersion() != "版本-1" {
		t.Fatalf("响应解析不对: %+v", rsp)
	}
}

func TestForwardSendsRawBytesUnchanged(t *testing.T) {
	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	jsonBody := []byte(`{"space_id":"s1","page":{"page":1}}`)
	out, err := c.Forward(context.Background(), metadataPath, "ListDatasets", codec.SerializationTypeJSON, jsonBody, WithMetadata("x-space-id", []byte("s1")), WithMetadata("x-moox-caller", []byte("forged")))
	if err != nil {
		t.Fatal(err)
	}
	call := hosts.remote.lastCall(t)
	if string(call.body) != string(jsonBody) || string(out) != string(jsonBody) || call.serialization != codec.SerializationTypeJSON {
		t.Fatalf("JSON 透传后字节变化: %s / %s (%d)", call.body, out, call.serialization)
	}
	if call.metadata["x-space-id"] != "s1" {
		t.Fatalf("透传元数据丢失: %v", call.metadata)
	}
	if call.metadata["X-Moox-Caller"] != "collector" {
		t.Fatalf("调用方伪造的 x-moox- 元数据不应发出: %v", call.metadata)
	}
	pbBody, _ := proto.Marshal(&directorypb.GetDirectoryReq{CurrentVersion: "x"})
	out, err = c.Forward(context.Background(), metadataPath, "GetDataset", codec.SerializationTypePB, pbBody)
	if err != nil {
		t.Fatal(err)
	}
	if call := hosts.remote.lastCall(t); string(call.body) != string(pbBody) || string(out) != string(pbBody) {
		t.Fatal("PB 透传后字节变化")
	}
}

func TestGeneratedStubUsesClientOptions(t *testing.T) {
	hosts := newTwoHosts(t)
	hosts.remote.respond = func(rpcName string, _ []byte) ([]byte, error) {
		return proto.Marshal(&directorypb.GetDirectoryRsp{RetInfo: &commonpb.RetInfo{}, HostId: "来自 storage"})
	}
	c := hosts.client(t, hosts.tls.ca)
	stub := directorypb.NewDirectoryClientProxy(c.ClientOptions()...)
	rsp, err := stub.GetDirectory(context.Background(), &directorypb.GetDirectoryReq{CurrentVersion: "桩"})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetHostId() != "来自 storage" {
		t.Fatalf("响应 = %+v", rsp)
	}
	call := hosts.remote.lastCall(t)
	if call.rpcName != "/"+stubService+"/GetDirectory" {
		t.Fatalf("rpc = %s", call.rpcName)
	}
	want, _ := proto.Marshal(&directorypb.GetDirectoryReq{CurrentVersion: "桩"})
	if string(call.body) != string(want) {
		t.Fatal("桩发出的字节与签名的字节不一致")
	}
}

func TestRemoteCallRejectsUntrustedCertificate(t *testing.T) {
	hosts := newTwoHosts(t)
	other := newTLSFiles(t, "storage")
	c := hosts.client(t, other.ca)
	if _, err := c.Forward(context.Background(), metadataPath, "ListDatasets", codec.SerializationTypeJSON, []byte(`{}`)); err == nil {
		t.Fatal("不是由信任的 CA 签发的证书应当被拒绝")
	}
	if hosts.remote.callCount() != 0 {
		t.Fatal("TLS 握手失败时请求不应到达网关")
	}
}

func TestWithHostSelectsPerHostService(t *testing.T) {
	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	if _, err := c.Forward(context.Background(), "trpc.moox.hostagent.HostAgentMgr", "GetStatus", codec.SerializationTypeJSON, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if hosts.local.callCount() != 1 || hosts.remote.callCount() != 0 {
		t.Fatal("默认应当优先本机")
	}
	if _, err := c.Forward(context.Background(), "trpc.moox.hostagent.HostAgentMgr", "GetStatus", codec.SerializationTypeJSON, []byte(`{}`), WithHost("storage")); err != nil {
		t.Fatal(err)
	}
	if hosts.remote.callCount() != 1 {
		t.Fatal("WithHost 应当发往指定主机")
	}
	_, err := c.Forward(context.Background(), "trpc.moox.collector.CollectMgr", "GetTaskList", codec.SerializationTypeJSON, []byte(`{}`), WithHost("storage"))
	if errs.Code(err) != gatewayroute.RetServiceNotHere {
		t.Fatalf("服务不在指定主机时应当明确报错，实际 %v", err)
	}
}

func TestDirectoryChangeSwitchesTargetAndRetriesReadOnly(t *testing.T) {
	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	if _, err := c.Forward(context.Background(), metadataPath, "ListDatasets", codec.SerializationTypeJSON, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	// Metadata 搬到 control：storage 的网关开始返回「服务不在本机」。
	moved := hosts.directory
	moved.Version = "v2"
	moved.Services = append([]servicecatalog.ServiceHosts(nil), moved.Services...)
	moved.Services[0] = servicecatalog.ServiceHosts{Path: metadataPath, HostIDs: []string{"control"}}
	hosts.local.setDirectory(moved)
	hosts.remote.notHere.Store(true)

	// 只读方法：刷新目录后立即重试到新目标。
	if _, err := c.Forward(context.Background(), metadataPath, "ListDatasets", codec.SerializationTypeJSON, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("只读方法应当在刷新目录后重试成功: %v", err)
	}
	if call := hosts.local.lastCall(t); call.rpcName != "/"+metadataPath+"/ListDatasets" {
		t.Fatalf("重试应当发往 control: %+v", call)
	}
	// 目录已经刷新，写方法直接发往新目标。
	before := hosts.remote.callCount()
	if _, err := c.Forward(context.Background(), metadataPath, "CreateDataset", codec.SerializationTypeJSON, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if hosts.remote.callCount() != before {
		t.Fatal("目录刷新后不应再发往旧目标")
	}
}

func TestMissingServiceRefreshesDirectoryBeforeSending(t *testing.T) {
	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	// 先让客户端拿到不含 FactorMgr 的目录。
	if _, err := c.Forward(context.Background(), metadataPath, "ListDatasets", codec.SerializationTypeJSON, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	// FactorMgr 新部署在 control：本地目录还没有它，写方法也应当先刷新目录再发送。
	added := hosts.directory
	added.Version = "v2"
	added.Services = append(append([]servicecatalog.ServiceHosts(nil), added.Services...), servicecatalog.ServiceHosts{Path: "trpc.moox.factor.FactorMgr", HostIDs: []string{"control"}})
	hosts.local.setDirectory(added)
	if _, err := c.Forward(context.Background(), "trpc.moox.factor.FactorMgr", "CreateFactor", codec.SerializationTypeJSON, []byte(`{}`)); err != nil {
		t.Fatalf("目录里没有的服务应当刷新目录后再选路: %v", err)
	}
	if call := hosts.local.lastCall(t); call.rpcName != "/trpc.moox.factor.FactorMgr/CreateFactor" {
		t.Fatalf("应当发往 control: %+v", call)
	}
	// 刷新后仍然没有部署时返回明确的错误，请求不会发出。
	before := hosts.local.callCount() + hosts.remote.callCount()
	_, err := c.Forward(context.Background(), "trpc.moox.strategy.StrategyMgr", "ListStrategies", codec.SerializationTypeJSON, []byte(`{}`))
	if errs.Code(err) != gatewayroute.RetServiceNotHere {
		t.Fatalf("没有部署的服务应当返回 4404，实际 %v", err)
	}
	if hosts.local.callCount()+hosts.remote.callCount() != before {
		t.Fatal("没有部署的服务不应发出请求")
	}
}

func TestResolvePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Config{KeyFile: "~/.config/moox/factor-engine.key", CAFile: "../certs/moox-ca.crt", CacheDir: "/var/cache/moox"}.ResolvePaths("/opt/moox/factor-mgr")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.KeyFile != filepath.Join(home, ".config/moox/factor-engine.key") || resolved.CAFile != "/opt/moox/certs/moox-ca.crt" || resolved.CacheDir != "/var/cache/moox" {
		t.Fatalf("resolved = %+v", resolved)
	}
	unchanged, err := Config{CAFile: "../certs/moox-ca.crt"}.ResolvePaths("")
	if err != nil || unchanged.CAFile != "../certs/moox-ca.crt" {
		t.Fatalf("没有 base 时相对路径保持不变: %+v %v", unchanged, err)
	}
}

func TestWriteMethodIsNotRetried(t *testing.T) {
	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	hosts.remote.notHere.Store(true)
	_, err := c.Forward(context.Background(), metadataPath, "CreateDataset", codec.SerializationTypeJSON, []byte(`{}`))
	if errs.Code(err) != gatewayroute.RetServiceNotHere {
		t.Fatalf("写方法应当直接返回明确的错误，实际 %v", err)
	}
	if hosts.remote.callCount() != 1 {
		t.Fatalf("写方法不能重试，实际发送 %d 次", hosts.remote.callCount())
	}
}

func TestDirectoryCacheSurvivesGatewayOutage(t *testing.T) {
	hosts := newTwoHosts(t)
	cacheDir := t.TempDir()
	credentials := testCredentials
	config := Config{Mode: ModeLocal, Caller: "collector", CAFile: hosts.tls.ca, CacheDir: cacheDir, LocalAddress: hosts.local.address}
	first, err := New(Options{Config: config, Credentials: &credentials, RefreshInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Directory(context.Background()); err != nil {
		t.Fatal(err)
	}
	first.Close()
	if info, err := os.Stat(filepath.Join(cacheDir, directoryCacheFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("目录缓存应当以 0600 落盘: %v %v", info, err)
	}
	// 本机网关不可用：新进程从缓存启动。
	config.LocalAddress = "127.0.0.1:1"
	second, err := New(Options{Config: config, Credentials: &credentials, RefreshInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	view, err := second.Directory(context.Background())
	if err != nil || view.Directory.Version != "v1" || view.LocalHostID != "control" {
		t.Fatalf("应当使用落盘缓存: %+v %v", view, err)
	}
}

func TestBackgroundRefreshPicksUpNewVersion(t *testing.T) {
	hosts := newTwoHosts(t)
	credentials := testCredentials
	c, err := New(Options{
		Config:      Config{Mode: ModeLocal, Caller: "collector", CAFile: hosts.tls.ca, CacheDir: t.TempDir(), LocalAddress: hosts.local.address},
		Credentials: &credentials, RefreshInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Directory(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := hosts.directory
	next.Version = "v9"
	hosts.local.setDirectory(next)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, _ := c.Directory(context.Background())
		if view.Directory.Version == "v9" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("后台刷新没有拿到新版本")
}

func TestAccessModeTargetsFixedAccess(t *testing.T) {
	access := startGateway(t, "access@storage", nil)
	credentials := testCredentials
	c, err := New(Options{Config: Config{Mode: ModeAccess, Caller: "collector", AccessAddress: access.address, AccessID: "access@storage"}, Credentials: &credentials})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	body := []byte(`{"x":1}`)
	if _, err := c.Forward(context.Background(), metadataPath, "ListSubjects", codec.SerializationTypeJSON, body); err != nil {
		t.Fatal(err)
	}
	if call := access.lastCall(t); string(call.body) != string(body) {
		t.Fatal("外部方式应当原样发往外部接入")
	}
	if _, err := c.Directory(context.Background()); err == nil {
		t.Fatal("外部方式没有服务目录")
	}
}

type mapTunnel map[string]string

func (m mapTunnel) Address(_ context.Context, hostID string) (string, error) {
	address, ok := m[hostID]
	if !ok {
		return "", errors.New("没有隧道")
	}
	return address, nil
}

func TestTunnelModeUsesControlDirectoryAndPerHostTunnels(t *testing.T) {
	control := startGateway(t, "control", nil)
	storage := startGateway(t, "storage", nil)
	control.setDirectory(servicecatalog.Directory{
		Version:  "v1",
		Services: []servicecatalog.ServiceHosts{{Path: metadataPath, HostIDs: []string{"storage"}}},
		Hosts:    []servicecatalog.DirectoryHost{{ID: "control", Address: "1.1.1.1"}, {ID: "storage", Address: "2.2.2.2"}},
	})
	credentials := testCredentials
	c, err := New(Options{
		Config:      Config{Mode: ModeTunnel, Caller: "collector"},
		Credentials: &credentials, Tunnel: mapTunnel{"control": control.address, "storage": storage.address},
		RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Forward(context.Background(), metadataPath, "ListDatasets", codec.SerializationTypeJSON, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if storage.callCount() != 1 || control.callCount() != 0 {
		t.Fatal("隧道方式应当经隧道发往目录中的目标主机")
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		config Config
		want   string
	}{
		{"缺少调用方", Config{Mode: ModeLocal, CAFile: "ca", CacheDir: "dir"}, "caller"},
		{"内部方式缺少 CA", Config{Mode: ModeLocal, Caller: "collector", CacheDir: "dir"}, "ca_file"},
		{"内部方式缺少缓存目录", Config{Mode: ModeLocal, Caller: "collector", CAFile: "ca"}, "cache_dir"},
		{"内部方式本机入口不是回环地址", Config{Mode: ModeLocal, Caller: "collector", CAFile: "ca", CacheDir: "dir", LocalAddress: "10.0.0.1:11002"}, "回环"},
		{"内部方式不能配外部接入", Config{Mode: ModeLocal, Caller: "collector", CAFile: "ca", CacheDir: "dir", AccessID: "access@storage"}, "access_address"},
		{"外部方式缺少地址", Config{Mode: ModeAccess, Caller: "factor-engine", AccessID: "access@storage"}, "access_address"},
		{"外部方式实例 ID 非法", Config{Mode: ModeAccess, Caller: "factor-engine", AccessAddress: "1.2.3.4:11004", AccessID: "storage-access"}, "access@"},
		{"未知方式", Config{Mode: "http", Caller: "collector"}, "mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望报错包含 %q，实际 %v", tc.want, err)
			}
		})
	}
	valid := []Config{
		{Mode: ModeLocal, Caller: "collector", CAFile: "ca", CacheDir: "dir"},
		{Mode: ModeAccess, Caller: "factor-engine", AccessAddress: "146.56.196.204:11004", AccessID: "access@storage"},
		{Mode: ModeTunnel, Caller: "moox-cli"},
	}
	for _, config := range valid {
		if err := config.Validate(); err != nil {
			t.Fatalf("%+v: %v", config, err)
		}
	}
}

func TestNewRejectsKeyForAnotherCaller(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "caller-monitor.key")
	raw, err := gatewayauth.MarshalCallerKey(gatewayauth.CallerKey{Caller: "monitor", KeyID: "monitor-1", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = New(Options{Config: Config{Mode: ModeAccess, Caller: "collector", KeyFile: path, AccessAddress: "127.0.0.1:1", AccessID: "access@storage"}})
	if err == nil || !strings.Contains(err.Error(), "monitor") {
		t.Fatalf("密钥属于其他调用方时应当报错，实际 %v", err)
	}
}

func TestAccessConfigFromEnv(t *testing.T) {
	t.Setenv(EnvAccessAddress, "10.206.0.5:11004")
	t.Setenv(EnvAccessID, "access@storage")
	t.Setenv(EnvCaller, "scf-collector")
	t.Setenv(EnvCallerKey, "scf-collector-1:secret")
	config, credentials, err := AccessConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != ModeAccess || credentials.KeyID != "scf-collector-1" || credentials.Caller != "scf-collector" {
		t.Fatalf("%+v %+v", config, credentials)
	}
	t.Setenv(EnvCallerKey, "no-separator")
	if _, _, err := AccessConfigFromEnv(); err == nil {
		t.Fatal("密钥格式错误应当报错")
	}
}

func TestRemoteAddressPrefersPrivateNetworkInSameRegion(t *testing.T) {
	storage := servicecatalog.DirectoryHost{ID: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Region: "ap-nanjing"}
	sameRegion := servicecatalog.DirectoryHost{ID: "x", Address: "1.1.1.1", PrivateAddress: "10.206.0.9", Region: "ap-nanjing"}
	control := servicecatalog.DirectoryHost{ID: "control", Address: "106.53.107.122"}
	if got := defaultRemoteAddress(sameRegion, storage); got != "10.206.0.5:11003" {
		t.Fatalf("同地域应当走私网: %s", got)
	}
	if got := defaultRemoteAddress(control, storage); got != "146.56.196.204:11003" {
		t.Fatalf("跨地域应当走公网: %s", got)
	}
}
