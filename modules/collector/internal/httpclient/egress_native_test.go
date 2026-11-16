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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
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

type egressFixture struct {
	calls  atomic.Int32
	signed atomic.Int32
}

func (f *egressFixture) Do(ctx context.Context, req *egresspb.DoReq) (*egresspb.DoRsp, error) {
	f.calls.Add(1)
	for name := range codec.Message(ctx).ServerMetaData() {
		if strings.HasPrefix(strings.ToLower(name), "x-moox-") {
			f.signed.Add(1)
		}
	}
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

// directoryFixture 下发的目录：采集器与 egress-proxy 部署在同一台主机上。
type directoryFixture struct{}

func (directoryFixture) Fetch(_ context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	const current = "v1"
	return gatewayclient.DirectoryUpdate{Changed: version != current, Version: current,
		Hosts: []servicecatalog.Host{{ID: "overseas", Address: "overseas.example.test", Status: servicecatalog.Enabled}},
		Deployments: []servicecatalog.Deployment{
			{HostID: "overseas", ComponentID: "collector", Status: servicecatalog.Enabled},
			{HostID: "overseas", ComponentID: "egress-proxy", Status: servicecatalog.Enabled},
		}}, nil
}

type tagSnapshotFixture struct {
	tags    []*storagepb.Tag
	applied map[string][]*storagepb.TagSnapshotItem
}

func (f *tagSnapshotFixture) ListTags(context.Context) ([]*storagepb.Tag, error) { return f.tags, nil }
func (f *tagSnapshotFixture) ApplyTagSnapshot(_ context.Context, _, tag string, items []*storagepb.TagSnapshotItem) error {
	f.applied[tag] = items
	return nil
}

// memoryRuns 在内存里保存标签的同步运行结果。
type memoryRuns struct {
	runs map[string]domain.TagSyncRun
}

func (m *memoryRuns) List(context.Context, string) ([]domain.TagSyncRun, error) {
	out := make([]domain.TagSyncRun, 0, len(m.runs))
	for _, run := range m.runs {
		out = append(out, run)
	}
	return out, nil
}

func (m *memoryRuns) Record(_ context.Context, run domain.TagSyncRun) error {
	m.runs[run.TagID] = run
	return nil
}

// 采集器经共享的网关客户端调 Egress。两者同机，客户端直连 egress-proxy 的回环端口（这里用测试缝指到假 Egress 的临时端口），
// 请求不带签名头。假上游给出确定的响应；真实的 TLS 与 DNS 连接由 Egress 自己的测试覆盖。
// 跨主机经网关调 Egress 的链路（TLS、验签）在这里测不到，由网关与网关客户端各自的测试覆盖。
func TestEgressTransportNativeLocalDirect(t *testing.T) {
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
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	spec, ok := catalog.Service(fixtureService)
	require.True(t, ok)
	t.Cleanup(gatewayclient.SetLocalServiceAddressForTest(func(port int) string {
		if port != spec.Port {
			return "127.0.0.1:1"
		}
		return listener.Addr().String()
	}))
	credential := authtest.Component("collector", "overseas")
	ca := httptest.NewTLSServer(http.NotFoundHandler())
	defer ca.Close()
	caFile := filepath.Join(root, "ca.crt")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Certificate().Raw}), 0600))
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		t.Run(fmt.Sprint(serialization), func(t *testing.T) {
			gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: credential, LocalHostID: "overseas", CAFile: caFile, Source: directoryFixture{}, Serialization: serialization})
			require.NoError(t, err)
			defer gateway.Close()
			client, err := httpclient.NewEgressHTTPClient(gateway, []string{"*.binance.com", "data-api.binance.vision"})
			require.NoError(t, err)
			defer client.Close()
			api := binanceapi.NewSpotAPI(binanceapi.NewClient(client))
			symbols, err := api.GetExchangeInfoWithDomainIPs(context.Background(), "api.binance.com", nil)
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
			listers, _, err := marketwiring.NewSubjectListers(client)
			require.NoError(t, err)
			store := &tagSnapshotFixture{applied: map[string][]*storagepb.TagSnapshotItem{}}
			runs := &memoryRuns{runs: map[string]domain.TagSyncRun{}}
			for _, kind := range []string{"spot", "swap"} {
				store.tags = append(store.tags, &storagepb.Tag{SpaceId: "crypto", TagId: kind, Mode: "auto", Attributes: map[string]string{"sync.source": "binance", "market_type": kind, "sync.cron": "0 * * * *", "sync.timezone": "UTC"}})
			}
			now := time.Date(2026, 10, 9, 10, 0, 30, 0, time.UTC)
			runner := &subjectsync.TagRunner{Store: store, Runs: runs, Listers: listers, Now: func() time.Time { return now }}
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
				require.Equal(t, domain.TagSyncFailed, runs.runs["spot"].LastStatus, "proxy failure must be recorded in the Collector database")
				require.Equal(t, before, backend.calls.Load())
			}
		})
	}
	require.Equal(t, int32(10), backend.calls.Load(), "HTTP status errors, snapshot IPs and skipped tags must not duplicate requests")
	require.Zero(t, backend.signed.Load(), "同机直连 Egress 的请求不带签名头")
}
