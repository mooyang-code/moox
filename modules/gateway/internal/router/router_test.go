package router

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/gateway/internal/health"
	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	testNode   = "gateway-test"
	testKeyID  = "moox-gateway-service"
	testSecret = "service-secret"
)

func TestServiceRouterProxiesAuthenticatedRequestAndPreservesHeaders(t *testing.T) {
	upstream := newUpstream(t)
	handler, closeHandler := newHandler(t, upstream, false, 1024)
	defer closeHandler()

	req := signedRequest(t, http.MethodPost, "/api/service/monitor/GetSnapshot", []byte(`{"ok":true}`), testNode, testSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-Id", "trace-123")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated || recorder.Header().Get("trpc-ret") != "0" || recorder.Header().Get("X-Trace-Id") != "trace-123" {
		t.Fatalf("response = %d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestNativeServiceDescUsesWildcardMethod(t *testing.T) {
	desc, implementation := NativeServiceDesc(NativeOptions{Table: &gatewayproxy.Table{}})
	if implementation == nil || desc == nil || len(desc.Methods) != 1 || desc.Methods[0].Name != "*" {
		t.Fatalf("native descriptor = %+v implementation=%T", desc, implementation)
	}
}

func TestNativeReadOnlyMethodAllowlist(t *testing.T) {
	for _, test := range []struct {
		method string
		read   bool
	}{
		{method: "ListViews", read: true},
		{method: "QueryTimeSeriesRows", read: true},
		{method: "GetSpace", read: true},
		{method: "UpdateView", read: false},
		{method: "ActivateViewIndex", read: false},
	} {
		if got := nativeReadOnlyMethod(test.method); got != test.read {
			t.Fatalf("nativeReadOnlyMethod(%q) = %v, want %v", test.method, got, test.read)
		}
	}
}

func TestNativeGatewayRoundTripsJSONThroughGeneratedStorageHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	upstream := server.New(server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.storage.Metadata"), server.WithListener(listener))
	storagepb.RegisterMetadataService(upstream, &nativeMetadataStub{})
	serveErr := make(chan error, 1)
	go func() { serveErr <- upstream.Serve() }()
	t.Cleanup(func() {
		upstream.Close(nil)
		select {
		case <-serveErr:
		case <-time.After(time.Second):
		}
	})

	snapshot, err := gatewayproxy.NormalizeAndHashState(testNode, false, []gatewayproxy.Route{{
		ServiceID: "storage-primary", Address: listener.Addr().String(), ServicePath: "trpc.moox.storage.Metadata",
		AllowedMethods: []string{"GetSpace"}, AllowedCallers: []string{"admin-gateway"}, MaxBodyBytes: 1 << 20,
	}})
	require.NoError(t, err)
	var table gatewayproxy.Table
	require.NoError(t, table.Replace(snapshot))
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	require.NoError(t, err)
	defer nonces.Close()
	desc, implementation := NativeServiceDesc(NativeOptions{NodeID: testNode, Credentials: gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, Table: &table, Nonces: nonces})
	gatewayListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gatewayServer := server.New(server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.gateway.ServiceGateway"), server.WithListener(gatewayListener), server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	require.NoError(t, gatewayServer.Register(desc, implementation))
	gatewayServeErr := make(chan error, 1)
	go func() { gatewayServeErr <- gatewayServer.Serve() }()
	t.Cleanup(func() {
		gatewayServer.Close(nil)
		select {
		case <-gatewayServeErr:
		case <-time.After(time.Second):
		}
	})
	body, err := codec.Marshal(codec.SerializationTypePB, &storagepb.GetSpaceReq{SpaceId: "space-1"})
	require.NoError(t, err)
	headers, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, gatewayauth.Request{
		Method: "POST", Path: "/trpc.moox.storage.Metadata/GetSpace", TargetNode: testNode,
		Caller: "admin-gateway", Callee: "trpc.moox.storage.Metadata", Func: "GetSpace", Body: body,
	}, time.Now())
	require.NoError(t, err)
	ctx, message := codec.EnsureMessage(context.Background())
	message.WithClientRPCName("/trpc.moox.storage.Metadata/GetSpace")
	metadata := codec.MetaData{}
	for key, values := range headers {
		metadata[key] = []byte(values[0])
	}
	invokeOptions := []client.Option{
		client.WithTarget("ip://" + gatewayListener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
	}
	for key, value := range metadata {
		invokeOptions = append(invokeOptions, client.WithMetaData(key, value))
	}
	storageProxy := storagepb.NewMetadataClientProxy(invokeOptions...)
	decoded, err := storageProxy.GetSpace(ctx, &storagepb.GetSpaceReq{SpaceId: "space-1"})
	require.NoError(t, err)
	require.Equal(t, "space-1", decoded.GetSpace().GetSpaceId())
	require.Equal(t, int32(0), int32(decoded.GetRetInfo().GetCode()))
}

type nativeMetadataStub struct {
	storagepb.UnimplementedMetadata
}

func (*nativeMetadataStub) GetSpace(context.Context, *storagepb.GetSpaceReq) (*storagepb.GetSpaceRsp, error) {
	return &storagepb.GetSpaceRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Space: &storagepb.Space{SpaceId: "space-1"}}, nil
}

func TestNativeGatewayAuthenticatesReplaysAndEnforcesRouteBodyLimit(t *testing.T) {
	snapshot, err := gatewayproxy.NormalizeAndHashState(testNode, false, []gatewayproxy.Route{{
		ServiceID: "echo", Address: "127.0.0.1:1", ServicePath: "trpc.test.Echo", AllowedMethods: []string{"Echo"}, AllowedCallers: []string{"*"}, MaxBodyBytes: 1,
	}})
	require.NoError(t, err)
	var table gatewayproxy.Table
	require.NoError(t, table.Replace(snapshot))
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	require.NoError(t, err)
	defer nonces.Close()
	desc, _ := NativeServiceDesc(NativeOptions{
		NodeID: testNode, Credentials: gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, Table: &table, Nonces: nonces,
	})
	call := func(body []byte, headers http.Header) error {
		ctx, message := codec.EnsureMessage(context.Background())
		message.WithServerRPCName("/trpc.test.Echo/Echo")
		metadata := codec.MetaData{}
		for key, values := range headers {
			if len(values) > 0 {
				metadata[key] = []byte(values[0])
			}
		}
		message.WithServerMetaData(metadata)
		_, callErr := desc.Methods[0].Func(nil, ctx, func(request interface{}) (filter.ServerChain, error) {
			request.(*codec.Body).Data = body
			return filter.ServerChain{}, nil
		})
		return callErr
	}
	overSizedHeaders, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, gatewayauth.Request{
		Method: http.MethodPost, Path: "/trpc.test.Echo/Echo", TargetNode: testNode, Callee: "trpc.test.Echo", Func: "Echo", Body: []byte("too large"),
	}, time.Now())
	require.NoError(t, err)
	if err := call([]byte("too large"), overSizedHeaders); err == nil || !strings.Contains(err.Error(), "body exceeds route limit") {
		t.Fatalf("oversized native request error = %v", err)
	}

	// Use a fresh one-byte body so authentication reaches the upstream call;
	// the unavailable upstream is expected, while the second delivery must be
	// rejected by the persistent nonce store before dialing it again.
	body := []byte("x")
	signedHeaders, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, gatewayauth.Request{
		Method: http.MethodPost, Path: "/trpc.test.Echo/Echo", TargetNode: testNode, Callee: "trpc.test.Echo", Func: "Echo", Body: body,
	}, time.Now())
	require.NoError(t, err)
	first := call(body, signedHeaders)
	if first == nil || strings.Contains(first.Error(), "replayed") {
		t.Fatalf("first native request error = %v, want upstream error", first)
	}
	second := call(body, signedHeaders)
	if second == nil || !strings.Contains(second.Error(), "replayed") {
		t.Fatalf("replayed native request error = %v", second)
	}
}

func TestServiceRouterAllowsAuthenticatedGetSecretValue(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trpc.moox.ops.SecretMgr/GetSecretValue" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"secret":{"secret_value":"plain"}}`))
	}))
	defer upstream.Close()
	snapshot, err := gatewayproxy.NormalizeAndHashState(testNode, false, []gatewayproxy.Route{{ServiceID: "secret", Address: upstream.Listener.Addr().String(), ServicePath: "trpc.moox.ops.SecretMgr", MaxBodyBytes: 1024, AllowedMethods: []string{"GetSecretValue"}, AllowedCallers: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	var table gatewayproxy.Table
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	if err != nil {
		t.Fatal(err)
	}
	defer nonces.Close()
	handler := New(Options{NodeID: testNode, Credentials: gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, MaxBodyBytes: 1024, Table: &table, Nonces: nonces})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(t, http.MethodPost, "/api/service/secret/GetSecretValue", []byte(`{"secret_id":"s1"}`), testNode, testSecret))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "plain") {
		t.Fatalf("response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceRouterRejectsInvalidMethodAndPath(t *testing.T) {
	handler, closeHandler := newHandler(t, nil, false, 1024)
	defer closeHandler()
	for name, req := range map[string]*http.Request{
		"HTTP method":  signedRequest(t, http.MethodGet, "/api/service/monitor/GetSnapshot", nil, testNode, testSecret),
		"extra path":   signedRequest(t, http.MethodPost, "/api/service/monitor/GetSnapshot/extra", nil, testNode, testSecret),
		"empty method": signedRequest(t, http.MethodPost, "/api/service/monitor/", nil, testNode, testSecret),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusMethodNotAllowed && recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d", recorder.Code)
			}
		})
	}
}

func TestServiceRouterAuthenticationAndReplay(t *testing.T) {
	handler, closeHandler := newHandler(t, nil, false, 1024)
	defer closeHandler()
	valid := signedRequest(t, http.MethodPost, "/api/service/missing/Call", nil, testNode, testSecret)
	for name, mutate := range map[string]func(*http.Request){
		"bad HMAC":   func(req *http.Request) { req.Header.Set("X-Moox-Signature", strings.Repeat("0", 64)) },
		"wrong node": func(req *http.Request) { req.Header.Set("X-Moox-Target-Node", "other-node") },
	} {
		t.Run(name, func(t *testing.T) {
			req := valid.Clone(context.Background())
			req.Header = valid.Header.Clone()
			mutate(req)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", recorder.Code)
			}
		})
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, valid)
	if first.Code != http.StatusNotFound {
		t.Fatalf("first status = %d", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, valid.Clone(context.Background()))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", second.Code)
	}
}

func TestServiceRouterStatusMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		upstream *httptest.Server
		disabled bool
		maxBody  int64
		path     string
		body     []byte
		want     int
	}{
		"body too large":       {maxBody: 3, path: "/api/service/monitor/GetSnapshot", body: []byte("four"), want: http.StatusRequestEntityTooLarge},
		"missing route":        {maxBody: 1024, path: "/api/service/missing/Call", want: http.StatusNotFound},
		"disabled node":        {maxBody: 1024, path: "/api/service/monitor/GetSnapshot", disabled: true, want: http.StatusServiceUnavailable},
		"unavailable upstream": {maxBody: 1024, path: "/api/service/monitor/GetSnapshot", want: http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			handler, closeHandler := newHandler(t, tc.upstream, tc.disabled, tc.maxBody)
			defer closeHandler()
			req := signedRequest(t, http.MethodPost, tc.path, tc.body, testNode, testSecret)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestServiceRouterRecordsRequestAuthReplayAndUpstreamMetrics(t *testing.T) {
	metrics := &metricRecorder{}
	handler, closeHandler := newHandlerWithMetrics(t, nil, false, 1024, metrics)
	defer closeHandler()
	bad := signedRequest(t, http.MethodPost, "/api/service/monitor/GetSnapshot", nil, testNode, "wrong-secret")
	handler.ServeHTTP(httptest.NewRecorder(), bad)
	valid := signedRequest(t, http.MethodPost, "/api/service/monitor/GetSnapshot", nil, testNode, testSecret)
	handler.ServeHTTP(httptest.NewRecorder(), valid)
	handler.ServeHTTP(httptest.NewRecorder(), valid.Clone(context.Background()))
	if metrics.auth != 1 || metrics.replay != 1 || metrics.upstream["connection"] != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
	if metrics.status[http.StatusUnauthorized] != 2 || metrics.status[http.StatusBadGateway] != 1 {
		t.Fatalf("request statuses = %+v", metrics.status)
	}
}

func TestServiceRouterRecordsUpstreamTimeout(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(50 * time.Millisecond) }))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()
	metrics := &metricRecorder{}
	handler, closeHandler := newHandlerWithRouteTimeout(t, upstream, 1, metrics)
	defer closeHandler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedRequest(t, http.MethodPost, "/api/service/monitor/GetSnapshot", nil, testNode, testSecret))
	if recorder.Code != http.StatusBadGateway || metrics.upstream["timeout"] != 1 {
		t.Fatalf("response=%d metrics=%+v", recorder.Code, metrics)
	}
}

func TestUnauthenticatedRequestsUseBoundedMetricLabels(t *testing.T) {
	state := health.NewState()
	handler, closeHandler := newHandlerWithMetrics(t, nil, false, 1024, state)
	defer closeHandler()
	for index := 0; index < 50; index++ {
		path := fmt.Sprintf("/api/service/random-%d/Method%d", index, index)
		req := signedRequest(t, http.MethodPost, path, nil, testNode, "wrong-secret")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	recorder := httptest.NewRecorder()
	state.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics := recorder.Body.String()
	if strings.Count(metrics, "gateway_requests_total{") != 1 || strings.Count(metrics, "gateway_request_duration_seconds_sum{") != 1 {
		t.Fatalf("unauthenticated requests created unbounded series:\n%s", metrics)
	}
	if strings.Contains(metrics, "random-") || !strings.Contains(metrics, "gateway_auth_failures_total 50") {
		t.Fatalf("unsafe labels or missing auth count:\n%s", metrics)
	}
}

func newHandler(t *testing.T, upstream *httptest.Server, disabled bool, maxBody int64) (http.Handler, func()) {
	return newHandlerWithMetrics(t, upstream, disabled, maxBody, nil)
}

func newHandlerWithMetrics(t *testing.T, upstream *httptest.Server, disabled bool, maxBody int64, metrics Metrics) (http.Handler, func()) {
	return newHandlerWithOptions(t, upstream, disabled, maxBody, 0, metrics)
}

func newHandlerWithRouteTimeout(t *testing.T, upstream *httptest.Server, timeoutMS int64, metrics Metrics) (http.Handler, func()) {
	return newHandlerWithOptions(t, upstream, false, 1024, timeoutMS, metrics)
}

func newHandlerWithOptions(t *testing.T, upstream *httptest.Server, disabled bool, maxBody, timeoutMS int64, metrics Metrics) (http.Handler, func()) {
	t.Helper()
	address := "127.0.0.1:1"
	if upstream != nil {
		address = upstream.Listener.Addr().String()
	}
	snapshot, err := gatewayproxy.NormalizeAndHashState(testNode, disabled, []gatewayproxy.Route{{ServiceID: "monitor", Address: address, ServicePath: "trpc.moox.monitor.MonitorMgr", MaxBodyBytes: 1024, TimeoutMS: timeoutMS, AllowedMethods: []string{"*"}, AllowedCallers: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	var table gatewayproxy.Table
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(Options{NodeID: testNode, Credentials: gatewayauth.Credentials{KeyID: testKeyID, Secret: testSecret}, MaxBodyBytes: maxBody, Table: &table, Nonces: nonces, Disabled: func() bool { return disabled }, Metrics: metrics})
	return handler, func() { _ = nonces.Close() }
}

type metricRecorder struct {
	auth, replay int
	upstream     map[string]int
	status       map[int]int
}

func (metrics *metricRecorder) AuthFailed()   { metrics.auth++ }
func (metrics *metricRecorder) ReplayFailed() { metrics.replay++ }
func (metrics *metricRecorder) UpstreamFailed(kind string) {
	if metrics.upstream == nil {
		metrics.upstream = map[string]int{}
	}
	metrics.upstream[kind]++
}
func (metrics *metricRecorder) ObserveRequest(_ string, _ string, status int, _ time.Duration) {
	if metrics.status == nil {
		metrics.status = map[int]int{}
	}
	metrics.status[status]++
}

func signedRequest(t *testing.T, method, path string, body []byte, nodeID, secret string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	headers, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: testKeyID, Secret: secret}, gatewayauth.Request{Method: method, Path: path, TargetNode: nodeID, Body: body}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req
}

func newUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trpc.moox.monitor.MonitorMgr/GetSnapshot" || r.Header.Get("X-Trace-Id") != "trace-123" {
			t.Errorf("upstream request = %s headers=%v", r.URL.Path, r.Header)
		}
		w.Header().Set("trpc-ret", "0")
		w.Header().Set("X-Trace-Id", r.Header.Get("X-Trace-Id"))
		w.WriteHeader(http.StatusCreated)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}
