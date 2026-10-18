package httpclient_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const fixtureService = "trpc.moox.egress.Proxy"

// Closing the fixture also closes pooled native connections, so shutdown is
// observable before the next invocation instead of depending on idle cleanup.
type fixtureListener struct {
	net.Listener
	mu     sync.Mutex
	closed bool
	conns  []net.Conn
}

func (l *fixtureListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	l.conns = append(l.conns, conn)
	return conn, nil
}

func (l *fixtureListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	err := l.Listener.Close()
	for _, conn := range l.conns {
		_ = conn.Close()
	}
	return err
}

type egressFixture struct{ calls atomic.Int32 }

func (f *egressFixture) Do(_ context.Context, req *egresspb.DoReq) (*egresspb.DoRsp, error) {
	f.calls.Add(1)
	if req.Host != "data-api.binance.vision" && !strings.HasSuffix(req.Host, ".binance.com") {
		return &egresspb.DoRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION}}, nil
	}
	if req.Path == "/large" {
		return &egresspb.DoRsp{RetInfo: &commonpb.RetInfo{}, Status: 200, Body: bytes.Repeat([]byte("x"), 32<<20)}, nil
	}
	if req.Path == "/retry" {
		return &egresspb.DoRsp{RetInfo: &commonpb.RetInfo{}, Status: 429, Body: []byte("retry later")}, nil
	}
	return &egresspb.DoRsp{RetInfo: &commonpb.RetInfo{}, Status: 200, Body: []byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","contractType":"PERPETUAL","baseAsset":"BTC","quoteAsset":"USDT"}]}`)}, nil
}
func (*egressFixture) ResolveDomains(_ context.Context, req *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
	result := &egresspb.ResolveDomainsRsp{RetInfo: &commonpb.RetInfo{}}
	for _, domain := range req.Domains {
		result.Resolutions = append(result.Resolutions, &egresspb.DomainResolution{Domain: domain, Ips: []*egresspb.ResolvedIP{{Ip: "8.8.8.8", TcpConnectLatencyMs: 12}, {Ip: "127.0.0.1"}}})
	}
	return result, nil
}

type directoryFixture struct{ directory servicecatalog.Directory }

func (s directoryFixture) Fetch(_ context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	return gatewayclient.DirectoryUpdate{Changed: version != s.directory.Version, Directory: s.directory.Clone()}, nil
}

type tagSnapshotFixture struct {
	tags     []*storagepb.Tag
	applied  map[string][]*storagepb.TagSnapshotItem
	failures map[string]string
}

func (f *tagSnapshotFixture) ListTags(context.Context) ([]*storagepb.Tag, error) { return f.tags, nil }
func (f *tagSnapshotFixture) ApplyTagSnapshot(_ context.Context, _, tag string, runAt time.Time, items []*storagepb.TagSnapshotItem) error {
	f.applied[tag] = items
	for _, item := range f.tags {
		if item.TagId == tag {
			item.LastRunAt = runAt.Format(time.RFC3339)
		}
	}
	return nil
}
func (f *tagSnapshotFixture) ReportTagRunFailure(_ context.Context, _, tag string, _ time.Time, message string) error {
	f.failures[tag] = message
	return nil
}

// This opt-in test uses the production gateway router and shared client. The
// loopback upstream supplies deterministic responses; Egress's own suite tests
// real TLS and DNS connections separately.
func TestEgressTransportNativeGatewayE2E(t *testing.T) {
	binary := os.Getenv("MOOX_EGRESS_E2E_GATEWAY_HELPER_BINARY")
	if binary == "" {
		t.Skip("set MOOX_EGRESS_E2E_GATEWAY_HELPER_BINARY to a native helper built on this platform")
	}
	root := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(&fixtureListener{Listener: listener}), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName(fixtureService))
	backend := &egressFixture{}
	require.NoError(t, service.Register(&egresspb.ProxyServer_ServiceDesc, backend))
	served := make(chan error, 1)
	go func() { served <- service.Serve() }()
	backendClosed := false
	t.Cleanup(func() {
		if !backendClosed {
			require.NoError(t, service.Close(nil))
		}
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("native upstream did not stop")
		}
	})
	ready := filepath.Join(root, "gateway.ready")
	credential := gatewayauth.Credentials{Caller: "collector", KeyID: "assigned-egress-collector-13", Secret: "fixture-egress-collector-key-at-least-32-bytes"}
	cmd := exec.Command(binary, "-mode=egress-native", "-node-id=compute-1", "-upstream-addr="+listener.Addr().String(), "-ready-file="+ready, "-nonce-dir="+filepath.Join(root, "nonces"), "-key-id="+credential.KeyID)
	cmd.Env = append(os.Environ(), "MOOX_GATEWAY_E2E_SERVICE_SECRET="+credential.Secret)
	logFile, err := os.Create(filepath.Join(root, "gateway.log"))
	require.NoError(t, err)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	require.NoError(t, cmd.Start())
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case err := <-stopped:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-stopped
			t.Error("gateway helper did not stop")
		}
		_ = logFile.Close()
	})
	var address string
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(ready)
		if err == nil {
			address = strings.TrimPrefix(strings.TrimSpace(string(raw)), "ip://")
		}
		return address != ""
	}, 10*time.Second, 20*time.Millisecond)
	ca := httptest.NewTLSServer(http.NotFoundHandler())
	defer ca.Close()
	caFile := filepath.Join(root, "ca.crt")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Certificate().Raw}), 0600))
	directory := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{"compute-1": {Address: "127.0.0.1"}}, Services: map[string][]string{fixtureService: {"compute-1"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		t.Run(fmt.Sprint(serialization), func(t *testing.T) {
			gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: credential, LocalHostID: "compute-1", LocalAddress: address, CAFile: caFile, Source: directoryFixture{directory}, Serialization: serialization})
			require.NoError(t, err)
			defer gateway.Close()
			client, err := httpclient.NewEgressHTTPClient(gateway, []string{"*.binance.com", "data-api.binance.vision"})
			require.NoError(t, err)
			defer client.Close()
			dns := dnsresolver.NewEgressClient(gateway, 5*time.Second)
			resolutions, err := dns.ResolveDomains(context.Background(), []string{"api.binance.com"})
			require.NoError(t, err)
			require.Equal(t, []string{"8.8.8.8"}, resolutions["api.binance.com"].IPs)
			api := binanceapi.NewSpotAPI(binanceapi.NewClient(client))
			symbols, err := api.GetExchangeInfoWithDomainIPs(context.Background(), "api.binance.com", resolutions["api.binance.com"].IPs)
			require.NoError(t, err)
			require.Len(t, symbols, 1)
			require.Equal(t, "BTC-USDT", symbols[0].Symbol)
			var value any
			err = client.Get(context.Background(), "api.binance.com", "/retry", nil, &value)
			var status *httpclient.StatusError
			require.ErrorAs(t, err, &status)
			require.Equal(t, 429, status.StatusCode)
			transport, err := httpclient.NewEgressTransport(gateway, []string{"*.binance.com"}, nil)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.binance.com/large", nil)
			require.NoError(t, err)
			rsp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			defer rsp.Body.Close()
			require.Equal(t, int64(32<<20), rsp.ContentLength)
			listers, err := marketwiring.NewSubjectListers(client)
			require.NoError(t, err)
			store := &tagSnapshotFixture{applied: map[string][]*storagepb.TagSnapshotItem{}, failures: map[string]string{}}
			for _, kind := range []string{"spot", "swap"} {
				store.tags = append(store.tags, &storagepb.Tag{SpaceId: "crypto", TagId: kind, Mode: "auto", Source: "binance", MarketType: kind, Cron: "0 * * * *", Timezone: "UTC"})
			}
			now := time.Date(2026, 10, 9, 10, 0, 30, 0, time.UTC)
			runner := &subjectsync.TagRunner{Store: store, Listers: listers, Now: func() time.Time { return now }}
			require.NoError(t, runner.RunOnce(context.Background()))
			require.Len(t, store.applied, 2)
			for _, kind := range []string{"spot", "swap"} {
				require.Len(t, store.applied[kind], 1)
				require.Equal(t, "BTC-USDT", store.applied[kind][0].SubjectId)
			}
			before := backend.calls.Load()
			require.NoError(t, runner.RunOnce(context.Background()))
			require.Equal(t, before, backend.calls.Load(), "tags not due must not fetch")
			if serialization == codec.SerializationTypeJSON {
				require.NoError(t, service.Close(nil))
				backendClosed = true
				now = now.Add(time.Hour)
				failureCtx, cancelFailure := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancelFailure()
				require.Error(t, runner.RunOnce(failureCtx), "stopped Egress must fail without direct fallback")
				require.NotEmpty(t, store.failures, "proxy failure must be reported to Storage")
				require.Equal(t, before, backend.calls.Load())
			}
		})
	}
	require.Equal(t, int32(10), backend.calls.Load(), "HTTP status errors, snapshot IPs and skipped tags must not duplicate requests")
}
