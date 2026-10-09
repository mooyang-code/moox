package garbage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type directorySource struct{ directory servicecatalog.Directory }

func (s directorySource) Fetch(_ context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	return gatewayclient.DirectoryUpdate{Changed: version != s.directory.Version, Directory: s.directory}, nil
}

func TestGarbageUsesProvisionedAdminIdentityThroughNativeGateway(t *testing.T) {
	db := newTestDB(t)
	store, err := keys.NewStore(db, "garbage-fixture-master")
	require.NoError(t, err)
	key, err := store.Ensure(t.Context(), "admin")
	require.NoError(t, err)
	credentials := key.Credentials()
	require.Equal(t, "admin", credentials.Caller)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	nonces := map[string]bool{}
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	descriptor := &server.ServiceDesc{ServiceName: "trpc.moox.hostgateway.ServiceGateway", HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ any, ctx context.Context, decode server.FilterFunc) (any, error) {
		raw := &codec.Body{}
		filters, err := decode(raw)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, raw, func(ctx context.Context, input any) (any, error) {
			message := codec.Message(ctx)
			if message.ServerRPCName() != "/trpc.moox.cloudnode.CloudNodeMgr/CollectGarbage" || message.SerializationType() != codec.SerializationTypeJSON {
				return nil, errors.New("unexpected admin garbage call")
			}
			headers := http.Header{}
			for name, value := range message.ServerMetaData() {
				headers.Set(name, string(value))
			}
			body := input.(*codec.Body).Data
			claims, err := gatewayauth.Verify(credentials, gatewayauth.Request{Method: "POST", Path: message.ServerRPCName(), TargetNode: "control", Callee: "trpc.moox.cloudnode.CloudNodeMgr", Func: "CollectGarbage", Body: body}, headers, time.Now())
			if err != nil {
				return nil, err
			}
			var request struct {
				DryRun bool `json:"dry_run"`
			}
			if err := json.Unmarshal(body, &request); err != nil || request.DryRun {
				return nil, errors.New("garbage request changed")
			}
			mu.Lock()
			defer mu.Unlock()
			if nonces[claims.Nonce] {
				return nil, errors.New("nonce reused")
			}
			nonces[claims.Nonce] = true
			return &codec.Body{Data: []byte(`{"ret_info":{"code":0},"packages":2,"cos_objects":3,"cos_bytes":"250","deleted_nodes":1}`)}, nil
		})
	}}}}
	require.NoError(t, service.Register(descriptor, struct{}{}))
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	directory := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}, Services: map[string][]string{"trpc.moox.cloudnode.CloudNodeMgr": {"control"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &private.PublicKey, private)
	require.NoError(t, err)
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	gateway, err := gatewayclient.New(gatewayclient.Config{CAFile: caPath, Mode: gatewayclient.Internal, Credentials: credentials, LocalHostID: "control", LocalAddress: listener.Addr().String(), Serialization: codec.SerializationTypeJSON, Source: directorySource{directory}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	collector, err := NewCollector(db, gateway)
	require.NoError(t, err)
	require.NoError(t, collector.Run(t.Context()))
	require.NoError(t, collector.Run(t.Context()))
	mu.Lock()
	count := len(nonces)
	mu.Unlock()
	require.Equal(t, 2, count)
	err = gateway.Invoke(t.Context(), "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeList", struct{}{}, &struct{}{})
	require.ErrorContains(t, err, "not allowed")
}
