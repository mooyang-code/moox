package accessproxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
)

type memoryNonces struct{ seen map[string]struct{} }

func (m *memoryNonces) Consume(_ context.Context, namespace, nonce string, _ time.Duration) (bool, error) {
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

type captureInvoker struct {
	called  bool
	options *client.Options
}

func (c *captureInvoker) Invoke(_ context.Context, _ interface{}, rsp interface{}, opts ...client.Option) error {
	c.called = true
	c.options = &client.Options{}
	for _, opt := range opts {
		opt(c.options)
	}
	rsp.(*codec.Body).Data = []byte("upstream-response")
	return nil
}

func testPrincipal(t *testing.T, name, preset, inboundCaller, upstreamCaller string) Principal {
	t.Helper()
	methods, err := presetMethods(preset)
	require.NoError(t, err)
	return Principal{
		Name:     name,
		Inbound:  gatewayauth.Credentials{KeyID: inboundCaller, Caller: inboundCaller, Secret: inboundCaller + "-inbound-secret"},
		Upstream: gatewayauth.Credentials{KeyID: upstreamCaller, Caller: upstreamCaller, Secret: upstreamCaller + "-upstream-secret"},
		Methods:  methods,
	}
}

func testProxy(t *testing.T, invoker Invoker, nonces NonceStore) (*Proxy, gatewayauth.Credentials, gatewayauth.Credentials, time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	collector := testPrincipal(t, "collector", "collector", "collector", "collector")
	engine := testPrincipal(t, "factor-engine", "factor-engine", "factor-engine", "factor")
	proxy, err := New(Options{
		Principals:        []Principal{collector, engine},
		InboundTargetNode: "access-nj", UpstreamTargetNode: "gateway-nj",
		UpstreamTarget: "ip://127.0.0.1:11003",
		Nonces:         nonces, Now: func() time.Time { return now }, Invoker: invoker,
	})
	require.NoError(t, err)
	return proxy, collector.Inbound, engine.Inbound, now
}

func inboundContext(t *testing.T, credentials gatewayauth.Credentials, now time.Time, rpcName string, body []byte) context.Context {
	t.Helper()
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerRPCName(rpcName)
	msg.WithSerializationType(codec.SerializationTypePB)
	servicePath, method, ok := splitRPCName(rpcName)
	require.True(t, ok)
	headers, err := gatewayauth.Sign(credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: rpcName, TargetNode: "access-nj",
		Callee: servicePath, Func: method, Body: body,
	}, now)
	require.NoError(t, err)
	metadata := codec.MetaData{}
	for key, values := range headers {
		metadata[key] = []byte(values[0])
	}
	msg.WithServerMetaData(metadata)
	return ctx
}

func TestProxyAllowsCollectorPeriodMethods(t *testing.T) {
	for _, method := range []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus"} {
		t.Run(method, func(t *testing.T) {
			invoker := &captureInvoker{}
			proxy, inbound, _, now := testProxy(t, invoker, &memoryNonces{})
			body := []byte("protobuf-body")
			rpcName := "/trpc.moox.storage.PrimaryStore/" + method
			ctx := inboundContext(t, inbound, now, rpcName, body)

			rsp, err := proxy.Forward(ctx, &codec.Body{Data: body})
			require.NoError(t, err)
			require.Equal(t, []byte("upstream-response"), rsp.Data)
			require.True(t, invoker.called)
			require.Equal(t, method, invoker.options.CalleeMethod)
		})
	}
}

func TestProxyAllowsCollectorStorageMethodAndBuildsUpstreamOptions(t *testing.T) {
	invoker := &captureInvoker{}
	proxy, inbound, _, now := testProxy(t, invoker, &memoryNonces{})
	body := []byte("protobuf-body")
	ctx := inboundContext(t, inbound, now, "/trpc.moox.storage.PrimaryStore/UpsertFields", body)

	rsp, err := proxy.Forward(ctx, &codec.Body{Data: body})
	require.NoError(t, err)
	require.Equal(t, []byte("upstream-response"), rsp.Data)
	require.True(t, invoker.called)
	require.Equal(t, "ip://127.0.0.1:11003", invoker.options.Target)
	require.Equal(t, PrimaryStoreName, invoker.options.ServiceName)
	require.Equal(t, "UpsertFields", invoker.options.CalleeMethod)
	require.Equal(t, codec.SerializationTypeNoop, invoker.options.CurrentSerializationType)
}

func TestProxyRejectsDisallowedMethodBeforeUpstream(t *testing.T) {
	invoker := &captureInvoker{}
	proxy, inbound, _, now := testProxy(t, invoker, &memoryNonces{})
	body := []byte("protobuf-body")
	ctx := inboundContext(t, inbound, now, "/trpc.moox.storage.PrimaryStore/DeleteDatasetRows", body)

	_, err := proxy.Forward(ctx, &codec.Body{Data: body})
	require.ErrorContains(t, err, "method is not allowed")
	require.False(t, invoker.called)
}

func TestProxyRejectsInvalidAuthAndReplay(t *testing.T) {
	invoker := &captureInvoker{}
	nonces := &memoryNonces{}
	proxy, inbound, _, now := testProxy(t, invoker, nonces)
	body := []byte("protobuf-body")
	ctx := inboundContext(t, inbound, now, "/trpc.moox.storage.PrimaryStore/UpsertFields", body)
	require.NoError(t, func() error {
		_, err := proxy.Forward(ctx, &codec.Body{Data: body})
		return err
	}())
	_, err := proxy.Forward(ctx, &codec.Body{Data: body})
	require.ErrorContains(t, err, "replayed")

	badCtx := inboundContext(t, inbound, now, "/trpc.moox.storage.PrimaryStore/UpsertFields", []byte("different-body"))
	_, err = proxy.Forward(badCtx, &codec.Body{Data: body})
	require.ErrorContains(t, err, "authentication failed")
}

func TestMethodAllowedIncludesCollectorResampleMethods(t *testing.T) {
	collector := testPrincipal(t, "collector", "collector", "collector", "collector")
	for _, method := range []string{"ResolveSubjects", "UpdateView", "UpsertViewColumn", "RequestViewRebuild"} {
		require.True(t, collector.allows(MetadataName, method), method)
	}
}

func TestFactorEnginePrincipalAllowsSevenMethods(t *testing.T) {
	for _, rpcName := range []string{
		"/trpc.moox.storage.PrimaryStore/ReadTimeSeriesRows", "/trpc.moox.storage.PrimaryStore/WriteFactorRows",
		"/trpc.moox.storage.PrimaryStore/ReportFactorPeriodComputed", "/trpc.moox.storage.PrimaryStore/GetFactorPeriodComputed",
		"/trpc.moox.storage.Metadata/GetDataset", "/trpc.moox.storage.Metadata/ListDatasetColumns",
		"/trpc.moox.storage.Metadata/ListDatasetSubjects",
	} {
		t.Run(rpcName, func(t *testing.T) {
			invoker := &captureInvoker{}
			proxy, _, engine, now := testProxy(t, invoker, &memoryNonces{})
			body := []byte("protobuf-body")

			_, err := proxy.Forward(inboundContext(t, engine, now, rpcName, body), &codec.Body{Data: body})

			require.NoError(t, err)
			require.True(t, invoker.called)
		})
	}
}

func TestFactorEnginePrincipalRejectsOtherMethods(t *testing.T) {
	for _, rpcName := range []string{
		"/trpc.moox.storage.PrimaryStore/DeleteDatasetRows", "/trpc.moox.storage.PrimaryStore/UpsertFields",
		"/trpc.moox.storage.Metadata/DeleteDataset", "/trpc.moox.storage.Metadata/CreateDataset",
	} {
		invoker := &captureInvoker{}
		proxy, _, engine, now := testProxy(t, invoker, &memoryNonces{})
		body := []byte("protobuf-body")

		_, err := proxy.Forward(inboundContext(t, engine, now, rpcName, body), &codec.Body{Data: body})

		require.ErrorContains(t, err, "method is not allowed", rpcName)
		require.False(t, invoker.called)
	}
}

func TestCollectorPrincipalCannotWriteFactorRows(t *testing.T) {
	invoker := &captureInvoker{}
	proxy, collector, _, now := testProxy(t, invoker, &memoryNonces{})
	body := []byte("protobuf-body")

	_, err := proxy.Forward(inboundContext(t, collector, now, "/trpc.moox.storage.PrimaryStore/WriteFactorRows", body), &codec.Body{Data: body})

	require.ErrorContains(t, err, "method is not allowed")
}

func TestUpstreamCredentialsSelectedByPrincipal(t *testing.T) {
	var captured []string
	invoker := &captureInvoker{}
	proxy, _, engine, now := testProxy(t, invoker, &memoryNonces{})
	body := []byte("protobuf-body")

	_, err := proxy.Forward(inboundContext(t, engine, now, "/trpc.moox.storage.PrimaryStore/WriteFactorRows", body), &codec.Body{Data: body})
	require.NoError(t, err)
	require.Len(t, invoker.options.Filters, 1)
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithClientRPCName("/trpc.moox.storage.PrimaryStore/WriteFactorRows")
	require.NoError(t, invoker.options.Filters[0](ctx, &codec.Body{Data: body}, &codec.Body{}, func(ctx context.Context, _, _ interface{}) error {
		captured = append(captured, string(codec.Message(ctx).ClientMetaData()["X-Moox-Caller"]))
		return nil
	}))

	require.Equal(t, []string{"factor"}, captured, "factor-engine is re-signed upstream as the factor caller")
}

func TestUnknownKeyRejected(t *testing.T) {
	invoker := &captureInvoker{}
	proxy, _, _, now := testProxy(t, invoker, &memoryNonces{})
	body := []byte("protobuf-body")
	stranger := gatewayauth.Credentials{KeyID: "stranger", Caller: "stranger", Secret: "stranger-secret"}

	_, err := proxy.Forward(inboundContext(t, stranger, now, "/trpc.moox.storage.PrimaryStore/ReadTimeSeriesRows", body), &codec.Body{Data: body})

	require.ErrorContains(t, err, "authentication failed")
	require.False(t, invoker.called)
}

func TestNewRejectsDuplicateInboundKeys(t *testing.T) {
	collector := testPrincipal(t, "collector", "collector", "collector", "collector")
	_, err := New(Options{
		Principals: []Principal{collector, collector}, InboundTargetNode: "access-nj", UpstreamTargetNode: "gateway-nj",
		UpstreamTarget: "ip://127.0.0.1:11003",
	})
	require.ErrorContains(t, err, "more than one principal")
}

func TestRawGatewayClientFilterSignsUpstreamBody(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	credentials := gatewayauth.Credentials{KeyID: "collector", Caller: "collector", Secret: "upstream-secret"}
	body := []byte("protobuf-body")
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithClientRPCName("/trpc.moox.storage.PrimaryStore/UpsertFields")
	msg.WithCalleeServiceName(PrimaryStoreName)
	msg.WithCalleeMethod("UpsertFields")
	msg.WithSerializationType(codec.SerializationTypePB)
	var headers http.Header
	filter := rawGatewayClientFilter(credentials, "gateway-nj", func() time.Time { return now })
	require.NoError(t, filter(ctx, &codec.Body{Data: body}, &codec.Body{}, func(ctx context.Context, _ interface{}, _ interface{}) error {
		headers = metadataToHeader(codec.Message(ctx).ClientMetaData())
		return nil
	}))
	_, err := gatewayauth.Verify(credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: "/trpc.moox.storage.PrimaryStore/UpsertFields", TargetNode: "gateway-nj",
		Callee: PrimaryStoreName, Func: "UpsertFields", Body: body,
	}, headers, now)
	require.NoError(t, err)
}

func TestValidateTargetRejectsNonIPOrPathTargets(t *testing.T) {
	for _, raw := range []string{"", "http://127.0.0.1:11003", "ip://127.0.0.1", "ip://127.0.0.1:11003/path", "ip://"} {
		require.Error(t, validateTarget(raw), raw)
	}
	require.NoError(t, validateTarget("ip://127.0.0.1:11003"))
}
