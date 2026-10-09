package marketwiring

import (
	"context"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
)

// fakeEgressProxy 在本机扮演出口代理：按路径返回准备好的 Binance exchangeInfo，并记下收到的请求。
type fakeEgressProxy struct {
	mu       sync.Mutex
	requests []string
}

func (f *fakeEgressProxy) Do(_ context.Context, req *egresspb.DoReq) (*egresspb.DoRsp, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req.GetMethod()+" "+req.GetHost()+req.GetPath())
	f.mu.Unlock()
	body, status := "", int32(404)
	switch req.GetPath() {
	case "/api/v3/exchangeInfo":
		body, status = `{"timezone":"UTC","symbols":[
			{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[]},
			{"symbol":"ETHBTC","status":"TRADING","baseAsset":"ETH","quoteAsset":"BTC","filters":[]},
			{"symbol":"LUNAUSDT","status":"BREAK","baseAsset":"LUNA","quoteAsset":"USDT","filters":[]}]}`, 200
	case "/fapi/v1/exchangeInfo":
		body, status = `{"timezone":"UTC","symbols":[
			{"symbol":"ETHUSDT","pair":"ETHUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDT","filters":[]},
			{"symbol":"ETHUSDT_261225","pair":"ETHUSDT","contractType":"CURRENT_QUARTER","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDT","filters":[]}]}`, 200
	}
	return &egresspb.DoRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Status:  status, Body: []byte(body), Headers: map[string]string{"Content-Type": "application/json"},
	}, nil
}

func (*fakeEgressProxy) ResolveDomains(context.Context, *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
	return &egresspb.ResolveDomainsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "未启用"}}, nil
}

func (f *fakeEgressProxy) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// recordingTagStore 记录写入的标签成员和上报的失败。
type recordingTagStore struct {
	mu       sync.Mutex
	tags     []*pb.Tag
	applied  map[string][]string
	failures map[string]string
}

func newRecordingTagStore() *recordingTagStore {
	return &recordingTagStore{
		tags: []*pb.Tag{
			{SpaceId: "crypto", TagId: "binance_spot", Mode: "auto", Source: "binance", MarketType: "spot", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "binance_swap", Mode: "auto", Source: "binance", MarketType: "swap", Cron: "0 * * * *", Timezone: "UTC"},
		},
		applied: map[string][]string{}, failures: map[string]string{},
	}
}

func (s *recordingTagStore) ListTags(context.Context) ([]*pb.Tag, error) { return s.tags, nil }

func (s *recordingTagStore) ApplyTagSnapshot(_ context.Context, _, tagID string, _ time.Time, items []*pb.TagSnapshotItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		s.applied[tagID] = append(s.applied[tagID], item.GetSubjectId())
	}
	sort.Strings(s.applied[tagID])
	return nil
}

func (s *recordingTagStore) ReportTagRunFailure(_ context.Context, _, tagID string, _ time.Time, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[tagID] = message
	return nil
}

func egressSubjectListers(t *testing.T, proxy httpclient.EgressDoer) subjectsync.Listers {
	t.Helper()
	domains, err := egresspb.ParseDomainList([]string{"*.binance.com", "data-api.binance.vision"})
	require.NoError(t, err)
	listers, err := NewSubjectListers(httpclient.NewEgressHTTPClient(domains, proxy))
	require.NoError(t, err)
	return listers
}

func egressProxyClient(address string) egresspb.ProxyClientProxy {
	return egresspb.NewProxyClientProxy(
		client.WithTarget("ip://"+address), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(5*time.Second),
	)
}

// 通过本地模拟的出口代理跑通 Binance 现货和合约标签的同步：标签同步 → 出口代理传输层 → tRPC → 出口代理。
func TestSubjectSyncThroughLocalEgressProxy(t *testing.T) {
	fake := &fakeEgressProxy{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.egress.Proxy"), server.WithListener(listener))
	egresspb.RegisterProxyService(service, fake)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })

	store := newRecordingTagStore()
	runner := &subjectsync.TagRunner{Store: store, Listers: egressSubjectListers(t, egressProxyClient(listener.Addr().String())), FetchTimeout: 10 * time.Second}
	require.NoError(t, runner.RunDue(context.Background(), time.Now()))

	require.Equal(t, []string{"BTC-USDT"}, store.applied["binance_spot"], "只保留以 USDT 计价且在交易的现货")
	require.Equal(t, []string{"ETH-USDT"}, store.applied["binance_swap"], "只保留在交易的永续合约")
	require.Empty(t, store.failures)
	var spot, swap bool
	for _, request := range fake.seen() {
		spot = spot || request == "GET api.binance.com/api/v3/exchangeInfo" || request == "GET data-api.binance.vision/api/v3/exchangeInfo" || request == "GET api-gcp.binance.com/api/v3/exchangeInfo"
		swap = swap || request == "GET fapi.binance.com/fapi/v1/exchangeInfo"
	}
	require.True(t, spot && swap, "现货和合约的请求都应当经过出口代理: %v", fake.seen())
}

// 出口代理停止后，标签同步失败并上报给 Storage。
func TestSubjectSyncReportsFailureWhenEgressProxyIsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	store := newRecordingTagStore()
	runner := &subjectsync.TagRunner{Store: store, Listers: egressSubjectListers(t, egressProxyClient(address)), FetchTimeout: 10 * time.Second}
	require.Error(t, runner.RunDue(context.Background(), time.Now()))
	require.Empty(t, store.applied)
	require.Contains(t, store.failures["binance_spot"], "出口代理")
	require.Contains(t, store.failures["binance_swap"], "出口代理")
}
