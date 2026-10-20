package relay_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/listener"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/relay"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	protocol "github.com/mooyang-code/moox/packages/gatewayclient/relay"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type memoryNonces struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (n *memoryNonces) Consume(_ context.Context, namespace, nonce string, _ time.Duration) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	key := namespace + "/" + nonce
	if n.seen[key] {
		return false, nil
	}
	n.seen[key] = true
	return true, nil
}

type harness struct {
	t       *testing.T
	ca      *testcert.Authority
	state   *snapshot.State
	raw     *pb.HostGatewaySnapshot
	server  *relay.Server
	address string
	roots   *x509.CertPool
	echo    string
	// plain 收到不带 ALPN 的普通连接（应交给 tRPC）。
	plain chan net.Conn
}

func clientAuth(cert *x509.Certificate) {
	cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
}

func freeAddress(t *testing.T) string {
	opened, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := opened.Addr().String()
	require.NoError(t, opened.Close())
	return address
}

// newHarness 启动 control 的跨主机监听（含中转端）与一个模拟出站主机本机服务的回显端口。
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, ca: testcert.New(t, nil), state: &snapshot.State{}, plain: make(chan net.Conn, 4)}
	h.raw = testsnapshot.NewWithOutbound(t, "control", "home")
	view, err := snapshot.Build("control", proto.Clone(h.raw).(*pb.HostGatewaySnapshot))
	require.NoError(t, err)
	h.state.Apply(view)
	cfg := hostgatewayconfig.Default("control", "control", "127.0.0.1", "fixture-key")
	cfg.TLS, cfg.Store.Path = testcert.Files(t, h.ca, "control", nil), filepath.Join(t.TempDir(), "cache")
	cfg.Server.RemoteAddr, cfg.Server.LocalAddr, cfg.Server.HealthAddr = freeAddress(t), freeAddress(t), freeAddress(t)
	material, err := tlsconfig.Load("control", cfg.TLS)
	require.NoError(t, err)
	opened, err := listener.Open(t.Context(), cfg, material)
	require.NoError(t, err)
	t.Cleanup(func() { _ = opened.Close() })
	h.server, err = relay.NewServer(relay.ServerOptions{State: h.state, Nonces: &memoryNonces{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.server.Close() })
	opened.SetRelay(h.server)
	go func() {
		for {
			conn, err := opened.Remote.Accept()
			if err != nil {
				return
			}
			h.plain <- conn
		}
	}()
	h.address = opened.Remote.Addr().String()
	h.roots = x509.NewCertPool()
	h.roots.AddCert(h.ca.Certificate)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	h.echo = echo.Addr().String()
	return h
}

// outbound 构造出站主机的反向连接端：证书主机、签名身份可分别指定以覆盖拒绝场景。
func (h *harness) outbound(certHost, caller string, mutate func(*x509.Certificate)) *relay.Client {
	h.t.Helper()
	paths := testcert.Files(h.t, h.ca, certHost, mutate)
	material, err := tlsconfig.Load(certHost, paths)
	require.NoError(h.t, err)
	config, err := material.Client("control")
	require.NoError(h.t, err)
	pair, err := tls.LoadX509KeyPair(paths.CertificateFile, paths.KeyFile)
	require.NoError(h.t, err)
	config.Certificates = []tls.Certificate{pair}
	client, err := relay.NewClient(relay.ClientOptions{
		HostID: "home", ControlHost: "control", Target: h.address, TLS: config, Credentials: testsnapshot.Credential(caller),
		Active:     func() bool { return true },
		Allowed:    func(port int) bool { return port == servicecatalog.HostGatewayRemotePort },
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		DialLocal: func(int) (net.Conn, error) { return net.Dial("tcp", h.echo) },
	})
	require.NoError(h.t, err)
	return client
}

func (h *harness) status() relay.Status {
	for _, item := range h.server.Status() {
		if item.HostID == "home" {
			return item
		}
	}
	return relay.Status{}
}

func (h *harness) waitOnline(instance string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		status := h.status()
		return status.Online && (instance == "" || status.InstanceID == instance)
	}, 5*time.Second, 10*time.Millisecond)
}

func (h *harness) open(caller string, port int) (net.Conn, error) {
	return protocol.Dial(h.t.Context(), protocol.OpenOptions{
		Address: h.address, ControlHost: "control", TLS: &tls.Config{RootCAs: h.roots, MinVersion: tls.VersionTLS12},
		Credentials: testsnapshot.Credential(caller), Target: protocol.Open{HostID: "home", Port: port},
	})
}

func roundTrip(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	_, err := conn.Write([]byte("ping"))
	require.NoError(t, err)
	reply := make([]byte, 4)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = io.ReadFull(conn, reply)
	require.NoError(t, err)
	require.Equal(t, "ping", string(reply))
}

// 经中转往返：会话离线时立即失败；上线后允许的调用方能打开流；非清单端口、非清单调用方、重放都拒绝；
// 同端口上不带 ALPN 的普通连接照常交给 tRPC。
func TestRelayOpenStreamAllowlistAndReplay(t *testing.T) {
	h := newHarness(t)
	require.Equal(t, []relay.Status{{HostID: "home"}}, h.server.Status(), "从未接入的出站主机也要上报为离线")
	_, err := h.open("monitor", servicecatalog.HostGatewayRemotePort)
	require.ErrorContains(t, err, "offline")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.outbound("home", "host-gateway@home", clientAuth).Run(ctx)
	h.waitOnline("")

	conn, err := h.open("monitor", servicecatalog.HostGatewayRemotePort)
	require.NoError(t, err)
	roundTrip(t, conn)

	_, err = h.open("monitor", 22)
	require.ErrorContains(t, err, "not allowed")
	_, err = h.open("host-gateway@control", servicecatalog.HostGatewayRemotePort)
	require.ErrorContains(t, err, "not allowed")
	view := h.state.Load()
	target, ok := view.Relay("home")
	require.True(t, ok)
	require.Greater(t, len(target.Ports), 1)
	health := target.Ports[1].Port
	_, err = h.open("console", health)
	require.ErrorContains(t, err, "not allowed", "健康端口只对 Monitor 开放")

	// 同一份签名前导重放：第二次被拒绝。
	signed, err := protocol.Sign(testsnapshot.Credential("monitor"), protocol.MethodOpen, "control", protocol.Open{HostID: "home", Port: servicecatalog.HostGatewayRemotePort}, time.Now())
	require.NoError(t, err)
	send := func() protocol.Reply {
		raw, err := tls.Dial("tcp", h.address, &tls.Config{RootCAs: h.roots, ServerName: "control", NextProtos: []string{protocol.ALPNOpen}})
		require.NoError(t, err)
		defer raw.Close()
		require.NoError(t, protocol.WriteFrame(raw, signed))
		var reply protocol.Reply
		require.NoError(t, protocol.ReadFrame(raw, &reply))
		return reply
	}
	require.Empty(t, send().Error)
	require.Contains(t, send().Error, "replayed")

	plain, err := tls.Dial("tcp", h.address, &tls.Config{RootCAs: h.roots, ServerName: "control"})
	require.NoError(t, err)
	defer plain.Close()
	select {
	case accepted := <-h.plain:
		_ = accepted.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("plain connection did not reach tRPC accept")
	}
}

// attach 拒绝：证书没有 ClientAuth、证书主机与声明不符、签名身份与主机不符、主机不是出站主机。
func TestRelayAttachRejections(t *testing.T) {
	h := newHarness(t)
	for name, client := range map[string]*relay.Client{
		"server-only certificate": h.outbound("home", "host-gateway@home", nil),
		"certificate for other":   h.outbound("other", "host-gateway@home", clientAuth),
		"caller for other host":   h.outbound("home", "host-gateway@control", clientAuth),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			client.Run(ctx)
			require.False(t, h.status().Online)
		})
	}
	// 目录里 home 不再由 control 中转（停用或改为直达）：attach 拒绝，已有会话被断开。
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.outbound("home", "host-gateway@home", clientAuth).Run(ctx)
	h.waitOnline("")
	direct := testsnapshot.New(t, "control")
	view, err := snapshot.Build("control", direct)
	require.NoError(t, err)
	h.state.Apply(view)
	h.server.Prune()
	require.Eventually(t, func() bool { return !h.status().Online }, 3*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	require.False(t, h.status().Online, "非出站主机不能重新接入")
}

// 新会话接替旧会话；会话断开后客户端自动重连。
func TestRelaySessionReplacementAndReconnect(t *testing.T) {
	h := newHarness(t)
	first := h.outbound("home", "host-gateway@home", clientAuth)
	firstCtx, stopFirst := context.WithCancel(t.Context())
	go first.Run(firstCtx)
	h.waitOnline(first.InstanceID())

	second := h.outbound("home", "host-gateway@home", clientAuth)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go second.Run(ctx)
	h.waitOnline(second.InstanceID())
	stopFirst()
	h.waitOnline(second.InstanceID())
	require.GreaterOrEqual(t, h.status().Reconnects, int64(1))

	// 服务端断开（模拟网络中断）：客户端按退避重连。
	outbound := h.state.Load()
	direct, err := snapshot.Build("control", testsnapshot.New(t, "control"))
	require.NoError(t, err)
	h.state.Apply(direct)
	h.server.Prune()
	require.False(t, h.status().Online)
	h.state.Apply(outbound)
	h.waitOnline(second.InstanceID())
	conn, err := h.open("monitor", servicecatalog.HostGatewayRemotePort)
	require.NoError(t, err)
	roundTrip(t, conn)
}
