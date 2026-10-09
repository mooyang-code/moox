package gatewayclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func testCertificates(t *testing.T) (string, string, string) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test MooX CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"storage"}, Subject: pkix.Name{CommonName: "storage"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, rootKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	ca, cert, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	for name, block := range map[string]*pem.Block{ca: {Type: "CERTIFICATE", Bytes: rootDER}, cert: {Type: "CERTIFICATE", Bytes: leafDER}, keyPath: {Type: "PRIVATE KEY", Bytes: keyDER}} {
		require.NoError(t, os.WriteFile(name, pem.EncodeToMemory(block), 0o600))
	}
	return ca, cert, keyPath
}

func startWireServer(t *testing.T, listener net.Listener, credentials gatewayauth.Credentials, target string) {
	t.Helper()
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName("trpc.moox.test.Gateway"))
	var mu sync.Mutex
	nonces := map[string]bool{}
	desc := &server.ServiceDesc{ServiceName: "trpc.moox.test.Gateway", HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
		request := &codec.Body{}
		filters, err := f(request)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, request, func(ctx context.Context, input interface{}) (interface{}, error) {
			message := codec.Message(ctx)
			headers := http.Header{}
			for key, value := range message.ServerMetaData() {
				headers.Set(key, string(value))
			}
			body := input.(*codec.Body).Data
			claims, err := gatewayauth.Verify(credentials, gatewayauth.Request{Method: "POST", Path: message.ServerRPCName(), TargetNode: target, Callee: secretService, Func: "GetSecret", Body: body}, headers, time.Now())
			if err != nil {
				return nil, err
			}
			mu.Lock()
			replayed := nonces[claims.Nonce]
			nonces[claims.Nonce] = true
			mu.Unlock()
			if replayed {
				t.Error("wire nonce was reused")
			}
			return &codec.Body{Data: body}, nil
		})
	}}}}
	require.NoError(t, svc.Register(desc, struct{}{}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
}

func TestRealTRPCObjectAndRawJSONRoundTrip(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	source := &sourceStub{directory: testDirectory(t, "storage")}
	config := tunnelConfig(source)
	config.Tunnels = tunnelFunc(func(context.Context, string) (string, error) { return listener.Addr().String(), nil })
	startWireServer(t, listener, config.Credentials, "storage")
	c := newTestClient(t, config)
	rsp := &wrapperspb.StringValue{}
	require.NoError(t, c.Invoke(context.Background(), secretService, "GetSecret", wrapperspb.String("signed object"), rsp))
	require.Equal(t, "signed object", rsp.Value)
	body := []byte(" { \"Data\": \"keep bytes\", \"n\": 1 }\n")
	raw, err := c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypeJSON, body)
	require.NoError(t, err)
	require.Equal(t, body, raw)
	// Duplicate protobuf fields are valid but would change if decoded and
	// re-encoded by a forwarding layer.
	body = []byte{0x0a, 0x01, 'a', 0x0a, 0x01, 'b'}
	raw, err = c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypePB, body)
	require.NoError(t, err)
	require.Equal(t, body, raw)
}

type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

func TestRealTRPCTLSRejectsWrongCAAndHostAfterSuccessfulCall(t *testing.T) {
	ca, cert, key := testCertificates(t)
	pair, err := tls.LoadX509KeyPair(cert, key)
	require.NoError(t, err)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	require.NoError(t, err)
	creds := gatewayauth.Credentials{Caller: "moox-cli", KeyID: "test", Secret: "secret"}
	counted := &countingListener{Listener: listener}
	startWireServer(t, counted, creds, "storage")
	body := []byte(`{"id":1}`)
	headers, err := gatewayauth.Sign(creds, gatewayauth.Request{Method: "POST", Path: "/" + secretService + "/GetSecret", TargetNode: "storage", Callee: secretService, Func: "GetSecret", Body: body}, time.Now())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ep := endpoint{address: listener.Addr().String(), target: "storage", caFile: ca, serverName: "storage"}
	pool := newRPCPool()
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	rsp, err := pool.invoke(ctx, ep, secretService, "GetSecret", codec.SerializationTypeJSON, body, headers)
	require.NoError(t, err)
	require.Equal(t, body, rsp)
	headers, err = gatewayauth.Sign(creds, gatewayauth.Request{Method: "POST", Path: "/" + secretService + "/GetSecret", TargetNode: "storage", Callee: secretService, Func: "GetSecret", Body: body}, time.Now())
	require.NoError(t, err)
	_, err = pool.invoke(ctx, ep, secretService, "GetSecret", codec.SerializationTypeJSON, body, headers)
	require.NoError(t, err)
	require.EqualValues(t, 1, counted.accepted.Load(), "same TLS identity reuses its connection")
	ep.serverName = "wrong-host"
	_, err = pool.invoke(ctx, ep, secretService, "GetSecret", codec.SerializationTypeJSON, body, headers)
	require.Error(t, err)
	ep.serverName = "storage"
	ep.caFile, _, _ = testCertificates(t)
	_, err = pool.invoke(ctx, ep, secretService, "GetSecret", codec.SerializationTypeJSON, body, headers)
	require.Error(t, err)
	require.NoError(t, pool.Close())
	pool.mu.Lock()
	remaining := len(pool.conns)
	pool.mu.Unlock()
	require.Zero(t, remaining)
	_, err = pool.Get("tcp", ep.address, connpool.NewGetOptions())
	require.ErrorIs(t, err, net.ErrClosed)
}
