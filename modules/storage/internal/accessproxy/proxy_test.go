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

func testProxy(t *testing.T, invoker Invoker, nonces NonceStore) (*Proxy, gatewayauth.Credentials, gatewayauth.Credentials, time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	inbound := gatewayauth.Credentials{KeyID: "collector", Caller: "collector", Secret: "inbound-secret"}
	upstream := gatewayauth.Credentials{KeyID: "collector", Caller: "collector", Secret: "upstream-secret"}
	proxy, err := New(Options{
		InboundCredentials: inbound, UpstreamCredentials: upstream,
		InboundTargetNode: "access-nj", UpstreamTargetNode: "gateway-nj",
		UpstreamTarget: "ip://127.0.0.1:11003", AllowedCallers: []string{"collector"},
		Nonces: nonces, Now: func() time.Time { return now }, Invoker: invoker,
	})
	require.NoError(t, err)
	return proxy, inbound, upstream, now
}

func inboundContext(t *testing.T, credentials gatewayauth.Credentials, now time.Time, rpcName string, body []byte) context.Context {
	t.Helper()
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerRPCName(rpcName)
	msg.WithSerializationType(codec.SerializationTypePB)
	headers, err := gatewayauth.Sign(credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: rpcName, TargetNode: "access-nj",
		Callee: PrimaryStoreName, Func: "UpsertFields", Body: body,
	}, now)
	require.NoError(t, err)
	metadata := codec.MetaData{}
	for key, values := range headers {
		metadata[key] = []byte(values[0])
	}
	msg.WithServerMetaData(metadata)
	return ctx
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
	for _, method := range []string{"ResolveSubjects", "UpdateView", "UpsertViewColumn", "RequestViewRebuild"} {
		require.True(t, methodAllowed(MetadataName, method), method)
	}
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
