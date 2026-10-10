package accessproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

type memoryNonces struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (m *memoryNonces) Consume(_ context.Context, namespace, nonce string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]struct{}{}
	}
	key := namespace + ":" + nonce
	if _, ok := m.seen[key]; ok {
		return false, nil
	}
	m.seen[key] = struct{}{}
	return true, nil
}

// fakeUpstream 模拟 gatewayclient：返回固定的本机主机 ID，并记录转发的请求。
type fakeUpstream struct {
	localHostID   string
	directoryErr  error
	forwardErr    error
	response      []byte
	calls         int
	servicePath   string
	method        string
	serialization int
	body          []byte
}

func (f *fakeUpstream) Directory(context.Context) (gatewayclient.View, error) {
	return gatewayclient.View{LocalHostID: f.localHostID}, f.directoryErr
}

func (f *fakeUpstream) Forward(_ context.Context, servicePath, method string, serialization int, body []byte, _ ...gatewayclient.CallOption) ([]byte, error) {
	f.calls++
	f.servicePath, f.method, f.serialization, f.body = servicePath, method, serialization, append([]byte(nil), body...)
	return f.response, f.forwardErr
}

// fakeMetrics 记录拒绝和转发结果。
type fakeMetrics struct {
	rejected  []string
	forwarded []string
}

func (m *fakeMetrics) Rejected(principal, reason string) {
	m.rejected = append(m.rejected, principal+":"+reason)
}

func (m *fakeMetrics) Forwarded(principal, servicePath, method string, code int, _ time.Duration) {
	m.forwarded = append(m.forwarded, principal+":"+servicePath+"/"+method+":"+strconv.Itoa(code))
}

var (
	testNow       = time.Unix(1_800_000_000, 0)
	scfCollector  = gatewayauth.Credentials{KeyID: "scf-collector-1", Caller: "scf-collector", Secret: "scf-collector-secret"}
	factorEngine  = gatewayauth.Credentials{KeyID: "factor-engine-1", Caller: "factor-engine", Secret: "factor-engine-secret"}
	commitBatch   = "/trpc.moox.storage.PrimaryStore/CommitTimeSeriesBatch"
	writeFactors  = "/trpc.moox.storage.PrimaryStore/WriteFactorRows"
	claimTimer    = "/trpc.moox.collector.MarketFetchRuntime/ClaimTimerBatch"
	storageAccess = "access@storage"
)

func testProxy(t *testing.T, upstream *fakeUpstream, metrics *fakeMetrics) *Proxy {
	t.Helper()
	registry, err := gatewayauth.NewCredentialRegistry([]gatewayauth.Credentials{scfCollector, factorEngine})
	require.NoError(t, err)
	proxy, err := New(Options{
		Registry: registry, Upstream: upstream, Nonces: &memoryNonces{}, Metrics: metrics,
		MaxBodyBytes: 1024, Now: func() time.Time { return testNow },
	})
	require.NoError(t, err)
	return proxy
}

// signedContext 构造外部调用方发给 targetNode 的已签名请求。
func signedContext(t *testing.T, credentials gatewayauth.Credentials, targetNode, rpcName string, body []byte) context.Context {
	t.Helper()
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerRPCName(rpcName)
	msg.WithSerializationType(codec.SerializationTypePB)
	servicePath, method, ok := splitRPCName(rpcName)
	require.True(t, ok)
	headers, err := gatewayauth.Sign(credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: rpcName, TargetNode: targetNode, Caller: credentials.Caller,
		Callee: servicePath, Func: method, Body: body,
	}, testNow)
	require.NoError(t, err)
	metadata := codec.MetaData{gatewayroute.MetadataSpaceID: []byte("crypto")}
	for key, values := range headers {
		metadata[key] = []byte(values[0])
	}
	msg.WithServerMetaData(metadata)
	return ctx
}

func requireCode(t *testing.T, err error, code int) {
	t.Helper()
	require.Error(t, err)
	require.EqualValues(t, code, errs.Code(err), "错误：%v", err)
}

func TestForwardSendsOriginalBytesForAllowedMethod(t *testing.T) {
	upstream := &fakeUpstream{localHostID: "storage", response: []byte("upstream-response")}
	metrics := &fakeMetrics{}
	proxy := testProxy(t, upstream, metrics)
	body := []byte{0x0a, 0x03, 'p', 'b', '!'}

	response, err := proxy.Forward(signedContext(t, scfCollector, storageAccess, commitBatch, body), &codec.Body{Data: body})
	require.NoError(t, err)
	require.Equal(t, []byte("upstream-response"), response.Data)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, "trpc.moox.storage.PrimaryStore", upstream.servicePath)
	require.Equal(t, "CommitTimeSeriesBatch", upstream.method)
	require.Equal(t, codec.SerializationTypePB, upstream.serialization)
	require.Equal(t, body, upstream.body, "转发的 PB 字节必须与原始请求一致")
	require.Empty(t, metrics.rejected)
	require.Len(t, metrics.forwarded, 1)

	_, err = proxy.Forward(signedContext(t, scfCollector, storageAccess, claimTimer, body), &codec.Body{Data: body})
	require.NoError(t, err, "scf-collector 可以经外部接入领取 Timer 批次")
}

func TestForwardRejectsMethodOutsideAllowlist(t *testing.T) {
	upstream := &fakeUpstream{localHostID: "storage"}
	metrics := &fakeMetrics{}
	proxy := testProxy(t, upstream, metrics)
	body := []byte("rows")

	_, err := proxy.Forward(signedContext(t, scfCollector, storageAccess, writeFactors, body), &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetForbidden)
	require.Zero(t, upstream.calls, "白名单之外的方法不能转发")
	require.Equal(t, []string{"scf-collector:" + ReasonForbidden}, metrics.rejected)

	_, err = proxy.Forward(signedContext(t, factorEngine, storageAccess, writeFactors, body), &codec.Body{Data: body})
	require.NoError(t, err, "factor-engine 可以写入因子结果")
}

func TestForwardRejectsReplayUnknownCallerAndWrongInstance(t *testing.T) {
	upstream := &fakeUpstream{localHostID: "storage"}
	metrics := &fakeMetrics{}
	proxy := testProxy(t, upstream, metrics)
	body := []byte("batch")

	ctx := signedContext(t, scfCollector, storageAccess, commitBatch, body)
	_, err := proxy.Forward(ctx, &codec.Body{Data: body})
	require.NoError(t, err)
	_, err = proxy.Forward(ctx, &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetUnauthenticated)

	unknown := gatewayauth.Credentials{KeyID: "intruder-1", Caller: "scf-collector", Secret: "guessed"}
	_, err = proxy.Forward(signedContext(t, unknown, storageAccess, commitBatch, body), &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetUnauthenticated)

	_, err = proxy.Forward(signedContext(t, scfCollector, "access@compute-1", commitBatch, body), &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetUnauthenticated)

	require.Equal(t, 1, upstream.calls)
	require.Equal(t, []string{
		"scf-collector:" + ReasonReplayed, unknownPrincipal + ":" + ReasonUnauthenticated, unknownPrincipal + ":" + ReasonUnauthenticated,
	}, metrics.rejected)
}

func TestForwardRequiresDirectoryAndBoundsBody(t *testing.T) {
	body := []byte("batch")
	waiting := &fakeUpstream{directoryErr: errors.New("本机主机网关不可达")}
	_, err := testProxy(t, waiting, &fakeMetrics{}).Forward(signedContext(t, scfCollector, storageAccess, commitBatch, body), &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetHostDisabled)

	upstream := &fakeUpstream{localHostID: "storage", response: make([]byte, 2048)}
	proxy := testProxy(t, upstream, &fakeMetrics{})
	large := make([]byte, 2048)
	_, err = proxy.Forward(signedContext(t, scfCollector, storageAccess, commitBatch, large), &codec.Body{Data: large})
	requireCode(t, err, gatewayroute.RetBodyTooLarge)
	_, err = proxy.Forward(signedContext(t, scfCollector, storageAccess, commitBatch, body), &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetBodyTooLarge)
}

func TestForwardReturnsUpstreamErrorCode(t *testing.T) {
	upstream := &fakeUpstream{localHostID: "storage", forwardErr: errs.New(gatewayroute.RetServiceNotHere, "服务不在本机")}
	body := []byte("batch")
	_, err := testProxy(t, upstream, &fakeMetrics{}).Forward(signedContext(t, scfCollector, storageAccess, commitBatch, body), &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetServiceNotHere)
}

func TestNewRequiresDependencies(t *testing.T) {
	registry, err := gatewayauth.NewCredentialRegistry([]gatewayauth.Credentials{scfCollector})
	require.NoError(t, err)
	_, err = New(Options{Upstream: &fakeUpstream{}, Nonces: &memoryNonces{}})
	require.Error(t, err)
	_, err = New(Options{Registry: registry, Nonces: &memoryNonces{}})
	require.Error(t, err)
	_, err = New(Options{Registry: registry, Upstream: &fakeUpstream{}})
	require.Error(t, err)
}

// 未认证的请求可以带任意的 RPC 名：指标只能按通过白名单的服务和方法打标签，否则公网请求能无限增加序列；
// 签名失败的原因也不能返回给未认证的调用方。
func TestUnauthenticatedRequestsDoNotCreateMetricSeriesOrLeakReasons(t *testing.T) {
	upstream := &fakeUpstream{localHostID: "storage"}
	metrics := &fakeMetrics{}
	proxy := testProxy(t, upstream, metrics)
	body := []byte("x")

	unknown := gatewayauth.Credentials{KeyID: "intruder-1", Caller: "scf-collector", Secret: "guessed"}
	for index := 0; index < 20; index++ {
		rpcName := fmt.Sprintf("/svc.random%d/Method%d", index, index)
		_, err := proxy.Forward(signedContext(t, unknown, storageAccess, rpcName, body), &codec.Body{Data: body})
		requireCode(t, err, gatewayroute.RetUnauthenticated)
		require.NotContains(t, err.Error(), "intruder-1", "未知的 KeyID 不能回显给调用方")
		require.NotContains(t, err.Error(), "KeyID")
	}
	// 已认证但不在白名单里的请求同样不记服务和方法标签。
	_, err := proxy.Forward(signedContext(t, scfCollector, storageAccess, "/svc.random/Method", body), &codec.Body{Data: body})
	requireCode(t, err, gatewayroute.RetForbidden)
	require.Empty(t, metrics.forwarded, "只有通过白名单的请求才按服务和方法记指标")
}
