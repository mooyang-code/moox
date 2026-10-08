package router

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

const testHost = "storage"

var (
	strategyKey = gatewayauth.Credentials{KeyID: "strategy-1", Caller: "strategy", Secret: "strategy-secret"}
	consoleKey  = gatewayauth.Credentials{KeyID: "console-1", Caller: "console", Secret: "console-secret"}
)

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	return addr
}

type upstreamCall struct {
	rpcName       string
	body          []byte
	serialization int
	metadata      map[string]string
}

type upstream struct {
	mu    sync.Mutex
	calls []upstreamCall
	delay time.Duration
}

func (u *upstream) last(t *testing.T) upstreamCall {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	require.NotEmpty(t, u.calls)
	return u.calls[len(u.calls)-1]
}

func startWildcard(t *testing.T, addr, name string, handle func(context.Context, *codec.Body) (*codec.Body, error)) {
	t.Helper()
	svc := server.New(server.WithAddress(addr), server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName(name))
	desc := &server.ServiceDesc{ServiceName: name, HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
		req := &codec.Body{}
		filters, err := f(req)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, req, func(ctx context.Context, body interface{}) (interface{}, error) {
			return handle(ctx, body.(*codec.Body))
		})
	}}}}
	require.NoError(t, svc.Register(desc, struct{}{}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	time.Sleep(150 * time.Millisecond)
}

func (u *upstream) start(t *testing.T) string {
	t.Helper()
	addr := freeAddr(t)
	startWildcard(t, addr, "trpc.moox.storage.DataView", func(ctx context.Context, req *codec.Body) (*codec.Body, error) {
		msg := codec.Message(ctx)
		metadata := map[string]string{}
		for key, value := range msg.ServerMetaData() {
			metadata[key] = string(value)
		}
		u.mu.Lock()
		u.calls = append(u.calls, upstreamCall{rpcName: msg.ServerRPCName(), body: append([]byte(nil), req.Data...), serialization: msg.SerializationType(), metadata: metadata})
		delay := u.delay
		u.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		return &codec.Body{Data: append([]byte("echo:"), req.Data...)}, nil
	})
	return addr
}

type gatewayFixture struct {
	address  string
	current  *snapshot.Current
	upstream *upstream
	routes   []gatewayroute.Route
}

func newGatewayFixture(t *testing.T, timeoutMS int64) *gatewayFixture {
	t.Helper()
	up := &upstream{}
	upstreamAddr := up.start(t)
	f := &gatewayFixture{current: &snapshot.Current{}, upstream: up, routes: []gatewayroute.Route{
		{ServiceID: "storage-view", Address: upstreamAddr, ServicePath: "trpc.moox.storage.DataView", TimeoutMS: timeoutMS, MaxBodyBytes: 64,
			AllowedMethods: []string{"QueryTimeSeriesRows"}, AllowedCallers: []string{"strategy"}},
	}}
	f.apply(t, false)
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = nonces.Close() })
	desc, impl := ServiceDesc(Options{HostID: testHost, Snapshot: f.current, Nonces: nonces})
	f.address = freeAddr(t)
	svc := server.New(server.WithAddress(f.address), server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName(ServiceName))
	require.NoError(t, svc.Register(desc, impl))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	time.Sleep(150 * time.Millisecond)
	return f
}

func (f *gatewayFixture) apply(t *testing.T, disabled bool) {
	t.Helper()
	keys := []gatewayroute.VerificationKey{
		{KeyID: strategyKey.KeyID, Caller: strategyKey.Caller, Secret: strategyKey.Secret},
		{KeyID: consoleKey.KeyID, Caller: consoleKey.Caller, Secret: consoleKey.Secret},
	}
	built, err := testsnapshot.Build(testHost, disabled, f.routes, keys, testsnapshot.Directory(testHost, "trpc.moox.storage.DataView"))
	require.NoError(t, err)
	applied, err := snapshot.Validate(testHost, built)
	require.NoError(t, err)
	f.current.Store(applied, time.Now())
}

type callOptions struct {
	targetNode string
	pb         bool
	metadata   map[string]string
	replay     map[string]string
}

func (f *gatewayFixture) call(t *testing.T, credentials gatewayauth.Credentials, servicePath, method string, body []byte, options callOptions) ([]byte, map[string]string, error) {
	t.Helper()
	if options.targetNode == "" {
		options.targetNode = testHost
	}
	serialization := codec.SerializationTypeJSON
	if options.pb {
		serialization = codec.SerializationTypePB
	}
	rpcName := "/" + servicePath + "/" + method
	headers := options.replay
	if headers == nil {
		signed, err := gatewayauth.Sign(credentials, gatewayauth.Request{Method: http.MethodPost, Path: rpcName, TargetNode: options.targetNode, Callee: servicePath, Func: method, Body: body}, time.Now())
		require.NoError(t, err)
		headers = map[string]string{}
		for key, values := range signed {
			headers[key] = values[0]
		}
	}
	invoke := []client.Option{client.WithTarget("ip://" + f.address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(servicePath), client.WithCalleeMethod(method), client.WithSerializationType(serialization),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop), client.WithTimeout(10 * time.Second)}
	for key, value := range headers {
		invoke = append(invoke, client.WithMetaData(key, []byte(value)))
	}
	for key, value := range options.metadata {
		invoke = append(invoke, client.WithMetaData(key, []byte(value)))
	}
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithClientRPCName(rpcName)
	rsp := &codec.Body{}
	err := client.New().Invoke(ctx, &codec.Body{Data: body}, rsp, invoke...)
	return rsp.Data, headers, err
}

func TestForwardsExactBytesWithVerifiedCaller(t *testing.T) {
	f := newGatewayFixture(t, 5000)
	body := []byte(`{"view_id":"v1"}`)
	rsp, _, err := f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", body, callOptions{
		metadata: map[string]string{"x-space-id": "crypto", "x-moox-verified-caller": "console", "X-Moox-Forged": "1"},
	})
	require.NoError(t, err)
	require.Equal(t, append([]byte("echo:"), body...), rsp)
	call := f.upstream.last(t)
	require.Equal(t, body, call.body)
	require.Equal(t, codec.SerializationTypeJSON, call.serialization)
	require.Equal(t, "/trpc.moox.storage.DataView/QueryTimeSeriesRows", call.rpcName)
	require.Equal(t, "strategy", call.metadata[gatewayroute.MetadataVerifiedCaller], "调用方身份来自签名，不能伪造")
	require.Equal(t, "crypto", call.metadata["x-space-id"])
	for key := range call.metadata {
		require.NotEqual(t, "X-Moox-Signature", key, "签名头不应转发给上游")
		require.NotEqual(t, "X-Moox-Forged", key)
	}

	pb := []byte{0x0a, 0x02, 'v', '1'}
	rsp, _, err = f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", pb, callOptions{pb: true})
	require.NoError(t, err)
	require.Equal(t, append([]byte("echo:"), pb...), rsp)
	require.Equal(t, codec.SerializationTypePB, f.upstream.last(t).serialization)
}

func TestRejectsWithExplicitCodes(t *testing.T) {
	f := newGatewayFixture(t, 5000)
	body := []byte(`{}`)
	unknown := gatewayauth.Credentials{KeyID: "nobody-1", Caller: "nobody", Secret: "x"}
	_, _, err := f.call(t, unknown, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", body, callOptions{})
	require.Equal(t, gatewayroute.RetUnauthenticated, int(errs.Code(err)), "未知密钥")

	_, _, err = f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", body, callOptions{targetNode: "compute-1"})
	require.Equal(t, gatewayroute.RetUnauthenticated, int(errs.Code(err)), "目标主机不是本机")

	_, _, err = f.call(t, consoleKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", body, callOptions{})
	require.Equal(t, gatewayroute.RetForbidden, int(errs.Code(err)), "调用方不在 ACL 中")
	require.Contains(t, errs.Msg(err), "console")

	_, _, err = f.call(t, strategyKey, "trpc.moox.storage.DataView", "SearchRecordRows", body, callOptions{})
	require.Equal(t, gatewayroute.RetForbidden, int(errs.Code(err)), "方法未开放")

	_, _, err = f.call(t, strategyKey, "trpc.moox.storage.Metadata", "ListDatasets", body, callOptions{})
	require.Equal(t, gatewayroute.RetServiceNotHere, int(errs.Code(err)), "服务不在本机")

	_, _, err = f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", make([]byte, 65), callOptions{})
	require.Equal(t, gatewayroute.RetBodyTooLarge, int(errs.Code(err)))

	_, headers, err := f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", body, callOptions{})
	require.NoError(t, err)
	_, _, err = f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", body, callOptions{replay: headers})
	require.Equal(t, gatewayroute.RetUnauthenticated, int(errs.Code(err)))
	require.Contains(t, errs.Msg(err), "重放")

	f.apply(t, true)
	_, _, err = f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", body, callOptions{})
	require.Equal(t, gatewayroute.RetHostDisabled, int(errs.Code(err)))
}

func TestNoSnapshotMeansServiceNotHere(t *testing.T) {
	desc, impl := ServiceDesc(Options{HostID: testHost, Snapshot: &snapshot.Current{}})
	addr := freeAddr(t)
	svc := server.New(server.WithAddress(addr), server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName(ServiceName))
	require.NoError(t, svc.Register(desc, impl))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	time.Sleep(150 * time.Millisecond)
	f := &gatewayFixture{address: addr}
	_, _, err := f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", []byte(`{}`), callOptions{})
	require.Equal(t, gatewayroute.RetServiceNotHere, int(errs.Code(err)))
}

// 上游比路由超时慢时，调用方要立刻收到超时错误，而不是等到它自己更长的超时。
func TestUpstreamTimeoutAnswersCallerPromptly(t *testing.T) {
	f := newGatewayFixture(t, 1000)
	f.upstream.delay = 3 * time.Second
	started := time.Now()
	_, _, err := f.call(t, strategyKey, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", []byte(`{}`), callOptions{})
	require.Error(t, err)
	require.Less(t, time.Since(started), 2500*time.Millisecond)
	require.Equal(t, errs.RetClientTimeout, errs.Code(err))
	require.Contains(t, errs.Msg(err), "没有响应")
}
