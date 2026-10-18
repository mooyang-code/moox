package test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/listener"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/relay"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

type relayNonces struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (n *relayNonces) Consume(_ context.Context, namespace, nonce string, _ time.Duration) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen[namespace+nonce] {
		return false, nil
	}
	n.seen[namespace+nonce] = true
	return true, nil
}

type staticDirectory struct{ directory servicecatalog.Directory }

func (s staticDirectory) Fetch(_ context.Context, current string) (gatewayclient.DirectoryUpdate, error) {
	return gatewayclient.DirectoryUpdate{Changed: current != s.directory.Version, Directory: s.directory}, nil
}

// relayTunnels 模拟操作端：没有到出站主机的 SSH，只有到 control 跨主机端口的本地转发。
type relayTunnels struct{ control string }

func (relayTunnels) Resolve(context.Context, string) (string, error) {
	return "", errors.New("outbound host has no direct tunnel")
}
func (r relayTunnels) ResolveRelay(_ context.Context, controlHost string) (string, error) {
	if controlHost != "control" {
		return "", errors.New("unexpected relay host")
	}
	return r.control, nil
}

// 三方端到端：操作端网关客户端（隧道模式）→ control 网关中转 → 出站主机的 TLS tRPC 服务。
// 经中转的字节只有密文；第二次调用复用已建立的连接。
func TestRelayRPCThroughControlCarriesOnlyCiphertext(t *testing.T) {
	ca := testcert.New(t, nil)
	state := &snapshot.State{}
	view, err := snapshot.Build("control", testsnapshot.NewWithOutbound(t, "control", "home"))
	require.NoError(t, err)
	state.Apply(view)
	cfg := hostgatewayconfig.Default("control", "control", "127.0.0.1", "fixture-key")
	cfg.TLS = testcert.Files(t, ca, "control", nil)
	cfg.Server.RemoteAddr, cfg.Server.LocalAddr, cfg.Server.HealthAddr = testrpc.Address(t), testrpc.Address(t), testrpc.Address(t)
	cfg.Store.Path = filepath.Join(t.TempDir(), "cache")
	controlTLS, err := tlsconfig.Load("control", cfg.TLS)
	require.NoError(t, err)
	opened, err := listener.Open(t.Context(), cfg, controlTLS)
	require.NoError(t, err)
	t.Cleanup(func() { _ = opened.Close() })
	server, err := relay.NewServer(relay.ServerOptions{State: state, Nonces: &relayNonces{seen: map[string]bool{}}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	opened.SetRelay(server)
	go func() {
		for {
			conn, err := opened.Remote.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	homePaths := testcert.Files(t, ca, "home", func(cert *x509.Certificate) {
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	})
	homeTLS, err := tlsconfig.Load("home", homePaths)
	require.NoError(t, err)
	homeSocket, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	testrpc.Serve(t, tls.NewListener(homeSocket, homeTLS.Server()), "trpc.moox.hostgateway.Remote", func(_ context.Context, body []byte) ([]byte, error) {
		return body, nil
	})
	relayTLS, err := homeTLS.RelayClient("control")
	require.NoError(t, err)
	var mu sync.Mutex
	var forwarded bytes.Buffer
	var dials atomic.Int32
	client, err := relay.NewClient(relay.ClientOptions{
		HostID: "home", ControlHost: "control", Target: opened.Remote.Addr().String(), TLS: relayTLS,
		Credentials: testsnapshot.Credential("host-gateway@home"),
		Active:      func() bool { return true },
		Allowed:     func(port int) bool { return port == servicecatalog.HostGatewayRemotePort },
		DialLocal: func(int) (net.Conn, error) {
			dials.Add(1)
			conn, err := net.Dial("tcp", homeSocket.Addr().String())
			if err != nil {
				return nil, err
			}
			return &recordingConn{Conn: conn, mu: &mu, seen: &forwarded}, nil
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go client.Run(ctx)
	require.Eventually(t, func() bool {
		status := server.Status()
		return len(status) == 1 && status[0].Online
	}, 5*time.Second, 10*time.Millisecond)

	// 调用方的目录里该服务只在 home 上，确保请求走中转。
	directory := view.Directory()
	directory.Services["trpc.moox.hostagent.HostAgentMgr"] = []string{"home"}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	caller, err := gatewayclient.New(gatewayclient.Config{
		Mode: gatewayclient.Tunnel, Credentials: testsnapshot.Credential("moox-cli"), CAFile: homePaths.CAFile,
		Source: staticDirectory{directory: directory}, Tunnels: relayTunnels{control: opened.Remote.Addr().String()},
		Serialization: codec.SerializationTypeJSON,
	})
	require.NoError(t, err)
	defer caller.Close()
	marker := []byte(`{"marker":"relay-plaintext-must-not-appear"}`)
	for range 2 {
		response, err := caller.Forward(t.Context(), "trpc.moox.hostagent.HostAgentMgr", "GetStatus", codec.SerializationTypeJSON, marker)
		require.NoError(t, err)
		require.Equal(t, marker, response)
	}
	require.Equal(t, int32(1), dials.Load(), "连接池复用经中转建立的连接")
	mu.Lock()
	defer mu.Unlock()
	require.NotZero(t, forwarded.Len())
	require.False(t, bytes.Contains(forwarded.Bytes(), []byte("relay-plaintext-must-not-appear")), "control 转发的只有密文")
}

// recordingConn 记录出站主机从中转流收到、写往本机服务的字节。
type recordingConn struct {
	net.Conn
	mu   *sync.Mutex
	seen *bytes.Buffer
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.seen.Write(p)
	c.mu.Unlock()
	return c.Conn.Write(p)
}
