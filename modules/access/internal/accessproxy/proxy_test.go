package accessproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const primaryService = "trpc.moox.storage.PrimaryStore"

type forwardFunc func(context.Context, string, string, int, []byte) ([]byte, error)

func (f forwardFunc) Forward(ctx context.Context, service, method string, encoding int, body []byte) ([]byte, error) {
	return f(ctx, service, method, encoding, body)
}

type nonceFunc func(context.Context, string, string, time.Duration) (bool, error)

func (f nonceFunc) Consume(ctx context.Context, ns, nonce string, ttl time.Duration) (bool, error) {
	return f(ctx, ns, nonce, ttl)
}

func fixtureProxy(t *testing.T, gateway Forwarder) (*Proxy, gatewayauth.Credentials) {
	t.Helper()
	credential := gatewayauth.Credentials{Caller: "scf-collector", KeyID: "assigned-scf-38", Secret: "fixture-external-key-at-least-32-bytes"}
	registry, err := gatewayauth.NewCredentialRegistry([]gatewayauth.Credentials{credential})
	require.NoError(t, err)
	nonces, err := OpenSQLiteNonces(filepath.Join(t.TempDir(), "nonces.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, nonces.Close()) })
	proxy, err := New(Options{HostID: "storage", Credentials: registry, Gateway: gateway, Nonces: nonces, Registerer: prometheus.NewRegistry()})
	require.NoError(t, err)
	return proxy, credential
}

func signedContext(t *testing.T, credential gatewayauth.Credentials, target, service, method string, body []byte) context.Context {
	t.Helper()
	ctx, message := codec.WithNewMessage(context.Background())
	t.Cleanup(func() { codec.PutBackMessage(message) })
	path := "/" + service + "/" + method
	message.WithServerRPCName(path)
	message.WithSerializationType(codec.SerializationTypePB)
	headers, err := gatewayauth.Sign(credential, gatewayauth.Request{Method: "POST", Path: path, TargetNode: target, Callee: service, Func: method, Body: body}, time.Now())
	require.NoError(t, err)
	metadata := codec.MetaData{}
	for key, values := range headers {
		metadata[key] = []byte(values[0])
	}
	message.WithServerMetaData(metadata)
	return ctx
}

func TestProxyUsesOnlyCatalogGrantsAndRecordsDeniedMethods(t *testing.T) {
	called := 0
	proxy, credential := fixtureProxy(t, forwardFunc(func(_ context.Context, _, _ string, _ int, body []byte) ([]byte, error) { called++; return body, nil }))
	body := []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}
	for _, call := range []struct{ service, method string }{
		{primaryService, "EnsureDatasetPeriod"}, {primaryService, "CommitTimeSeriesBatch"},
		{primaryService, "RecordDatasetPeriodFailures"}, {primaryService, "GetDatasetPeriodStatus"},
		{"trpc.moox.collector.MarketFetchRuntime", "ClaimTimerBatch"},
	} {
		response, err := proxy.Forward(signedContext(t, credential, "access@storage", call.service, call.method, body), &codec.Body{Data: body})
		require.NoError(t, err)
		require.Equal(t, body, response.Data)
	}
	require.Equal(t, 5, called)
	for _, method := range []string{"UpsertFields", "ReadTimeSeriesRows", "WriteFactorRows", "DeleteDatasetRows", "unbounded-untrusted-method"} {
		_, err := proxy.Forward(signedContext(t, credential, "access@storage", primaryService, method, body), &codec.Body{Data: body})
		require.ErrorContains(t, err, "not allowed")
		label := method
		if strings.HasPrefix(method, "unbounded-") {
			label = "unknown"
		}
		require.Equal(t, 1.0, testutil.ToFloat64(proxy.denials.WithLabelValues("scf-collector", primaryService, label, "permission")))
	}
	require.Equal(t, 5, called)
}

func TestProxyRejectsReplayTamperingWrongInstanceUnknownCallerAndNonceFailure(t *testing.T) {
	called := 0
	proxy, credential := fixtureProxy(t, forwardFunc(func(_ context.Context, _, _ string, _ int, body []byte) ([]byte, error) { called++; return body, nil }))
	body := []byte("signed bytes")
	ctx := signedContext(t, credential, "access@storage", primaryService, "CommitTimeSeriesBatch", body)
	_, err := proxy.Forward(ctx, &codec.Body{Data: body})
	require.NoError(t, err)
	_, err = proxy.Forward(ctx, &codec.Body{Data: body})
	require.ErrorContains(t, err, "replayed")
	require.Equal(t, 1.0, testutil.ToFloat64(proxy.denials.WithLabelValues("scf-collector", primaryService, "CommitTimeSeriesBatch", "replay")))
	for _, wrong := range []struct {
		credential gatewayauth.Credentials
		target     string
		body       []byte
	}{
		{credential, "storage", body}, {credential, "access@compute-1", body}, {credential, "access@storage", []byte("tampered")},
		{gatewayauth.Credentials{Caller: "scf-collector", KeyID: "unknown-key", Secret: credential.Secret}, "access@storage", body},
	} {
		_, err := proxy.Forward(signedContext(t, wrong.credential, wrong.target, primaryService, "CommitTimeSeriesBatch", wrong.body), &codec.Body{Data: body})
		require.ErrorContains(t, err, "authentication failed")
	}
	unknown := credential
	unknown.Caller = "unregistered"
	proxy.options.Credentials, err = gatewayauth.NewCredentialRegistry([]gatewayauth.Credentials{unknown})
	require.NoError(t, err)
	_, err = proxy.Forward(signedContext(t, unknown, "access@storage", primaryService, "CommitTimeSeriesBatch", body), &codec.Body{Data: body})
	require.ErrorContains(t, err, "not allowed")
	require.Equal(t, 1.0, testutil.ToFloat64(proxy.denials.WithLabelValues("unknown", primaryService, "CommitTimeSeriesBatch", "permission")))
	proxy.options.Credentials, err = gatewayauth.NewCredentialRegistry([]gatewayauth.Credentials{credential})
	require.NoError(t, err)
	proxy.options.Nonces = nonceFunc(func(context.Context, string, string, time.Duration) (bool, error) {
		return false, errors.New("unavailable")
	})
	_, err = proxy.Forward(signedContext(t, credential, "access@storage", primaryService, "CommitTimeSeriesBatch", body), &codec.Body{Data: body})
	require.ErrorContains(t, err, "replay store unavailable")
	require.Equal(t, 1, called)
	_, err = New(Options{HostID: "storage", Credentials: proxy.options.Credentials, Gateway: proxy.options.Gateway})
	require.ErrorContains(t, err, "durable nonce store")
}

func TestProxyDropsExternalUserAuthorizationAndRejectsAmbiguousMetadata(t *testing.T) {
	called := 0
	proxy, credential := fixtureProxy(t, forwardFunc(func(ctx context.Context, _, _ string, _ int, body []byte) ([]byte, error) {
		called++
		require.Equal(t, gatewayclient.CallMetadata{SpaceID: "crypto", TraceID: "trace-1"}, gatewayclient.CallMetadataFromContext(ctx))
		return body, nil
	}))
	body := []byte("signed bytes")
	ctx := signedContext(t, credential, "access@storage", primaryService, "CommitTimeSeriesBatch", body)
	metadata := codec.Message(ctx).ServerMetaData()
	metadata["X-User-Id"], metadata["X-User-Role"] = []byte("forged-user"), []byte("admin")
	metadata["X-Space-Id"], metadata["X-Trace-Id"] = []byte("crypto"), []byte("trace-1")
	_, err := proxy.Forward(ctx, &codec.Body{Data: body})
	require.NoError(t, err)
	ctx = signedContext(t, credential, "access@storage", primaryService, "CommitTimeSeriesBatch", body)
	metadata = codec.Message(ctx).ServerMetaData()
	metadata["X-Space-Id"], metadata["x-space-id"] = []byte("crypto"), []byte("other")
	_, err = proxy.Forward(ctx, &codec.Body{Data: body})
	require.ErrorContains(t, err, "invalid access application metadata")
	require.Equal(t, 1, called)
}

type directorySource struct{ directory servicecatalog.Directory }

func (s directorySource) Fetch(_ context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	return gatewayclient.DirectoryUpdate{Changed: version != s.directory.Version, Directory: s.directory}, nil
}

func signingCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return path
}

func TestNativeAccessForwardsExactBytesThroughSharedInternalClient(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	directory := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{"storage": {Address: "storage.example.test"}}, Services: map[string][]string{}}
	credentials := []gatewayauth.Credentials{}
	for _, principal := range catalog.Principals {
		credentials = append(credentials, gatewayauth.Credentials{Caller: principal.ID, KeyID: "assigned-" + principal.ID + "-73", Secret: "fixture-external-signing-key-at-least-32-bytes"})
		for _, grant := range principal.Allow {
			directory.Services[grant.Service] = []string{"storage"}
		}
	}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	internal := gatewayauth.Credentials{Caller: "access", KeyID: "assigned-internal-access-85", Secret: "fixture-internal-access-signing-key-at-least-32-bytes"}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	upstream := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	require.NoError(t, upstream.Register(&server.ServiceDesc{ServiceName: "trpc.moox.test.Upstream", HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, decode server.FilterFunc) (interface{}, error) {
		request := &codec.Body{}
		filters, err := decode(request)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, request, func(ctx context.Context, req interface{}) (interface{}, error) {
			message := codec.Message(ctx)
			service, method, _ := strings.Cut(strings.TrimPrefix(message.ServerRPCName(), "/"), "/")
			headers := http.Header{}
			for key, value := range message.ServerMetaData() {
				headers.Add(key, string(value))
			}
			_, err := gatewayauth.Verify(internal, gatewayauth.Request{Method: "POST", Path: message.ServerRPCName(), TargetNode: "storage", Callee: service, Func: method, Body: request.Data}, headers, time.Now())
			if err != nil {
				return nil, err
			}
			require.True(t, catalog.Allowed("access", service, method))
			require.Equal(t, "crypto", headers.Get("X-Space-Id"))
			require.Empty(t, headers.Get("X-User-Role"))
			return &codec.Body{Data: append([]byte(nil), request.Data...)}, nil
		})
	}}}}, struct{}{}))
	go func() { _ = upstream.Serve() }()
	t.Cleanup(func() { _ = upstream.Close(nil) })
	gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: internal, LocalHostID: "storage", LocalAddress: listener.Addr().String(), CAFile: signingCA(t), Source: directorySource{directory}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	proxy, _ := fixtureProxy(t, gateway)
	proxy.options.Credentials, err = gatewayauth.NewCredentialRegistry(credentials)
	require.NoError(t, err)
	accessListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	access := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(accessListener), server.WithAddress(accessListener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	require.NoError(t, RegisterAccessService(access, proxy))
	go func() { _ = access.Serve() }()
	t.Cleanup(func() { _ = access.Close(nil) })
	for index, principal := range catalog.Principals {
		external, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.External, Credentials: credentials[index], AccessAddress: accessListener.Addr().String(), AccessInstanceID: "access@storage"})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, external.Close()) })
		ctx, cancel := context.WithTimeout(gatewayclient.WithCallMetadata(t.Context(), gatewayclient.CallMetadata{SpaceID: "crypto", TraceID: "trace-fixture", UserRole: "forged-admin"}), 5*time.Second)
		defer cancel()
		for _, grant := range principal.Allow {
			for _, method := range grant.Methods {
				for _, request := range []struct {
					encoding int
					body     []byte
				}{
					{codec.SerializationTypePB, []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}},
					{codec.SerializationTypeJSON, []byte(" {\n\"value\": 1e+03 }\n")},
				} {
					response, err := external.Forward(ctx, grant.Service, method, request.encoding, request.body)
					require.NoError(t, err, "%s %s/%s", principal.ID, grant.Service, method)
					require.Equal(t, request.body, response)
				}
			}
		}
	}
}
